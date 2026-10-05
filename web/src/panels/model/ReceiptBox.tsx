import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { promotionsGetOptions, promotionsListOptions } from "@/api/gen/@tanstack/react-query.gen";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { errorMessage, problemOf, runCommand } from "@/shell/panel";
import { Message, shortHash, Verdict } from "./ui";

// A pending delivery (02 "Promotion records"): the record's bundle (its state, deliver.sh, the smoke check's size and,
// for people, a short-lived download link) and the receipt box. The person who ran deliver.sh on the production host
// pastes the CADENCE-RECEIPT line it printed; promotions.verify checks it and appends a signed confirmation, or
// answers promotion-receipt-mismatch and changes nothing.

export function ReceiptBox({ recordId, onConfirmed }: { recordId: string; onConfirmed: () => void }) {
  const qc = useQueryClient();
  const rec = useQuery(promotionsGetOptions({ path: { id: recordId } }));
  const [receipt, setReceipt] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ error: boolean; text: string } | null>(null);
  const r = rec.data;
  const dl = r?.delivery;
  const confirm = async () => {
    if (!r) return;
    setBusy(true);
    setMessage(null);
    try {
      const out = await runCommand("promotions.verify", { record: r.id, rev: r.rev, receipt });
      setMessage({ error: false, text: `Confirmed: ${out.closedBy ?? "the confirmation record"} moves the deployment to its stage.` });
      setReceipt("");
      void qc.invalidateQueries({ queryKey: promotionsGetOptions({ path: { id: recordId } }).queryKey });
      onConfirmed();
    } catch (err) {
      const p = problemOf(err);
      setMessage({ error: true, text: p?.type?.endsWith("/promotion-receipt-mismatch") ? `The receipt does not match: ${p.detail ?? p.title}` : errorMessage(err) });
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="flex flex-col gap-1.5 rounded-md border border-dashed p-2" data-slot="receipt-box" data-record={recordId}>
      <p>
        Waiting for the receipt of <code className="text-[11px]">{recordId}</code>
        {r ? (
          <>
            {" "}
            ({r.kind}, seq {r.seq}, hash <code className="text-[11px]">{shortHash(r.hash)}</code>)
          </>
        ) : null}
      </p>
      {dl ? (
        <p className="flex flex-wrap items-center gap-2" data-slot="delivery" data-state={dl.state}>
          <span className="text-muted-foreground">Delivery bundle:</span> <Verdict value={dl.state} />
          {dl.smoke ? (
            <span className="text-muted-foreground">
              smoke check {dl.smoke.required} of {dl.smoke.utterances} must match
            </span>
          ) : null}
          {dl.bundleUrl ? (
            <a href={dl.bundleUrl} download className="underline-offset-2 hover:underline">
              Download the bundle
            </a>
          ) : null}
          {dl.error ? <span className="text-destructive">{dl.error}</span> : null}
        </p>
      ) : null}
      {dl?.script ? (
        <details>
          <summary className="cursor-pointer text-muted-foreground">deliver.sh</summary>
          <pre className="max-h-48 overflow-auto rounded bg-muted p-2 text-[11px]">{dl.script}</pre>
        </details>
      ) : null}
      <label className="flex flex-col gap-0.5">
        <span className="text-muted-foreground">Receipt (the CADENCE-RECEIPT line deliver.sh printed)</span>
        <Textarea aria-label="Receipt" className="min-h-12 font-mono text-[11px]" value={receipt} onChange={(e) => setReceipt(e.target.value)} />
      </label>
      <Button size="xs" className="w-fit" disabled={busy || !r || !receipt.trim()} onClick={() => void confirm()} data-command="promotions.verify">
        Confirm delivery
      </Button>
      <Message m={message} />
    </div>
  );
}

/** A delivery target's chain for one slot of the project, oldest first, each record verified again on read. */
export function PromotionChain({ target, project, slot }: { target: string; project: string; slot?: string }) {
  const chain = useQuery(promotionsListOptions({ path: { id: target }, query: { project, ...(slot ? { slot } : {}) } }));
  const c = chain.data;
  if (!c) return null;
  return (
    <div className="flex flex-col gap-0.5" data-slot="promotion-chain" data-intact={c.intact}>
      <p className="text-muted-foreground">
        Promotion chain of {c.targetName}
        {slot ? ` / ${slot}` : ""}: {c.intact ? "intact" : `broken — ${(c.problems ?? []).join("; ")}`}
      </p>
      <ol className="flex flex-col gap-0.5">
        {c.items.map((r) => (
          <li key={r.id} className="flex flex-wrap items-center gap-2" data-record={r.id}>
            <span className="w-8 text-right text-muted-foreground tabular-nums">{r.seq}</span>
            <span className="font-medium">{r.kind}</span>
            {r.state ? <Verdict value={r.state} /> : null}
            <span className={r.verified ? "text-muted-foreground" : "text-destructive"}>{r.verified ? "verified" : "does not verify"}</span>
            <code className="text-[11px] text-muted-foreground">{shortHash(r.hash)}</code>
          </li>
        ))}
      </ol>
    </div>
  );
}
