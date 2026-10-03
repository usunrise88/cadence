package playbooks

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Plan item states.
const (
	ItemPending = "pending"
	ItemRunning = "running"
	ItemDone    = "done"
	ItemFailed  = "failed"
	ItemSkipped = "skipped"
)

// Playbook states of a session.
const (
	StateRunning = "running"
	StateDone    = "done"
	StateStopped = "stopped"
)

// MaxNudges is how often the server reminds the agent of the next step when a turn ends without progress.
const MaxNudges = 2

// Item is one entry of a playbook session's plan (the contract's PlaybookPlanItem).
type Item struct {
	ID         string        `json:"id"`
	Title      string        `json:"title"`
	Command    string        `json:"command"`
	Accepts    []string      `json:"accepts,omitempty"`
	Until      string        `json:"until,omitempty"`
	Phase      int           `json:"phase,omitempty"`
	Spending   bool          `json:"spending"`
	State      string        `json:"state"`
	Note       string        `json:"note,omitempty"`
	CommandID  string        `json:"commandId,omitempty"`
	ToolCallID string        `json:"toolCallId,omitempty"`
	EntityID   string        `json:"entityId,omitempty"`
	JobID      string        `json:"jobId,omitempty"`
	At         *time.Time    `json:"at,omitempty"`
	Estimate   *StepEstimate `json:"estimate,omitempty"`
}

func (it Item) matches(op string) bool { return it.Command == op || slices.Contains(it.Accepts, op) }

// StopInfo is why a playbook stopped.
type StopInfo struct {
	On      string     `json:"on"`
	When    string     `json:"when"`
	Message string     `json:"message"`
	At      *time.Time `json:"at,omitempty"`
}

// State is a playbook session's playbook (the contract's AgentPlaybook), kept on the session row.
type State struct {
	Name      string         `json:"name"`
	Title     string         `json:"title"`
	VersionID string         `json:"versionId,omitempty"`
	State     string         `json:"state"`
	Inputs    map[string]any `json:"inputs"`
	Estimate  Estimate       `json:"estimate"`
	Plan      []Item         `json:"plan"`
	Stop      *StopInfo      `json:"stop,omitempty"`
	Summary   string         `json:"summary,omitempty"`
	Next      string         `json:"next,omitempty"`
	DryRuns   []string       `json:"dryRuns,omitempty"`
	// DryRunRequests maps each operation of DryRuns to the request fingerprint of its last dry run
	// (commands.HashRequest: method, path, query without dryRun, If-Match and canonical body; never the
	// Idempotency-Key). The real command must carry the same one (DryRunMatches). Not part of the contract.
	DryRunRequests map[string]string `json:"dryRunRequests,omitempty"`
	Nudges         int               `json:"nudges,omitempty"`
	// Stops and NextText are the template's, carried so the session needs no template lookup.
	Stops    []Stop `json:"stops,omitempty"`
	NextText Next   `json:"nextText"`
}

// NewPlan is the initial plan of p: one item per step, later-phase steps skipped, estimates attached.
func NewPlan(p Playbook, e Estimate) []Item {
	out := make([]Item, 0, len(p.Chain))
	for i, s := range p.Chain {
		it := Item{ID: s.ID, Title: s.Title, Command: s.Command, Accepts: s.Accepts, Until: s.Until, Phase: s.Phase,
			Spending: Spending[s.Command], State: ItemPending}
		if !s.Available() {
			it.State, it.Note = ItemSkipped, phaseNote(s.Phase)
		}
		if i < len(e.Steps) && e.Steps[i].ID == s.ID {
			se := e.Steps[i]
			it.Estimate = &se
		}
		out = append(out, it)
	}
	return out
}

// Observation is one operation of the session that succeeded (a dry run, a command, or a read the chain waits on).
type Observation struct {
	Operation  string
	DryRun     bool
	Request    string // the command's request fingerprint (commands.Command.RequestHash); a dry run records it
	Status     int
	Body       map[string]any
	CommandID  string
	ToolCallID string
	At         time.Time
}

// Current is the index of the plan's current item (the first neither done nor skipped), or -1 when the plan is
// through.
func (st *State) Current() int {
	for i, it := range st.Plan {
		if it.State == ItemPending || it.State == ItemRunning {
			return i
		}
	}
	return -1
}

