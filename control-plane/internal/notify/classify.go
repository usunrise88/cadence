package notify

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/events"
)

// Notice is what one event says to a person on a channel.
type Notice struct {
	Class      string
	Title      string
	Body       string
	ApprovalID string // an approval request: the Telegram message carries Approve / Deny buttons
	// Key, when set, makes the notice once-only: a later event with the same key is not sent again (an end that
	// several events repeat, such as an agent session's).
	Key string
}

// classTable names the event types of each class that need no look at the payload. Other streams add their event
// types here as they emit them (mount health, gates, promotions, schedules, batches, checkpoints; card slots once a
// card's own health closes its slot); the web shell's in-app history mirrors this table
// (web/src/shell/notifications/classes.ts; TestClassTableMatchesWeb keeps the two equal). Types classified by their
// payload (approval.requested, job.state_changed, pipeline_run.step_changed, compute.health, eval.status_changed) are
// in Classify. A gate verdict is eval.gated (evals.gate; passed or failed, both an outcome).
var classTable = map[string]string{
	// failure
	"backup.failed":         ClassFailure,
	"backup.restore_failed": ClassFailure,
	"mount.unhealthy":       ClassFailure,
	"storage.low_space":     ClassFailure,
	// outcome
	"eval.gated":          ClassOutcome,
	"sweep.ended":         ClassOutcome,
	"deployment.promoted": ClassOutcome,
	"schedule.finished":   ClassOutcome,
	"batch.closed":        ClassOutcome,
	"branch.waiting":      ClassOutcome,
	// progress
	"backup.succeeded":      ClassProgress,
	"backup.restore_passed": ClassProgress,
	"checkpoint.saved":      ClassProgress,
	"triage.item_added":     ClassProgress,
	"golden_set.frozen":     ClassProgress,
}

// evalRunPrefix starts the id of an eval (evals.Kind's evl_…): the steps of an eval's pipeline run are not told one
// by one (an eval of a few hundred cells would send as many notices); the eval's end is (eval.status_changed).
const evalRunPrefix = "evl_"

// EventTypes lists the event types of class (for the Settings table), job failures and approvals included.
func EventTypes(class string) []string {
	out := []string{}
	switch class {
	case ClassApproval:
		out = append(out, "approval.requested")
	case ClassFailure:
		out = append(out, "job.state_changed (failed)", "pipeline_run.state_changed (failed, not an eval's)",
			"eval.status_changed (failed)", "agent_session.changed (failed)", "compute.health (unreachable)")
	case ClassProgress:
		out = append(out, "job.state_changed (done)", "pipeline_run.step_changed (done or failed, not an eval's)",
			"eval.status_changed (done)")
	case ClassDigest:
		out = append(out, "notification.digest")
	}
	for _, t := range slices.Sorted(maps.Keys(classTable)) {
		if classTable[t] == class {
			out = append(out, t)
		}
	}
	return out
}

type approvalPayload struct {
	Approval *approvalView `json:"approval"`
}

