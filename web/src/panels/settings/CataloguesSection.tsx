import { useQuery } from "@tanstack/react-query";
import { baseModelsListOptions, templatesListOptions } from "@/api/gen/@tanstack/react-query.gen";
import { StatusChip } from "@/shell/entity/primitives";
import { SectionHeading, Table, Td, when } from "./ui";

// Catalogues (docs/spec/11-ui-panels.md "Settings"): read-only views of what the registry offers the wizard and
// the agent profile — base models, instruction templates and permission presets. New versions arrive by
// registration (bundled at start now; the registry's own commands in phase 4). Agent models are not a registry kind
// yet: the drivers' model lists arrive with the agent profile.

export function CataloguesSection() {
  const models = useQuery(baseModelsListOptions());
  const instructions = useQuery(templatesListOptions({ query: { templateKind: "instructions" } }));
  const presets = useQuery(templatesListOptions({ query: { templateKind: "preset" } }));
  return (
    <section aria-labelledby="settings-catalogues" className="flex flex-col gap-4">
      <SectionHeading id="settings-catalogues" title="Catalogues" hint="Read-only: what projects can adopt. Versions are immutable; a change is a new version." />
      <div className="flex flex-col gap-1">
        <h4 className="text-xs font-medium">Base models</h4>
        <Table label="Base models" head={["Collection", "Version", "State", "Hugging Face", "Family", "Size", "Licence", "Used by"]}>
          {(models.data?.items ?? []).map((m) => (
            <tr key={m.id}>
              <Td className="font-mono">{m.name}</Td>
              <Td className="font-mono tabular-nums">{m.version}</Td>
              <Td>
                <StatusChip state={m.state} />
              </Td>
              <Td className="font-mono" title={`revision ${m.baseModel.revision}`}>
                {m.baseModel.hfRepo}@{m.baseModel.revision.slice(0, 7)}
              </Td>
              <Td className="font-mono">{m.baseModel.familyId}</Td>
              <Td>{m.baseModel.parameters ?? "—"}</Td>
              <Td>{m.baseModel.licence}</Td>
              <Td>{m.usedBy.length ? m.usedBy.map((u) => u.projectSlug).join(", ") : "—"}</Td>
            </tr>
          ))}
        </Table>
      </div>
      <TemplateTable title="Instruction templates" rows={instructions.data?.items ?? []} />
      <TemplateTable title="Permission presets" rows={presets.data?.items ?? []} />
      <p className="text-xs text-muted-foreground">Agent models: chosen per project in Agent settings from each driver's own list (Claude Code, opencode).</p>
    </section>
  );
}

type TemplateRow = { id: string; name: string; version: string; state: string; template: { path: string; files: unknown[] }; usedBy: { projectSlug: string }[]; createdAt: string };

function TemplateTable({ title, rows }: { title: string; rows: TemplateRow[] }) {
  return (
    <div className="flex flex-col gap-1">
      <h4 className="text-xs font-medium">{title}</h4>
      {rows.length === 0 ? (
        <p className="text-xs text-muted-foreground">None registered.</p>
      ) : (
        <Table label={title} head={["Collection", "Version", "State", "Path", "Files", "Registered", "Used by"]}>
          {rows.map((t) => (
            <tr key={t.id}>
              <Td className="font-mono">{t.name}</Td>
              <Td className="font-mono tabular-nums">{t.version}</Td>
              <Td>
                <StatusChip state={t.state} />
              </Td>
              <Td className="font-mono">{t.template.path}</Td>
              <Td className="tabular-nums">{t.template.files.length}</Td>
              <Td className="tabular-nums">{when(t.createdAt)}</Td>
              <Td>{t.usedBy.length ? t.usedBy.map((u) => u.projectSlug).join(", ") : "—"}</Td>
            </tr>
          ))}
        </Table>
      )}
    </div>
  );
}
