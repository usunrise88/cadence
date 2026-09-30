// Structured JSON log lines on stderr (one per event), like the control plane's slog output.

export type Level = "debug" | "info" | "warn" | "error";

export interface Logger {
  log(level: Level, msg: string, attrs?: Record<string, unknown>): void;
}

export const jsonLogger = (minLevel: Level = "info"): Logger => {
  const order: Record<Level, number> = { debug: 0, info: 1, warn: 2, error: 3 };
  return {
    log(level, msg, attrs) {
      if (order[level] < order[minLevel]) return;
      process.stderr.write(`${JSON.stringify({ time: new Date().toISOString(), level, msg, ...attrs })}\n`);
    },
  };
};

export const silentLogger: Logger = { log: () => undefined };

export function errText(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