// Observe applies o to the plan and reports whether anything changed. Only the current item ticks, only from an
// operation it names: a spending command's dry run marks it running (and records the dry run), the real command marks
// it done with the entity and job it answered; a terminal step ticks from the job it waits for once that job has
// ended (failed or cancelled fails it). A successful real spending command uses up its dry run. Then the playbook
// moves on: done when every item is done or skipped, stopped when an item failed and the playbook stops on it.
func (st *State) Observe(o Observation) bool {
	if st.State != StateRunning {
		return false
	}
	changed := false
	if Spending[o.Operation] {
		if o.DryRun {
			if !slices.Contains(st.DryRuns, o.Operation) {
				st.DryRuns = append(st.DryRuns, o.Operation)
				changed = true
			}
			if prev, ok := st.DryRunRequests[o.Operation]; !ok || prev != o.Request {
				if st.DryRunRequests == nil {
					st.DryRunRequests = map[string]string{}
				}
				st.DryRunRequests[o.Operation] = o.Request
				changed = true
			}
		}
		if !o.DryRun {
			if i := slices.Index(st.DryRuns, o.Operation); i >= 0 {
				st.DryRuns = slices.Delete(st.DryRuns, i, i+1)
				changed = true
			}
			if _, ok := st.DryRunRequests[o.Operation]; ok {
				delete(st.DryRunRequests, o.Operation)
				changed = true
			}
		}
	}
	i := st.Current()
	if i < 0 || !st.Plan[i].matches(o.Operation) {
		return changed
	}
	it := &st.Plan[i]
	at := o.At
	switch {
	case o.DryRun:
		if !it.Spending || it.State == ItemRunning {
			return changed
		}
		it.State, it.Note, it.At = ItemRunning, "dry run answered"+estimateText(o.Body), &at
	case it.Until == UntilTerminal:
		if !st.terminal(i, it, o) {
			return changed
		}
		it.At = &at
	default:
		it.State, it.At = ItemDone, &at
		it.EntityID, it.JobID = entityOf(o.Body), jobOf(o.Body)
		switch {
		case it.EntityID != "" && it.JobID != "":
			it.Note = it.EntityID + ", job " + it.JobID
		case it.EntityID != "":
			it.Note = it.EntityID
		case it.JobID != "":
			it.Note = "job " + it.JobID
		default:
			it.Note = o.Operation + " succeeded"
		}
	}
	it.CommandID, it.ToolCallID = o.CommandID, o.ToolCallID
	st.Nudges = 0
	if v := gateVerdict(o.Body); v != "" && it.State == ItemDone {
		it.Note = it.EntityID + ": gate " + v
		if v == "failed" && st.StopOn("gate", "failed", "the eval "+it.EntityID+" failed the gate (evals.get shows the checks)", at) {
			return true
		}
	}
	st.advance(at)
	return true
}

// gateVerdict is the verdict an evals.gate answered (its eval's gate.verdict), or "".
func gateVerdict(body map[string]any) string {
	if g, ok := body["gate"].(map[string]any); ok {
		return str(g["verdict"])
	}
	return ""
}

// before is the nearest earlier item that is done and not itself a wait: what a terminal item waits for.
func (st *State) before(i int) *Item {
	for j := i - 1; j >= 0; j-- {
		if st.Plan[j].State == ItemDone && st.Plan[j].Until == "" && (st.Plan[j].EntityID != "" || st.Plan[j].JobID != "") {
			return &st.Plan[j]
		}
	}
	return nil
}

// terminal applies the answer of a wait to a terminal item and reports whether it changed. A read of an entity with
// a status (runs.get: the run the step before started) ticks it once the status has ended — done, or failed and
// cancelled, which fail it. A job (jobs.wait) ends the item only when it is the job the step before started; any other
// job of it (a run's pipeline has several steps) only marks the item running.
func (st *State) terminal(i int, it *Item, o Observation) bool {
	prev := st.before(i)
	id := str(o.Body["id"])
	status, isJob := str(o.Body["status"]), false
	if _, ok := o.Body["state"]; ok && status == "" {
		status, isJob = str(o.Body["state"]), true
	}
	var ours bool
	switch {
	case prev == nil:
		ours = true
	case isJob:
		ours = prev.EntityID == "" && id == prev.JobID // the step before started a job, not an entity
	default:
		if prev.EntityID != "" && id != prev.EntityID {
			return false // some other run
		}
		ours = true
	}
	what := id
	if isJob {
		what = "job " + id
	}
	ended := status == "done" || status == "failed" || status == "cancelled"
	switch {
	case ours && status == "done":
		it.State, it.Note = ItemDone, what+" done"
	case ours && ended:
		it.State, it.Note = ItemFailed, what+" "+status
		if e := str(o.Body["error"]); e != "" {
			it.Note += ": " + e
		}
	case ended:
		return false // one job of a longer run ended; the run goes on
	default:
		note := "waiting for " + what + " (" + status + ")"
		if it.State == ItemRunning && it.Note == note {
			return false
		}
		it.State, it.Note = ItemRunning, note
	}
	if isJob {
		it.JobID = id
	}
	return true
}

