import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { gatesGetOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { Gates, ProblemFieldError } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { errorMessage, GATE_REQUEST, problemOf, runCommand, useEditRequest, useTopic } from "@/shell/panel";
import { departureLine, gateETag, gateRows, gateSource } from "./gate";

// The Project home's Gates section (gates.get, gates.edit): the effective gate with its departures from
// defaults.yaml, and gates.yaml edited like a Recipe file — Check (a dry run that parses the file, checks its golden
// sets are adopted and shows the gate it would make), then Commit to main with If-Match on the commit that last
// changed the file ("defaults" while there is none). An agent's edit waits for an approval; a person's commits.

export function GateSection({ slug, ready }: { slug: string; ready: boolean }) {
  const qc = useQueryClient();
  const opts = gatesGetOptions({ path: { p: slug } });
  const q = useQuery({ ...opts, enabled: ready, retry: false });
  const [editing, setEditing] = useState(false);
  useEditRequest(`${GATE_REQUEST}project:${slug}`, () => setEditing(true));
  useTopic(ready ? ["recipe.*"] : null, (batch) => {
    if (batch.some((e) => e.topic === "recipe.gates.yaml")) void qc.invalidateQueries({ queryKey: opts.queryKey });
  });
  const g = q.data;
  return (
    <section aria-labelledby="project-gates" data-slot="project-gate">
      <div className="mb-2 flex items-center gap-2">
        <h3 id="project-gates" className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
          Gate
        </h3>
        <Button size="xs" variant="outline" className="ml-auto" disabled={!g || editing} onClick={() => setEditing(true)} data-command="gates.edit">
          Edit gates.yaml
        </Button>
      </div>
      {!ready ? <p className="text-xs text-muted-foreground">Available once the repository is bootstrapped.</p> : null}
      {q.isLoading ? <p className="text-xs text-muted-foreground">Loading…</p> : null}
      {q.error ? (
        <p role="alert" className="text-xs text-destructive">
          {errorMessage(q.error)}
        </p>
      ) : null}
      {g ? <GateSummary g={g} /> : null}
      {g && editing ? (
        <GateEditor
          slug={slug}
          gates={g}
          onDone={(next) => {
            if (next) qc.setQueryData(opts.queryKey, next);
            else void qc.invalidateQueries({ queryKey: opts.queryKey });
            setEditing(false);
          }}
        />
      ) : null}
    </section>
  );
}

function GateSummary({ g }: { g: Gates }) {
  return (
    <div className="flex flex-col gap-1.5 text-xs" data-gate-exists={g.exists}>
      <p className="text-muted-foreground">{gateSource(g)}</p>
      <dl className="grid grid-cols-[8rem_1fr] gap-x-3 gap-y-1">
        {gateRows(g.config).map(([k, v]) => (
          <div key={k} className="contents">
            <dt className="text-muted-foreground">{k}</dt>
            <dd className="min-w-0 break-words">{v}</dd>
          </div>
        ))}
      </dl>
      {g.departures.length ? (
        <ul className="flex flex-col gap-0.5" aria-label="Departures from defaults" data-slot="gate-departures">
          {g.departures.map((d) => (
            <li key={d.param} className="rounded bg-diff-added px-1.5 text-diff-added-foreground">
              {departureLine(d)}
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-muted-foreground">Every value is the default.</p>
      )}
    </div>
  );
}

function GateEditor({ slug, gates, onDone }: { slug: string; gates: Gates; onDone: (next?: Gates) => void }) {
  const [text, setText] = useState(gates.content);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [fields, setFields] = useState<ProblemFieldError[]>([]);
  const [conflict, setConflict] = useState(false);
  const [checked, setChecked] = useState<Gates | null>(null);
  const [approval, setApproval] = useState<string | null>(null);
  const dirty = text !== gates.content || !gates.exists;
  const save = async (dryRun: boolean) => {
    setBusy(true);
    setError(null);
    setFields([]);
    setChecked(null);
    try {
      const res = await runCommand("gates.edit", {
        project: slug,
        expect: gateETag(gates),
        dryRun,
        body: { content: text, message: message.trim() || "edit gates.yaml" },
      });
      if (!res) return;
      if ("approvalId" in res) {
        setApproval(res.approvalId);
        return;
      }
      if (dryRun) setChecked(res);
      else onDone(res);
    } catch (err) {
      const p = problemOf(err);
      if (p?.status === 412) setConflict(true);
      else {
        setError(errorMessage(err));
        setFields(p?.errors ?? []);
      }
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="mt-2 flex flex-col gap-2 text-xs" data-slot="gate-editor">
      <textarea
        className="min-h-56 w-full resize-y rounded-md border bg-background p-2 font-mono text-xs leading-5 focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
        value={text}
        spellCheck={false}
        aria-label="gates.yaml content"
        onChange={(e) => {
          setText(e.target.value);
          setChecked(null);
        }}
      />
      <div className="flex flex-wrap items-center gap-2">
        <input
          className="h-7 min-w-40 flex-1 rounded-md border bg-background px-2 text-xs"
          placeholder="edit gates.yaml"
          aria-label="Commit message"
          value={message}
          onChange={(e) => setMessage(e.target.value)}
        />
        <Button size="xs" variant="outline" disabled={busy || !dirty || conflict} onClick={() => void save(true)} data-command="gates.edit">
          Check
        </Button>
        <Button size="xs" disabled={busy || !dirty || conflict || !!approval} onClick={() => void save(false)} data-command="gates.edit">
          Commit to main
        </Button>
        <Button size="xs" variant="ghost" disabled={busy} onClick={() => onDone()}>
          {approval ? "Close" : "Cancel"}
        </Button>
      </div>
      {conflict ? (
        <p role="alert" className="text-status-warning-foreground">
          gates.yaml changed on main while you edited it; nothing was committed. Copy your text if you need it, then{" "}
          <button type="button" className="underline underline-offset-2" onClick={() => onDone()}>
            reload
          </button>
          .
        </p>
      ) : null}
      {approval ? (
        <p role="status" data-slot="gate-approval">
          The change waits for an approval ({approval}); it commits once a person approves it in Approvals.
        </p>
      ) : null}
      {checked ? (
        <div role="status" className="text-muted-foreground" data-slot="gate-checked">
          <p>Checked: it would commit cleanly. The gate would be:</p>
          <GateSummary g={checked} />
        </div>
      ) : null}
      {error ? (
        <div role="alert" className="text-destructive">
          <p>{error}</p>
          {fields.length > 0 ? (
            <ul className="mt-1 list-disc pl-5">
              {fields.map((f, i) => (
                <li key={i}>
                  {f.path ? <code className="mr-1">{f.path}</code> : null}
                  {f.message}
                </li>
              ))}
            </ul>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