// approvalView is what a person is told about an approval request (the event payload's approval, or its row).
type approvalView struct {
	ID        string `json:"id"`
	Operation string `json:"operation"`
	ProjectID string `json:"projectId"`
	Reason    string `json:"reason"`
	Actor     struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"actor"`
	Estimate *struct {
		GPUHours          float64  `json:"gpuHours"`
		RemainingGPUHours *float64 `json:"remainingGpuHours"`
	} `json:"estimate"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// notice is the approval's Telegram message (Approve / Deny buttons through ApprovalID).
func (a approvalView) notice() Notice {
	who := a.Actor.Name
	if who == "" {
		who = a.Actor.ID
	}
	lines := []string{fmt.Sprintf("%s %s asks: %s", a.Actor.Kind, who, a.Reason)}
	if a.Estimate != nil {
		est := fmt.Sprintf("Estimate: %.1f GPU-hours", a.Estimate.GPUHours)
		if a.Estimate.RemainingGPUHours != nil {
			est += fmt.Sprintf(" (%.1f left in today's budget)", *a.Estimate.RemainingGPUHours)
		}
		lines = append(lines, est)
	}
	if a.ProjectID != "" {
		lines = append(lines, "Project: "+a.ProjectID)
	}
	if !a.ExpiresAt.IsZero() {
		lines = append(lines, "Expires: "+a.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC"))
	}
	return Notice{Class: ClassApproval, Title: "Approval requested: " + a.Operation, Body: strings.Join(lines, "\n"),
		ApprovalID: a.ID}
}

type jobPayload struct {
	Job *struct {
		ID        string `json:"id"`
		Kind      string `json:"kind"`
		ProjectID string `json:"projectId"`
		State     string `json:"state"`
		Error     string `json:"error"`
		Message   string `json:"message"`
	} `json:"job"`
}

type stepPayload struct {
	PipelineRunID string `json:"pipelineRunId"`
	RunID         string `json:"runId"` // the facade entity the pipeline run belongs to (run_…, evl_…)
	Step          *struct {
		ID    string `json:"id"`
		Step  string `json:"step"`
		Kind  string `json:"kind"`
		State string `json:"state"`
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"step"`
}

type pipelineRunPayload struct {
	PipelineRun *struct {
		ID        string `json:"id"`
		ProjectID string `json:"projectId"`
		Pipeline  string `json:"pipeline"`
		State     string `json:"state"`
		RunID     string `json:"runId"`
		Error     string `json:"error"`
	} `json:"pipelineRun"`
}

// sessionListTopic is sessions.TopicList: an agent session's change is read there once (its own topic repeats it).
const sessionListTopic = "agent.sessions"

type sessionPayload struct {
	Session *struct {
		ID      string `json:"id"`
		Number  int    `json:"number"`
		Project string `json:"project"`
		State   string `json:"state"`
		Error   string `json:"error"`
	} `json:"session"`
}

type evalPayload struct {
	Eval *struct {
		ID        string `json:"id"`
		ProjectID string `json:"projectId"`
		Status    string `json:"status"`
		Error     string `json:"error"`
		Subject   *struct {
			Label string `json:"label"`
			ID    string `json:"id"`
		} `json:"subject"`
		Gate *struct {
			Verdict  string `json:"verdict"`
			GatesSHA string `json:"gatesSha"`
		} `json:"gate"`
	} `json:"eval"`
}

// subject names the eval's subject model (its label, else its id).
func (p evalPayload) subject() string {
	if s := p.Eval.Subject; s != nil {
		if s.Label != "" {
			return s.Label
		}
		return s.ID
	}
	return p.Eval.ID
}

type sweepPayload struct {
	Experiment *struct {
		ID    string `json:"id"`
		Sweep *struct {
			ID         string `json:"id"`
			State      string `json:"state"`
			StopReason string `json:"stopReason"`
			Points     int    `json:"points"`
			Ended      int    `json:"ended"`
		} `json:"sweep"`
	} `json:"experiment"`
}

type versionPayload struct {
	Version *struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"version"`
}

type healthPayload struct {
	HostID string `json:"hostId"`
	Health *struct {
		State  string `json:"state"`
		Detail string `json:"detail"`
	} `json:"health"`
}

type genericPayload struct {
	Title   string `json:"title"`
	Message string `json:"message"`
	Error   string `json:"error"`
	Backup  *struct {
		ID      string `json:"id"`
		Trigger string `json:"trigger"`
		Error   string `json:"error"`
	} `json:"backup"`
}

// Classify says which class an event belongs to and what it tells a person; ok is false for events no one is
// notified about. Approval events go out twice (the approvals topic and the entity topic): only the approvals topic
// counts, and a job's state change counts on its job topic only — except a step job's, which its pipeline step
// tells (pipeline_run.step_changed on pipeline_run.{id}). A failure is told by what it ends, once: a step's failure
// is progress whatever follows (an optional step's lets the run go on; an OOM or a lost lease is retried without a
// failed step event at all; any other ends the pipeline run, whose pipeline_run.state_changed failed is the one
// failure notice). An agent session that ends failed (agent_session.changed on agent.sessions) is a failure, once
// per session (Notice.Key). A host turning unreachable (compute.health on compute.{id}) is a failure. The steps of
// an eval's pipeline run, and the run itself, tell nothing; the eval tells its end (eval.status_changed: failed a
// failure, done progress) and evals.gate its
// verdict (eval.gated, an outcome), sweeps their end (sweep.ended) and golden sets their freeze (golden_set.frozen),
// all on their entity topics, which only these announce on. The other classified types count on any
// topic but an entity topic (entity.{kind}.{id} repeats what a domain topic already carried).
func Classify(r events.Record) (Notice, bool) {
	switch r.Type {
	case "approval.requested":
		if r.Topic != "approvals" {
			return Notice{}, false
		}
		var p approvalPayload
		if json.Unmarshal(r.Payload, &p) != nil || p.Approval == nil {
			return Notice{}, false
		}
		return p.Approval.notice(), true
	case "branch.waiting":
		var p struct {
			Project   string `json:"project"`
			Branch    string `json:"branch"`
			Files     int    `json:"files"`
			Conflicts int    `json:"conflicts"`
			Reason    string `json:"reason"`
		}
		if json.Unmarshal(r.Payload, &p) != nil || p.Branch == "" {
			return Notice{}, false
		}
		body := fmt.Sprintf("%s (%d files", p.Reason, p.Files)
		if p.Conflicts > 0 {
			body += fmt.Sprintf(", %d conflicting", p.Conflicts)
		}
		body += ").\nAccept or discard it in the Recipe document's branch list."
		return Notice{Class: ClassOutcome, Title: fmt.Sprintf("Branch waiting for review: %s (%s)", p.Branch, p.Project),
			Body: body}, true
	case "storage.low_space":
		var p struct {
			TotalBytes         int64 `json:"totalBytes"`
			FreeBytes          int64 `json:"freeBytes"`
			EvictableArtifacts int   `json:"evictableArtifacts"`
			EvictableBytes     int64 `json:"evictableBytes"`
			Permanent          bool  `json:"permanent"`
		}
		if json.Unmarshal(r.Payload, &p) != nil || p.TotalBytes <= 0 {
			return Notice{}, false
		}
		gb := func(b int64) float64 { return float64(b) / 1e9 }
		body := fmt.Sprintf("%.0f GB free of %.0f GB (%.0f %%).", gb(p.FreeBytes), gb(p.TotalBytes),
			100*float64(p.FreeBytes)/float64(p.TotalBytes))
		if p.EvictableArtifacts > 0 {
			body += fmt.Sprintf("\n%d superseded training states (%.0f GB) can be evicted: Settings → Content store.",
				p.EvictableArtifacts, gb(p.EvictableBytes))
			if p.Permanent {
				body += " No backup mirror is configured, so eviction is permanent."
			}
		} else {
			body += "\nNo training state can be evicted; free space on the disk or give the store a larger one."
		}
		return Notice{Class: ClassFailure, Title: "Content store low on space", Body: body}, true
	case "job.state_changed":
		if !strings.HasPrefix(r.Topic, "job.") {
			return Notice{}, false
		}
		var p jobPayload
		if json.Unmarshal(r.Payload, &p) != nil || p.Job == nil {
			return Notice{}, false
		}
		j := p.Job
		if j.Kind == stepJobKind {
			return Notice{}, false // the pipeline step's event tells it, once per step rather than per attempt
		}
		switch j.State {
		case "failed":
			return Notice{Class: ClassFailure, Title: "Job failed: " + j.Kind, Body: join(j.Error, j.Message, j.ID)}, true
		case "done":
			return Notice{Class: ClassProgress, Title: "Job done: " + j.Kind, Body: join(j.Message, j.ID)}, true
		}
		return Notice{}, false
	case "pipeline_run.step_changed":
		if !strings.HasPrefix(r.Topic, "pipeline_run.") {
			return Notice{}, false
		}
		var p stepPayload
		if json.Unmarshal(r.Payload, &p) != nil || p.Step == nil {
			return Notice{}, false
		}
		s := p.Step
		if strings.HasPrefix(p.RunID, evalRunPrefix) {
			return Notice{}, false // an eval's steps: its end is told once (eval.status_changed)
		}
		name := s.Step
		if s.Kind != "" {
			name += " (" + s.Kind + ")"
		}
		switch s.State {
		case "failed":
			var msg string
			if s.Error != nil {
				msg = s.Error.Type + ": " + s.Error.Message
			}
			// Progress, not a failure: a failure that stops the run is told by the run's end (pipeline_run.state_changed).
			return Notice{Class: ClassProgress, Title: "Step failed: " + name, Body: join(msg, "Pipeline run: "+p.PipelineRunID)}, true
		case "done":
			return Notice{Class: ClassProgress, Title: "Step done: " + name, Body: "Pipeline run: " + p.PipelineRunID}, true
		}
		return Notice{}, false
	case "pipeline_run.state_changed":
		if !strings.HasPrefix(r.Topic, "pipeline_run.") {
			return Notice{}, false
		}
		var p pipelineRunPayload
		if json.Unmarshal(r.Payload, &p) != nil || p.PipelineRun == nil || p.PipelineRun.State != "failed" ||
			strings.HasPrefix(p.PipelineRun.RunID, evalRunPrefix) {
			return Notice{}, false
		}
		pr := p.PipelineRun
		title := "Pipeline run failed: " + pr.Pipeline
		if pr.RunID != "" {
			title = "Run failed: " + pr.RunID
		}
		return Notice{Class: ClassFailure, Title: title, Body: join(pr.Error, "Pipeline run: "+pr.ID, project(pr.ProjectID))}, true
	case "agent_session.changed":
		if r.Topic != sessionListTopic {
			return Notice{}, false // the session's own topic repeats it
		}
		var p sessionPayload
		if json.Unmarshal(r.Payload, &p) != nil || p.Session == nil || p.Session.State != "failed" {
			return Notice{}, false
		}
		ss := p.Session
		return Notice{Class: ClassFailure, Title: fmt.Sprintf("Agent session %d failed (%s)", ss.Number, ss.Project),
			Body: join(ss.Error, "Session: "+ss.ID), Key: "agent_session.failed:" + ss.ID}, true
	case "eval.status_changed", "eval.gated":
		// Evals announce on their entity topic only (entity.eval.{id}), so it counts here.
		var p evalPayload
		if json.Unmarshal(r.Payload, &p) != nil || p.Eval == nil {
			return Notice{}, false
		}
		e := p.Eval
		if r.Type == "eval.gated" {
			if e.Gate == nil {
				return Notice{}, false
			}
			body := "Eval: " + e.ID
			if e.Gate.GatesSHA != "" {
				body += "\ngates.yaml at " + shortSHA(e.Gate.GatesSHA)
			}
			return Notice{Class: ClassOutcome, Title: fmt.Sprintf("Gate %s: %s", e.Gate.Verdict, p.subject()), Body: body}, true
		}
		switch e.Status {
		case "failed":
			return Notice{Class: ClassFailure, Title: "Eval failed: " + p.subject(), Body: join(e.Error, "Eval: "+e.ID)}, true
		case "done":
			return Notice{Class: ClassProgress, Title: "Eval done: " + p.subject(), Body: "Eval: " + e.ID + "\nRun evals.gate for the verdict."}, true
		}
		return Notice{}, false
	case "sweep.ended":
		var p sweepPayload
		if json.Unmarshal(r.Payload, &p) != nil || p.Experiment == nil || p.Experiment.Sweep == nil {
			return Notice{}, false
		}
		sw := p.Experiment.Sweep
		body := fmt.Sprintf("%d of %d points ran.", sw.Ended, sw.Points)
		return Notice{Class: classTable[r.Type], Title: "Sweep " + sw.State + ": " + p.Experiment.ID,
			Body: join(sw.StopReason, body, "Sweep: "+sw.ID)}, true
	case "golden_set.frozen":
		var p versionPayload
		if json.Unmarshal(r.Payload, &p) != nil || p.Version == nil {
			return Notice{}, false
		}
		v := p.Version
		return Notice{Class: classTable[r.Type], Title: "Golden set frozen: " + strings.TrimPrefix(v.Name, "golden-set/"),
			Body: join("Version "+v.Version, v.ID)}, true
	case "compute.health":
		if !strings.HasPrefix(r.Topic, "compute.") {
			return Notice{}, false
		}
		var p healthPayload
		if json.Unmarshal(r.Payload, &p) != nil || p.Health == nil || p.Health.State != "unreachable" {
			return Notice{}, false
		}
		return Notice{Class: ClassFailure, Title: "Compute host unreachable", Body: join(p.Health.Detail, p.HostID)}, true
	}
	class, ok := classTable[r.Type]
	if !ok || strings.HasPrefix(r.Topic, "entity.") { // the entity topic repeats an event of a domain topic
		return Notice{}, false
	}
	var p genericPayload
	_ = json.Unmarshal(r.Payload, &p)
	title := p.Title
	body := join(p.Error, p.Message)
	if p.Backup != nil {
		body = join(p.Backup.Error, body, p.Backup.ID)
	}
	if title == "" {
		title = humanize(r.Type)
	}
	return Notice{Class: class, Title: title, Body: body}, true
}

// stepJobKind is steps.JobKind (a pipeline step's job), named here to keep notify free of the steps package.
const stepJobKind = "step"

// shortSHA is a commit's first 12 characters.
func shortSHA(sha string) string { return sha[:min(len(sha), 12)] }

// humanize turns "backup.restore_failed" into "Backup restore failed".
func humanize(typ string) string {
	s := strings.NewReplacer(".", " ", "_", " ").Replace(typ)
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// project is "Project: id", or nothing without one.
func project(id string) string {
	if id == "" {
		return ""
	}
	return "Project: " + id
}

func join(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n")
}
