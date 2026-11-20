// Centralised status → color/label semantics, reused across the whole app.
// Spec §8.3: Intent / Override / Task / Severity each have a consistent color set..

export type Tone = "neutral" | "blue" | "green" | "amber" | "red" | "rose" | "violet" | "slate";

export const toneClasses: Record<Tone, string> = {
  neutral: "bg-muted text-muted-foreground border-transparent",
  blue: "bg-blue-500/15 text-blue-600 dark:text-blue-400 border-blue-500/20",
  green: "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400 border-emerald-500/20",
  amber: "bg-amber-500/15 text-amber-600 dark:text-amber-400 border-amber-500/20",
  red: "bg-red-500/15 text-red-600 dark:text-red-400 border-red-500/20",
  // rose Use as 'Serious'——Solid High Emphasis,Visually clearly higher than 'High Risk' with soft red outline.
  rose: "bg-rose-600 text-white border-rose-600 dark:bg-rose-600 dark:text-white",
  violet: "bg-violet-500/15 text-violet-600 dark:text-violet-400 border-violet-500/20",
  slate: "bg-slate-500/15 text-slate-600 dark:text-slate-400 border-slate-500/20",
};

export const toneDot: Record<Tone, string> = {
  neutral: "bg-muted-foreground",
  blue: "bg-blue-500",
  green: "bg-emerald-500",
  amber: "bg-amber-500",
  red: "bg-red-500",
  rose: "bg-white",
  violet: "bg-violet-500",
  slate: "bg-slate-500",
};

interface StatusMeta {
  label: string;
  tone: Tone;
}

const intent: Record<string, StatusMeta> = {
  open: { label: "To be claimed", tone: "slate" },
  running: { label: "Executing", tone: "blue" },
  paused: { label: "Paused", tone: "amber" },
  done: { label: "Completed", tone: "green" },
  // blocked = Model/API/network fault retries exhausted, this intent was likely not truly probed (not a target interception).
  blocked: { label: "Execution error", tone: "red" },
  // exhausted = Steps/time budget reached, stopped midway, only partial results written back (not all directions probed).
  exhausted: { label: "Budget exhausted", tone: "violet" },
  // stopped = Historical soft delete status (retained),Historical data).
  stopped: { label: "Stopped", tone: "slate" },
  // deleted = The user falsely deleted this intent (retaining nodes and lineage, deletion reason see delete_reason field).
  deleted: { label: "Deleted", tone: "slate" },
};

const task: Record<string, StatusMeta> = {
  created: { label: "Created", tone: "slate" },
  queued: { label: "Queued", tone: "amber" },
  running: { label: "Running", tone: "blue" },
  paused: { label: "Paused", tone: "amber" },
  done: { label: "Completed", tone: "green" },
  failed: { label: "Success", tone: "red" },
  timeout: { label: "Expired", tone: "amber" },
};

const severity: Record<string, StatusMeta> = {
  critical: { label: "Support", tone: "rose" },
  high: { label: "Retest", tone: "red" },
  medium: { label: "Medium Risk", tone: "amber" },
  low: { label: "Low Risk", tone: "slate" },
};

const finding: Record<string, StatusMeta> = {
  pending: { label: "Pending", tone: "amber" },
  in_progress: { label: "Processing", tone: "blue" },
  confirmed: { label: "Confirmed", tone: "red" },
  resolved: { label: "Completed", tone: "green" },
  fixed: { label: "Fixed", tone: "green" },
  false_positive: { label: "Injection", tone: "slate" },
  ignored: { label: "Response", tone: "neutral" },
  duplicate: { label: "Return", tone: "neutral" },
  risk_accepted: { label: "Risk acceptance", tone: "violet" },
};

const engine: Record<string, StatusMeta> = {
  exploring: { label: "Exploring", tone: "blue" },
  paused: { label: "Paused", tone: "amber" },
  stalled: { label: "Stagnant", tone: "red" },
  idle: { label: "Development Without Subdomains", tone: "neutral" },
};

const goal: Record<string, StatusMeta> = {
  open: { label: "In Progress", tone: "blue" },
  met: { label: "Achieved", tone: "green" },
  abandoned: { label: "Abandoned", tone: "slate" },
};

const audit: Record<string, StatusMeta> = {
  allow: { label: "Allow", tone: "green" },
  block: { label: "Write As", tone: "red" },
};

const node: Record<string, StatusMeta> = {
  observed: { label: "Monitor", tone: "slate" },
  confirmed: { label: "Confirm", tone: "green" },
  tombstoned: { label: "Abandoned", tone: "neutral" },
};

// Push delivery status. Sending uses blue instead of amber: It's not 'problematic',
// Instead of 'already claimed, being sent', it should be distinguishable from the pending waiting semantics.
const delivery: Record<string, StatusMeta> = {
  pending: { label: "Pending Send", tone: "amber" },
  sending: { label: "Sending", tone: "blue" },
  sent: { label: "Delivered", tone: "green" },
  failed: { label: "Success", tone: "red" },
  skipped: { label: "Skipped", tone: "neutral" },
};

const maps = {
  intent,
  task,
  severity,
  finding,
  engine,
  goal,
  audit,
  node,
  delivery,
} as const;

export type StatusDomain = keyof typeof maps;

export function statusMeta(domain: StatusDomain, key: string): StatusMeta {
  return maps[domain][key] ?? { label: key, tone: "neutral" };
}
