// The workspace autosave schedule, kept free of Dockview and the API so it can be tested with fake timers.
//
// A layout is saved `delayMs` after the last change (moving a window fires many layout events), only when its
// serialized form differs from what was last stored, one save at a time (two saves in flight on one rev would
// conflict with each other), and at once on `flush()` (page hidden, workspace switched, autosave stopped).

export type AutosaveDeps = {
  delayMs: number;
  /** The serialized workspace now, or undefined while it must not be saved (restoring, conflict, another workspace). */
  snapshot(): string | undefined;
  /** The serialized workspace the server is known to hold (last restore or save). */
  saved(): string | undefined;
  /** Stores a snapshot; `urgent` when the page may be going away (the request should outlive it). */
  save(snapshot: string, urgent: boolean): Promise<void>;
};

export type Autosaver = {
  /** A change happened: (re)start the quiet-period timer. */
  schedule(): void;
  /** Save now if anything is pending or changed; resolves when the save (if any) is done. */
  flush(urgent?: boolean): Promise<void>;
  /** Stop the timer; does not save. */
  dispose(): void;
};

export function createAutosaver(deps: AutosaveDeps): Autosaver {
  let timer: ReturnType<typeof setTimeout> | undefined;
  let inflight: Promise<void> | undefined;
  let again = false;
  let disposed = false;

  const run = async (urgent: boolean): Promise<void> => {
    clearTimeout(timer);
    timer = undefined;
    if (inflight) {
      // Save again once the current one settles (it moves the rev); the later snapshot wins.
      again = true;
      return inflight;
    }
    const snap = deps.snapshot();
    if (snap === undefined || snap === deps.saved()) return;
    inflight = deps.save(snap, urgent).finally(() => {
      inflight = undefined;
    });
    await inflight;
    if (again) {
      again = false;
      await run(urgent);
    }
  };

  return {
    schedule() {
      if (disposed) return;
      clearTimeout(timer);
      timer = setTimeout(() => void run(false), deps.delayMs);
    },
    flush(urgent = false) {
      return run(urgent);
    },
    dispose() {
      disposed = true;
      clearTimeout(timer);
      timer = undefined;
    },
  };
}