func (st *State) advance(at time.Time) {
	for i, it := range st.Plan {
		if it.State == ItemFailed {
			if st.stops("step") {
				st.End(StateStopped, &StopInfo{On: "step", When: "failed",
					Message: fmt.Sprintf("step %d (%s) failed: %s", i+1, it.Title, it.Note), At: &at})
			}
			return
		}
	}
	if st.Current() < 0 {
		st.End(StateDone, nil)
	}
}

func (st *State) stops(on string) bool {
	return len(st.Stops) == 0 || slices.ContainsFunc(st.Stops, func(s Stop) bool { return s.On == on })
}

// StopOn ends a running playbook on a stop condition the template names (gate, budget, approval); it reports
// whether it stopped.
func (st *State) StopOn(on, when, message string, at time.Time) bool {
	if st.State != StateRunning || !st.stops(on) {
		return false
	}
	st.End(StateStopped, &StopInfo{On: on, When: when, Message: message, At: &at})
	return true
}

// End closes the playbook: done or stopped, with the summary of what the chain did and the next step.
func (st *State) End(state string, stop *StopInfo) {
	st.State, st.Stop = state, stop
	var b strings.Builder
	done := 0
	for _, it := range st.Plan {
		if it.State == ItemDone {
			done++
		}
	}
	switch state {
	case StateDone:
		fmt.Fprintf(&b, "The playbook %q is complete: %d step(s) done", st.Title, done)
		st.Next = st.NextText.Done
	default:
		fmt.Fprintf(&b, "The playbook %q stopped (%s %s): %s. %d step(s) done", st.Title, stop.On, stop.When, stop.Message, done)
		st.Next = st.NextText.Stopped
	}
	var parts []string
	for _, it := range st.Plan {
		p := it.ID + " " + it.State
		if it.Note != "" && it.State != ItemPending {
			p += " (" + it.Note + ")"
		}
		parts = append(parts, p)
	}
	b.WriteString(" — " + strings.Join(parts, "; ") + ".")
	st.Summary = b.String()
}

// DryRunMatches reports whether the session's last dry run of op since its last real call was this very request
// (request is the real command's fingerprint), and whether op had such a dry run at all. A dry run of a cheap
// request never admits a different, expensive one: only path, query, If-Match and body equal to the dry run's do.
func (st *State) DryRunMatches(op, request string) (matches, dryRun bool) {
	if !slices.Contains(st.DryRuns, op) {
		return false, false
	}
	prev, ok := st.DryRunRequests[op]
	return ok && prev != "" && prev == request, true
}

// NextItem names the current item for a reminder, or "".
func (st *State) NextItem() string {
	i := st.Current()
	if i < 0 {
		return ""
	}
	it := st.Plan[i]
	s := fmt.Sprintf("step %d of %d, %q (%s", i+1, len(st.Plan), it.Title, it.Command)
	if it.Spending && !slices.Contains(st.DryRuns, it.Command) {
		s += ", dry run first"
	}
	return s + ")"
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// entityOf is the entity a command answered: its id, or the pipeline run it started (runs.calibrate,
// checkpoints.average).
func entityOf(body map[string]any) string {
	if id := str(body["id"]); id != "" {
		return id
	}
	if m, ok := body["pipelineRun"].(map[string]any); ok {
		return str(m["id"])
	}
	return ""
}

func jobOf(body map[string]any) string {
	if j := str(body["jobId"]); j != "" {
		return j
	}
	if j := str(body["currentJobId"]); j != "" {
		return j
	}
	if m, ok := body["job"].(map[string]any); ok {
		return str(m["id"])
	}
	return ""
}

func estimateText(body map[string]any) string {
	gh, ok := body["gpuHours"].(map[string]any)
	if !ok {
		if e, ok2 := body["estimate"].(map[string]any); ok2 {
			gh, ok = e["gpuHours"].(map[string]any)
		}
	}
	if !ok {
		return ""
	}
	v, _ := number(gh["value"])
	return fmt.Sprintf(": %.2f GPU-hours", v)
}
