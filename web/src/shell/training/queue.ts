import type { QueueEntry } from "@/api/gen/types.gen";

// Queue order, shared by Queue & GPU and the status bar's Queue popup.

const STATE_ORDER: Record<QueueEntry["state"], number> = { running: 0, stopping: 1, waiting: 2, paused: 3 };

/** Running and stopping first, then the queue order the scheduler uses: priority (higher first), then FIFO. */
export function sortEntries(items: QueueEntry[]): QueueEntry[] {
  return [...items].sort(
    (a, b) => STATE_ORDER[a.state] - STATE_ORDER[b.state] || b.priority - a.priority || a.enqueuedAt.localeCompare(b.enqueuedAt) || a.jobId.localeCompare(b.jobId),
  );
}
