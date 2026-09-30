import { useRef, useState, type KeyboardEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { authGetOptions } from "@/api/gen/@tanstack/react-query.gen";
import { cn } from "@/lib/utils";
import { EmptyState } from "@/shell/entity/primitives";
import type { PanelProps } from "@/shell/panel";
import { AgentsSection } from "./AgentsSection";
import { AuditSection } from "./AuditSection";
import { CataloguesSection } from "./CataloguesSection";
import { ComputeSection } from "./ComputeSection";
import { CredentialsSection } from "./CredentialsSection";
import { PoliciesSection } from "./PoliciesSection";
import { SecretsSection } from "./SecretsSection";
import { SecuritySection } from "./SecuritySection";

// Settings (docs/spec/11-ui-panels.md "Panel catalogue"; admin only): registry-level configuration in sections —
// compute, agents (the agents' model accounts), secrets, credentials, policies, catalogues, security and the audit
// log. Notification rules, the Telegram bot and backup status join in later phases.

export const SECTIONS = [
  { id: "compute", label: "Compute", component: ComputeSection },
  { id: "agents", label: "Agents", component: AgentsSection },
  { id: "secrets", label: "Secrets", component: SecretsSection },
  { id: "credentials", label: "Credentials", component: CredentialsSection },
  { id: "policies", label: "Policies", component: PoliciesSection },
  { id: "catalogues", label: "Catalogues", component: CataloguesSection },
  { id: "security", label: "Security", component: SecuritySection },
  { id: "audit", label: "Audit log", component: AuditSection },
] as const;
type SectionId = (typeof SECTIONS)[number]["id"];

export function SettingsEmpty() {
  return <EmptyState step="prepare" title="Settings are the admin's" hint="Compute, secrets, credentials, policies and the audit log are changed by the admin account only." />;
}

export function SettingsPanel({ instanceId }: PanelProps) {
  const { data: auth } = useQuery({ ...authGetOptions(), staleTime: Infinity });
  const [section, setSection] = useState<SectionId>("compute");
  const tabs = useRef<(HTMLButtonElement | null)[]>([]);
  if (auth && auth.actor?.kind !== "user") return <SettingsEmpty />;
  const onKey = (e: KeyboardEvent, i: number) => {
    const d = e.key === "ArrowDown" ? 1 : e.key === "ArrowUp" ? -1 : 0;
    const to = e.key === "Home" ? 0 : e.key === "End" ? SECTIONS.length - 1 : d ? (i + d + SECTIONS.length) % SECTIONS.length : -1;
    if (to < 0) return;
    e.preventDefault();
    setSection(SECTIONS[to]!.id);
    tabs.current[to]?.focus();
  };
  const Current = SECTIONS.find((s) => s.id === section)!.component;
  return (
    <div className="flex h-full min-h-0">
      <div role="tablist" aria-orientation="vertical" aria-label="Settings sections" className="flex w-36 shrink-0 flex-col gap-0.5 border-r bg-tool p-1.5">
        {SECTIONS.map((s, i) => (
          <button
            key={s.id}
            ref={(el) => {
              tabs.current[i] = el;
            }}
            id={`${instanceId}-tab-${s.id}`}
            role="tab"
            type="button"
            aria-selected={section === s.id}
            aria-controls={`${instanceId}-section`}
            tabIndex={section === s.id ? 0 : -1}
            onKeyDown={(e) => onKey(e, i)}
            onClick={() => setSection(s.id)}
            className={cn("h-7 rounded px-2 text-left text-xs", section === s.id ? "bg-selected font-medium text-foreground" : "text-muted-foreground hover:bg-hover hover:text-foreground")}
          >
            {s.label}
          </button>
        ))}
      </div>
      <div id={`${instanceId}-section`} role="tabpanel" aria-labelledby={`${instanceId}-tab-${section}`} className="@container min-w-0 flex-1 overflow-auto p-3">
        <Current />
      </div>
    </div>
  );
}
