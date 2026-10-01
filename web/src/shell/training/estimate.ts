import { useRef, useState } from "react";

// A dry-run estimate tied to the exact request it answered (runs.new, runs.stage). A form shows the newest answer,
// but Start is offered only while that answer is for exactly the values the form holds now and no newer dry run is
// pending: after any edit the estimate is stale until it is asked for again.

export type KeyedEstimate<E> = {
  /** The newest answer, whatever values it was for (shown as stale when `fresh` is false). */
  estimate: E | undefined;
  /** The answer is for exactly `key` and no newer dry run is pending: Start may use it. */
  fresh: boolean;
  /** An answer is shown but is not for the current values, or a newer dry run is pending. */
  stale: boolean;
  /** A dry run is pending. */
  pending: boolean;
  /** Call when sending a dry run for the current key; pass the ticket to `settle`. */
  begin(): number;
  /** The answer to a ticket (no value: it failed or was not an estimate). Answers to older tickets are dropped. */
  settle(ticket: number, value?: E): void;
};

/** `key` serialises the request body the dry run and Start send (`JSON.stringify` of the same object). */
export function useKeyedEstimate<E>(key: string): KeyedEstimate<E> {
  const [answer, setAnswer] = useState<{ key: string; value: E }>();
  const [pending, setPending] = useState(false);
  const asked = useRef<{ ticket: number; key: string }>({ ticket: 0, key: "" });
  const fresh = !pending && answer !== undefined && answer.key === key;
  return {
    estimate: answer?.value,
    fresh,
    stale: answer !== undefined && !fresh,
    pending,
    begin() {
      const ticket = asked.current.ticket + 1;
      asked.current = { ticket, key };
      setPending(true);
      return ticket;
    },
    settle(ticket, value) {
      if (ticket !== asked.current.ticket) return;
      setPending(false);
      if (value !== undefined) setAnswer({ key: asked.current.key, value });
    },
  };
}
