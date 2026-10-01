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
}

// classTable names the event types of each class that need no look at the payload. Other streams add their event
// types here as they emit them (mount health, gates, promotions, schedules, batches, checkpoints; card slots once a
// card's own health closes its slot); the web shell's in-app history mirrors this table
// (web/src/shell/notifications/classes.ts; TestClassTableMatchesWeb keeps the two equal). Types classified by their
// payload (approval.requested, job.state_changed, pipeline_run.step_changed, compute.health) are in Classify.
var classTable = map[string]string{
	// failure
	"backup.failed":         ClassFailure,
	"backup.restore_failed": ClassFailure,
	"mount.unhealthy":       ClassFailure,
	"storage.low_space":     ClassFailure,
	// outcome
	"gate.verdict":        ClassOutcome,
	"deployment.promoted": ClassOutcome,
	"schedule.finished":   ClassOutcome,
	"batch.closed":        ClassOutcome,
	// progress
	"backup.succeeded":      ClassProgress,
	"backup.restore_passed": ClassProgress,
	"checkpoint.saved":      ClassProgress,
	"triage.item_added":     ClassProgress,
}

// EventTypes lists the event types of class (for the Settings table), job failures and approvals included.
func EventTypes(class string) []string {
	out := []string{}
	switch class {
	case ClassApproval:
		out = append(out, "approval.requested")
	case ClassFailure:
		out = append(out, "job.state_changed (failed)", "pipeline_run.step_changed (failed)", "compute.health (unreachable)")
	case ClassProgress:
		out = append(out, "job.state_changed (done)", "pipeline_run.step_changed (done)")
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
// tells (pipeline_run.step_changed on pipeline_run.{id}: done is progress, failed — no retry left — a failure). A
// host turning unreachable (compute.health on compute.{id}) is a failure. The other classified types count on any
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
			return Notice{Class: ClassFailure, Title: "Step failed: " + name, Body: join(msg, "Pipeline run: "+p.PipelineRunID)}, true
		case "done":
			return Notice{Class: ClassProgress, Title: "Step done: " + name, Body: "Pipeline run: " + p.PipelineRunID}, true
		}
		return Notice{}, false
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

// humanize turns "backup.restore_failed" into "Backup restore failed".
func humanize(typ string) string {
	s := strings.NewReplacer(".", " ", "_", " ").Replace(typ)
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
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
