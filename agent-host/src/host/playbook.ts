// Playbook sessions (docs/spec/05-agents.md "Playbooks and schedules as sessions", R16): the control plane owns the
// plan and ticks it from the session's commands; the host only tells the agent the plan and the rules once, before
// the first prompt of an agent session (a restored ACP session already has it). Driver-neutral: both agents read the
// same text.

import type { AgentSession } from "./api.ts";

const PLAYBOOK = "playbook";

// playbookPreamble is the block the agent reads before the playbook's prompt, or undefined for other sessions.
export function playbookPreamble(session: AgentSession): string | undefined {
  const pb = session.playbook;
  if (session.kind !== PLAYBOOK || !pb) return undefined;
  const lines = pb.plan.map((it, i) => {
    const marks: string[] = [it.command];
    if (it.spending) marks.push("spends GPU time: dry run first");
    if (it.until === "terminal") marks.push("ticks when the job has ended");
    const state = it.state === "pending" ? "" : ` — ${it.state}${it.note ? ` (${it.note})` : ""}`;
    return `${i + 1}. ${it.title} [${marks.join("; ")}]${state}`;
  });
  const e = pb.estimate;
  return [
    `[Cadence] This is a playbook session: "${pb.title}". Estimate ${e.gpuHours.value.toFixed(2)} GPU-hours ` +
      `(${e.gpuHours.low.toFixed(2)}–${e.gpuHours.high.toFixed(2)}, basis ${e.basis}).`,
    "The plan (Cadence ticks each step when your Cadence tool call for it succeeds; you cannot tick steps yourself):",
    ...lines,
    "Rules:",
    "- Work the steps in order with Cadence MCP tools; skipped steps belong to a later phase — do not attempt them.",
    "- Before every command that spends GPU time call it with dryRun=true and report the estimate in one line; the real call without a dry run is refused (playbook-dry-run-required).",
    "- An answer with an approvalId means a person decides: say what you asked for and wait, do not retry.",
    "- Wait on jobs with jobs.wait (call it again while the job is queued or running).",
    "- If a step fails or a tool answers playbook-stopped, stop: write a short summary with the next step and end your turn.",
    "- When the plan is done, write a short summary (entities as @kind:id) and the next step, then end your turn.",
  ].join("\n");
}

// isWaitTool reports whether a tool is a Cadence wait (jobs.wait): calling it again with the same arguments is how
// an agent follows a long job, so the runaway rule does not count it.
export function isWaitTool(tool: string): boolean {
  return /(^|[._])wait$/.test(tool);
}
