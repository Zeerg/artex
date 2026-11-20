import type { NewAssetType } from "@/lib/types";

const ASSET_TYPE_LABELS: Record<NewAssetType, string> = {
  app: "Application",
  endpoint: "Interface",
  ip: "IP",
  root_domain: "Root domain",
  service: "Use",
  subdomain: "Subdomain",
};

const TASK_ASSET_SOURCE_LABELS: Record<string, string> = {
  agent: "Agent Remove",
  anchor: "Blackboard Anchor Point",
  api: "Asset API",
  company: "Enterprise Association",
  legacy: "Historical associations",
  manual: "Manually added",
  system: "System Association",
  task: "Task initialization",
};

export function taskAssetTypeLabel(type: NewAssetType): string {
  return ASSET_TYPE_LABELS[type];
}

export function taskAssetSourceLabel(source: string): string {
  return TASK_ASSET_SOURCE_LABELS[source] ?? source;
}
