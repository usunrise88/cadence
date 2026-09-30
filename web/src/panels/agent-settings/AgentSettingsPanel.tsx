import { useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { agentModelsListOptions, agentProfileGetOptions, projectsGetOptions, templatesListOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { AgentDriver, AgentProfile, AgentProfileEdit, AutoMerge, DraftMode, DraftPolicy } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";
import { EmptyState, PanelToolbar } from "@/shell/entity/primitives";
import { openDocument, runCommand, useProject, useTopic } from "@/shell/panel";

// Agent settings (docs/spec/11-ui-panels.md, Agent settings): the project's agent profile — driver and model,
// permission preset, auto-merge policy, draft policy per entity kind, instructions template — with a preview of the
// config files rendered from it and the AGENTS.md editor. Save commits the rendered files to the project repository
// (agentProfile.edit); running sessions pick them up at their next turn.

const DRIVERS: { id: AgentDriver; label: string }[] = [
  { id: "claude-code", label: "Claude Code" },
  { id: "opencode", label: "opencode" },
];
const DRAFT_KINDS: { key: keyof DraftPolicy; label: string }[] = [
  { key: "mix", label: "Mixes" },
  { key: "gate", label: "Gates" },
  { key: "note", label: "Notes" },
  { key: "language_pack", label: "Language packs" },
];
const PREVIEWS = [".claude/settings.json", "opencode.json", "AGENTS.md", "CLAUDE.md"] as const;
type Preview = (typeof PREVIEWS)[number];

type Form = Pick<AgentProfile, "driver" | "model" | "permissionPreset" | "instructionsTemplate" | "autoMerge" | "draftPolicy">;

function names(collections: string[] | undefined, kind: "preset" | "instructions"): string[] {
  const prefix = `template/${kind}-`;
  return [...new Set((collections ?? []).filter((n) => n.startsWith(prefix)).map((n) => n.slice(prefix.length)))].sort();
}

export function AgentSettingsEmpty() {
  return <EmptyState step="prepare" title="No project open" hint="Agent settings belong to a project; open one from the menu bar." />;
}

export function AgentSettingsPanel() {
  const project = useProject();
  if (!project) return <AgentSettingsEmpty />;
  return <Settings key={project} project={project} />;
}

function Row({ label, children, hint }: { label: string; children: ReactNode; hint?: string }) {
  return (
    <label className="grid grid-cols-[9rem_1fr] items-center gap-2 text-xs">
      <span className="text-muted-foreground" title={hint}>
        {label}
      </span>
      {children}
    </label>
  );
}

function Settings({ project }: { project: string }) {
  const qc = useQueryClient();
  const proj = useQuery(projectsGetOptions({ path: { p: project } }));
  const ready = proj.data?.state === "active" && !proj.data.archivedAt;
  const opts = agentProfileGetOptions({ path: { p: project } });
  const profile = useQuery({ ...opts, enabled: ready });
  const catalogue = useQuery(agentModelsListOptions());
  const presets = useQuery(templatesListOptions({ query: { templateKind: "preset" } }));
  const instructions = useQuery(templatesListOptions({ query: { templateKind: "instructions" } }));
  const [edits, setEdits] = useState<Partial<Form>>({});
  const [agentsMd, setAgentsMd] = useState<string | null>(null);
  const [baseRev, setBaseRev] = useState<number | undefined>(undefined);
  const [preview, setPreview] = useState<Preview>(".claude/settings.json");
  const [status, setStatus] = useState<{ kind: "error" | "conflict" | "approval"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  useTopic(proj.data ? [`entity.project.${proj.data.id}`] : null, () => void qc.invalidateQueries({ queryKey: opts.queryKey }));

  if (proj.data && !ready) {
    return <EmptyState step="prepare" title="No agent profile yet" hint={proj.data.archivedAt ? "The project is archived." : `The project is ${proj.data.state}; the profile exists once the bootstrap is done.`} />;
  }
  const p = profile.data;
  if (!p) return <div className="p-3 text-xs text-muted-foreground">{profile.error ? "The agent profile could not be loaded." : "Loading…"}</div>;

  const v: Form = { ...p, ...edits, draftPolicy: { ...p.draftPolicy, ...edits.draftPolicy } };
  const dirty = Object.keys(edits).length > 0 || agentsMd !== null;
  const rev = baseRev ?? p.rev;
  const stale = dirty && baseRev !== undefined && baseRev !== p.rev;
  const edit = (patch: Partial<Form>) => {
    if (baseRev === undefined) setBaseRev(p.rev);
    setEdits((e) => ({ ...e, ...patch }));
  };
  const reset = () => {
    setEdits({});
    setAgentsMd(null);
    setBaseRev(undefined);
    setStatus(null);
  };
  const models = catalogue.data?.items.find((c) => c.driver === v.driver);
  const file = (path: string) => p.files.find((f) => f.path === path)?.content ?? "";

  const save = async (body: AgentProfileEdit, atRev: number) => {
    setBusy(true);
    setStatus(null);
    try {
      const res = await runCommand<{ profile?: AgentProfile; approvalId?: string } | undefined>("agentProfile.edit", { project, rev: atRev, body });
      if (res?.approvalId) setStatus({ kind: "approval", text: `Waiting for approval ${res.approvalId}` });
      if (res?.profile) qc.setQueryData(opts.queryKey, res.profile);
      setEdits({});
      setAgentsMd(null);
      setBaseRev(undefined);
      await qc.invalidateQueries({ queryKey: opts.queryKey });
    } catch (err) {
      const statusCode = (err as { status?: number }).status;
      if (statusCode === 412) setStatus({ kind: "conflict", text: "The profile changed meanwhile. Reload to see it, then apply your change again." });
      else setStatus({ kind: "error", text: err instanceof Error ? err.message : String(err) });
    } finally {
      setBusy(false);
    }
  };
  const submit = () => {
    const body: AgentProfileEdit = {};
    for (const k of ["driver", "model", "permissionPreset", "instructionsTemplate", "autoMerge"] as const) {
      if (edits[k] !== undefined && edits[k] !== p[k]) (body as Record<string, unknown>)[k] = edits[k];
    }
    if (edits.draftPolicy) body.draftPolicy = edits.draftPolicy;
    if (agentsMd !== null && agentsMd !== file("AGENTS.md")) body.agentsMd = agentsMd;
    if (Object.keys(body).length === 0) return reset();
    void save(body, rev);
  };
  const resetToTemplate = () => {
    const tpl = v.instructionsTemplate === "custom" ? "default" : v.instructionsTemplate;
    void save({ instructionsTemplate: tpl }, p.rev);
  };

  const presetNames = names(presets.data?.items.map((t) => t.name), "preset");
  const templateNames = names(instructions.data?.items.map((t) => t.name), "instructions");
  const content = preview === "AGENTS.md" ? (agentsMd ?? file("AGENTS.md")) : file(preview);

  return (
    <div className="@container flex h-full min-h-0 flex-col" data-testid="agent-settings">
      <PanelToolbar>
        <span className="truncate text-xs font-medium">{project}</span>
        <span className="shrink-0 text-[11px] whitespace-nowrap text-muted-foreground">rev {p.rev}</span>
        {p.commit ? <code className="hidden text-[11px] text-muted-foreground @xs:inline">{p.commit.slice(0, 7)}</code> : null}
        <div className="ml-auto flex gap-1">
          <Button size="xs" variant="ghost" disabled={busy || !dirty} onClick={reset}>
            Discard
          </Button>
          <Button size="xs" disabled={busy || !dirty || stale} onClick={submit}>
            Save
          </Button>
        </div>
      </PanelToolbar>
      <div className="min-h-0 flex-1 overflow-auto">
        {status || stale ? (
          <p role={status?.kind === "approval" ? "status" : "alert"} className={cn("mx-3 mt-2 rounded-md border px-2 py-1.5 text-xs", status?.kind === "approval" ? "" : "text-destructive")}>
            {stale ? "The profile changed while you were editing. " : null}
            {status?.text}
            {status?.kind === "conflict" || stale ? (
              <Button size="xs" variant="outline" className="ml-2" onClick={reset}>
                Reload
              </Button>
            ) : null}
          </p>
        ) : null}
        <section aria-label="Profile" className="flex flex-col gap-2 p-3">
          <Row label="Driver">
            <NativeSelect
              value={v.driver}
              onChange={(e) => {
                const driver = e.target.value as AgentDriver;
                const def = catalogue.data?.items.find((c) => c.driver === driver)?.default;
                edit({ driver, ...(def ? { model: def } : {}) });
              }}
              name="driver"
            >
              {DRIVERS.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.label}
                </option>
              ))}
            </NativeSelect>
          </Row>
          <Row label="Model" hint={models?.description}>
            {models?.freeForm ? (
              <>
                <Input value={v.model} onChange={(e) => edit({ model: e.target.value })} list="agent-models" name="model" />
                <datalist id="agent-models">
                  {models.models.map((m) => (
                    <option key={m.id} value={m.id}>
                      {m.name}
                    </option>
                  ))}
                </datalist>
              </>
            ) : (
              <NativeSelect value={v.model} onChange={(e) => edit({ model: e.target.value })} name="model">
                {(models?.models.some((m) => m.id === v.model) ? models.models : [{ id: v.model, name: v.model }, ...(models?.models ?? [])]).map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.name}
                  </option>
                ))}
              </NativeSelect>
            )}
          </Row>
          <Row label="Permission preset">
            <NativeSelect value={v.permissionPreset} onChange={(e) => edit({ permissionPreset: e.target.value })} name="preset">
              {(presetNames.includes(v.permissionPreset) ? presetNames : [v.permissionPreset, ...presetNames]).map((n) => (
                <option key={n}>{n}</option>
              ))}
            </NativeSelect>
          </Row>
          <Row label="Session branches" hint="What happens to a session branch when the session ends">
            <NativeSelect value={v.autoMerge} onChange={(e) => edit({ autoMerge: e.target.value as AutoMerge })} name="autoMerge">
              <option value="when-clean">Merge into main when it applies cleanly</option>
              <option value="never">Always wait as Session changes</option>
            </NativeSelect>
          </Row>
          <Row label="Instructions template">
            <NativeSelect value={v.instructionsTemplate} onChange={(e) => edit({ instructionsTemplate: e.target.value })} name="instructions">
              {[...new Set([...templateNames, v.instructionsTemplate])].map((n) => (
                <option key={n} value={n} disabled={n === "custom"}>
                  {n === "custom" ? "custom (edited AGENTS.md)" : n}
                </option>
              ))}
            </NativeSelect>
          </Row>
          <fieldset className="mt-1 grid grid-cols-[9rem_1fr] gap-2 text-xs">
            <legend className="mb-1 text-[11px] font-medium tracking-wide text-muted-foreground uppercase">Agent changes land as</legend>
            {DRAFT_KINDS.map((k) => (
              <label key={k.key} className="contents">
                <span className="self-center text-muted-foreground">{k.label}</span>
                <NativeSelect value={v.draftPolicy[k.key]} onChange={(e) => edit({ draftPolicy: { ...v.draftPolicy, [k.key]: e.target.value as DraftMode } })} name={`draft-${k.key}`}>
                  <option value="direct">Direct changes</option>
                  <option value="draft">Drafts to accept or revert</option>
                </NativeSelect>
              </label>
            ))}
          </fieldset>
        </section>
        <section aria-label="Rendered files" className="border-t">
          <div role="tablist" aria-label="Rendered files" className="flex gap-3 px-3">
            {PREVIEWS.map((f) => (
              <button
                key={f}
                type="button"
                role="tab"
                aria-selected={preview === f}
                onClick={() => setPreview(f)}
                className={cn(
                  "-mb-px min-h-6 border-b-2 py-1.5 text-xs",
                  preview === f ? "border-accent-line font-medium text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
                )}
              >
                {f}
              </button>
            ))}
            <Button size="xs" variant="ghost" className="my-auto ml-auto" onClick={() => openDocument(`recipe:${preview}`)}>
              Open in Recipe
            </Button>
          </div>
          <div role="tabpanel" className="p-3">
            {preview === "AGENTS.md" ? (
              <div className="flex flex-col gap-2">
                <Textarea
                  aria-label="AGENTS.md"
                  value={content}
                  onChange={(e) => {
                    if (baseRev === undefined) setBaseRev(p.rev);
                    setAgentsMd(e.target.value);
                  }}
                  rows={16}
                  className="font-mono text-xs leading-relaxed"
                  spellCheck={false}
                />
                <div className="flex items-center gap-2 text-[11px] text-muted-foreground">
                  <span>
                    {v.instructionsTemplate === "custom" || agentsMd !== null
                      ? "Custom instructions: the template no longer re-renders AGENTS.md."
                      : `Rendered from the ${v.instructionsTemplate} template with the project facts.`}
                  </span>
                  <Button size="xs" variant="outline" className="ml-auto" disabled={busy || (v.instructionsTemplate !== "custom" && agentsMd === null)} onClick={resetToTemplate}>
                    Reset to template
                  </Button>
                </div>
              </div>
            ) : (
              <pre className="max-h-[28rem] overflow-auto rounded-md border bg-muted/40 p-2 font-mono text-xs leading-relaxed" data-testid="rendered-file">
                {content || "—"}
              </pre>
            )}
            {preview !== "AGENTS.md" ? (
              <p className="mt-1.5 text-[11px] text-muted-foreground">
                Rendered from the profile and the permission preset; permissions only — the Cadence MCP server and its token reach the agent through the session, never through
                a file.
              </p>
            ) : null}
          </div>
        </section>
      </div>
    </div>
  );
}
