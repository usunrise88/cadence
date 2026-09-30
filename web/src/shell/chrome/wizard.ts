import type { AgentDriver, AgentModels, Defaults, ProjectNew, RepositoryKind } from "@/api/gen/types.gen";

// The project wizard's pure part (docs/spec/11-ui-panels.md "The recommended path"): Recommended mode asks three
// things (name, language, where the recordings are); everything else starts from defaults.yaml and is sent only
// when the person departed from it, so the server stays the one place defaults are applied.

export type WizardValues = {
  name: string;
  slug: string;
  locale: string;
  domain: string;
  /** A base-model version id (ver_…) or the collection name from defaults.yaml. */
  baseModel: string;
  driver: AgentDriver;
  model: string;
  preset: string;
  instructions: string;
  repoKind: RepositoryKind;
  repoUrl: string;
  repoSecret: string;
  repoOwner: string;
  gpuHoursPerDay: string;
  agentTokensPerDay: string;
};

export function slugify(name: string): string {
  return name
    .toLowerCase()
    .normalize("NFKD")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .replace(/^[^a-z]+/, "")
    .slice(0, 40)
    .replace(/-+$/, "");
}

function str(d: Defaults | undefined, section: "wizard" | "budgets", key: string, fallback: string): string {
  const v = d?.[section]?.[key]?.value;
  return v === undefined || v === null ? fallback : String(v);
}

/** The model a driver starts with: the catalogue's default, else defaults.yaml. */
export function defaultModel(driver: AgentDriver, catalogue: AgentModels[] | undefined, d: Defaults | undefined): string {
  const entry = catalogue?.find((c) => c.driver === driver);
  if (entry) return entry.default;
  return driver === "opencode" ? str(d, "wizard", "opencode_model", "") : str(d, "wizard", "claude_code_model", "");
}

/** The Recommended values: defaults.yaml plus the agent catalogue. Name and slug start empty. */
export function recommended(d: Defaults | undefined, catalogue: AgentModels[] | undefined): WizardValues {
  const driver = (str(d, "wizard", "driver", "claude-code") as AgentDriver) ?? "claude-code";
  return {
    name: "",
    slug: "",
    locale: str(d, "wizard", "locale", "he-IL"),
    domain: str(d, "wizard", "domain", "telephony"),
    baseModel: str(d, "wizard", "base_model", ""),
    driver,
    model: defaultModel(driver, catalogue, d),
    preset: str(d, "wizard", "permission_preset", "guardrails-default"),
    instructions: str(d, "wizard", "instructions_template", "default"),
    repoKind: str(d, "wizard", "repository", "internal") as RepositoryKind,
    repoUrl: "",
    repoSecret: "",
    repoOwner: "",
    gpuHoursPerDay: str(d, "budgets", "gpu_hours_per_project_per_day", "8"),
    agentTokensPerDay: str(d, "budgets", "agent_tokens_per_project_per_day", ""),
  };
}

/** Which Customise fields differ from Recommended (shown as departure chips). */
export function departures(v: WizardValues, rec: WizardValues): string[] {
  const keys: (keyof WizardValues)[] = [
    "domain",
    "baseModel",
    "driver",
    "model",
    "preset",
    "instructions",
    "repoKind",
    "gpuHoursPerDay",
    "agentTokensPerDay",
  ];
  return keys.filter((k) => v[k] !== rec[k]);
}

/**
 * The projects.new body: name, slug and locale always; every other field only when it departs from Recommended.
 * `baseModelIsDefault` tells whether the chosen base-model version is the one defaults.yaml names (its newest frozen
 * version), in which case it is left to the server.
 */
export function projectNewBody(v: WizardValues, rec: WizardValues, baseModelIsDefault = v.baseModel === rec.baseModel): ProjectNew {
  const body: ProjectNew = { name: v.name.trim(), locales: [v.locale.trim()] };
  const slug = v.slug.trim() || slugify(v.name);
  if (slug) body.slug = slug;
  if (v.domain !== rec.domain) body.domain = v.domain.trim();
  if (!baseModelIsDefault && v.baseModel) body.baseModel = v.baseModel;
  if (v.driver !== rec.driver || v.model !== rec.model || v.preset !== rec.preset) {
    body.agent = { driver: v.driver, model: v.model.trim(), permissionPreset: v.preset };
  }
  if (v.instructions !== rec.instructions) body.instructionsTemplate = v.instructions;
  if (v.repoKind !== rec.repoKind || v.repoUrl || v.repoSecret || v.repoOwner) {
    body.repository = { kind: v.repoKind };
    if (v.repoKind === "url" && v.repoUrl) body.repository.url = v.repoUrl.trim();
    if (v.repoKind !== "internal" && v.repoSecret) body.repository.secret = v.repoSecret.trim();
    if (v.repoKind === "github" && v.repoOwner) body.repository.owner = v.repoOwner.trim();
  }
  const budgets: NonNullable<ProjectNew["budgets"]> = {};
  if (v.gpuHoursPerDay !== rec.gpuHoursPerDay && v.gpuHoursPerDay !== "") budgets.gpuHoursPerDay = Number(v.gpuHoursPerDay);
  if (v.agentTokensPerDay !== rec.agentTokensPerDay && v.agentTokensPerDay !== "") budgets.agentTokensPerDay = Number(v.agentTokensPerDay);
  if (Object.keys(budgets).length > 0) body.budgets = budgets;
  return body;
}

/** Names from template collections: template/preset-guardrails-default → guardrails-default. */
export function templateNames(collections: string[], kind: "preset" | "instructions"): string[] {
  const prefix = `template/${kind}-`;
  return [...new Set(collections.filter((n) => n.startsWith(prefix)).map((n) => n.slice(prefix.length)))].sort();
}

export function formatTokens(n: string | number): string {
  const v = Number(n);
  if (!Number.isFinite(v) || n === "") return "—";
  if (v >= 1_000_000) return `${+(v / 1_000_000).toFixed(1)}M`;
  if (v >= 1_000) return `${+(v / 1_000).toFixed(1)}k`;
  return String(v);
}
