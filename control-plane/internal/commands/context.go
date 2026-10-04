package commands

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/policy"
)

type approverKey struct{}

// WithReplayApprover names the person deciding the approval a replay carries: the approve command's actor, before
// approvals.Decide records it. Signed promotion records name their approver (02 "Promotion records").
func WithReplayApprover(ctx context.Context, a auth.Actor) context.Context {
	return context.WithValue(ctx, approverKey{}, a)
}

// ReplayApprover returns the approver set by WithReplayApprover.
func ReplayApprover(ctx context.Context) (auth.Actor, bool) {
	a, ok := ctx.Value(approverKey{}).(auth.Actor)
	return a, ok
}

// HeaderToolCallID names the agent tool call behind a request; the MCP server sets it and the pipeline records it
// as causedBy.toolCallId.
const HeaderToolCallID = "Cadence-Tool-Call-Id"

type (
	projectKey  struct{}
	estimateKey struct{}
	toolCallKey struct{}
	draftKey    struct{}
	replayKey   struct{}

	continuationKey struct{}
)

// WithProject names the project a command touches (its prj_ id), for the policy engine, the approval and the audit
// log. Handlers of project commands resolve the project before Pipeline.Run and set it here.
func WithProject(ctx context.Context, projectID string) context.Context {
	return context.WithValue(ctx, projectKey{}, projectID)
}

// ProjectFromContext returns the project set by WithProject, or "".
func ProjectFromContext(ctx context.Context) string {
	id, _ := ctx.Value(projectKey{}).(string)
	return id
}

// WithEstimate attaches what a spending command will cost; the policy engine compares it with the budget.
func WithEstimate(ctx context.Context, e policy.Estimate) context.Context {
	return context.WithValue(ctx, estimateKey{}, e)
}

func estimateFrom(ctx context.Context) *policy.Estimate {
	if e, ok := ctx.Value(estimateKey{}).(policy.Estimate); ok {
		return &e
	}
	return nil
}

// WithContinuation names the pipeline run a spending command continues (pipelineRuns.retry, jobs.resume,
// runs.resume): when the policy asks for an approval, the command may inherit the one the run was approved under
// (approvals.Inherit), and a replayed approval of the command becomes the run's approval (approvals.Continue).
func WithContinuation(ctx context.Context, pipelineRunID string) context.Context {
	return context.WithValue(ctx, continuationKey{}, pipelineRunID)
}

// ContinuationFromContext returns the pipeline run set by WithContinuation, or "".
func ContinuationFromContext(ctx context.Context) string {
	id, _ := ctx.Value(continuationKey{}).(string)
	return id
}

// WithToolCall records the agent tool call a request comes from.
func WithToolCall(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, toolCallKey{}, id)
}

// ToolCallFromContext returns the tool call id, or "".
func ToolCallFromContext(ctx context.Context) string {
	id, _ := ctx.Value(toolCallKey{}).(string)
	return id
}

// WithDraft records the draft a command applies (drafts.accept); its events carry it as causedBy.draftId.
func WithDraft(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, draftKey{}, id)
}

// DraftFromContext returns the draft id set by WithDraft, or "".
func DraftFromContext(ctx context.Context) string {
	id, _ := ctx.Value(draftKey{}).(string)
	return id
}

type replay struct {
	tx         pgx.Tx
	approvalID string
}

// WithReplay marks ctx as the replay of approval approvalID inside tx (the approve command's transaction): the
// command it carries runs in a savepoint of tx, skips the policy, records causedBy.approvalId and replaces the
// 202 stored under its Idempotency-Key with its real answer.
func WithReplay(ctx context.Context, tx pgx.Tx, approvalID string) context.Context {
	return context.WithValue(ctx, replayKey{}, &replay{tx: tx, approvalID: approvalID})
}

// ReplayedApproval is the approval whose replay ctx carries, or "" outside a replay (a job the replay enqueues
// names it in its audit entry).
func ReplayedApproval(ctx context.Context) string {
	if r := replayFrom(ctx); r != nil {
		return r.approvalID
	}
	return ""
}

func replayFrom(ctx context.Context) *replay {
	r, _ := ctx.Value(replayKey{}).(*replay)
	return r
}
