"use client";

import * as React from "react";

import {
 BuildingIcon,
 ChevronRightIcon,
 CircleDashedIcon,
 GlobeIcon,
 LayoutTemplateIcon,
 LinkIcon,
 type LucideIcon,
 NetworkIcon,
 RefreshCwIcon,
 SearchIcon,
 SmartphoneIcon,
} from "lucide-react";

import { Button } from "@/components/ui/button";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import type { FindingAssetKind, FindingAssetNode } from "@/lib/types";
import { cn } from "@/lib/utils";

// Icons inherit asset page type mapping.,The same asset looks identical in two places.
const KIND_ICON: Record<FindingAssetKind, LucideIcon> = {
 company: BuildingIcon,
 root_domain: GlobeIcon,
 subdomain: GlobeIcon,
 ip: NetworkIcon,
 app: SmartphoneIcon,
 service: LayoutTemplateIcon,
 endpoint: LinkIcon,
 none: CircleDashedIcon,
};

const KIND_LABEL: Record<FindingAssetKind, string> = {
 company: "enterprise",
 root_domain: "Root domain",
 subdomain: "Subdomain",
 ip: "IP",
 app: "Application",
 service: "Use",
 endpoint: "Interface",
 none: "Not associated",
};

// TreeNode It is a tree assembled from node arrays. The backend has already sorted it by 'more discovered under the same parent first',
// This only needs to be mounted in array order.
interface TreeNode extends FindingAssetNode {
 children: TreeNode[];
 depth: number;
 /** The actual text rendered on the tree.;The full label is still retained in the label.(Used for hover tips and breadcrumbs). */
 display: string;
}

function stripBrackets(host: string) {
 return host.startsWith("[") && host.endsWith("]")? host.slice(1, -1): host;
}

function parseAssetURL(raw: string): URL | null {
 try {
 return new URL(raw);
 } catch {
 return null;
 }
}

// hostOf Take one node to represent the host:URL get hostname,'host:port'Get Host,The rest is.
// Tag itself.(Root domain / subdomain / IP).
function hostOf(label: string): string {
 const url = parseAssetURL(label);
 if (url) return stripBrackets(url.hostname);
 const hostPort = label.match(/^(.+):(\d+)$/);
 return stripBrackets(hostPort? hostPort[1]: label);
}

// shortLabel Remove duplicate prefixes with parent node. Service / endpoint labels are complete URLs.,and
// The host domain/IP has already been written on the previous line. —— Deep nodes are already narrow,Repeat the host once more.,Truly Exists
// The information volume of the port and path are all truncated. The full value is still in title and breadcrumb.
function shortLabel(node: FindingAssetNode, parent?: FindingAssetNode): string {
 if (!parent) return node.label;

 // Subdomains hanging under root domain:Remove root domain suffix,Keep only your own segment.
 if (node.kind === "subdomain" && node.label.endsWith(`.${parent.label}`)) {
 return node.label.slice(0, -(parent.label.length + 1)) || node.label;
 }
 if (node.kind!== "service" && node.kind!== "endpoint") return node.label;

 // The parent tag is exactly the prefix of its own(Interface on same URL as service, service on same IP):Cut directly.
 if (node.label.startsWith(parent.label)) {
 return node.label.slice(parent.label.length) || node.label;
 }

 // Only if the parent node is indeed the host of this URL will it be abbreviated.,Otherwise, identification information will be lost
 // (For example, a service directly hanging under the root domain due to missing subdomain assets.,Then it must display the full URL).
 if (hostOf(node.label)!== hostOf(parent.label)) return node.label;

 const url = parseAssetURL(node.label);
 if (!url) return node.label;
 if (node.kind === "endpoint") return `${url.pathname}${url.search}` || "/";
 const scheme = url.protocol.replace(":", "");
 const port = url.port || (url.protocol === "https:"? "443": "80");
 return `${scheme}:${port}`;
}

export function buildAssetTree(nodes: FindingAssetNode[]): TreeNode[] {
 const byKey = new Map<string, TreeNode>();
 for (const node of nodes) {
 byKey.set(node.key, { ...node, children: [], depth: 0, display: node.label });
 }
 const roots: TreeNode[] = [];
 for (const node of nodes) {
 const current = byKey.get(node.key);
 if (!current) continue;
 const parent = node.parent? byKey.get(node.parent): undefined;
 // Parent node missing.(Truncated levels dropped.)Elevate to top level,Do not let the entire subtree disappear.
 if (parent) {
 parent.children.push(current);
 current.display = shortLabel(node, parent);
 } else {
 roots.push(current);
 }
 }
 const setDepth = (node: TreeNode, depth: number) => {
 node.depth = depth;
 for (const child of node.children) setDepth(child, depth + 1);
 };
 for (const root of roots) setDepth(root, 0);
 return roots;
}

// assetPathOf Returning the path from the top level to this node.,Used for right-side breadcrumbs. Each level should only display relative
// Increment of the previous level(display),Full value left in label.
export function assetPathOf(nodes: FindingAssetNode[], key: string | null): (FindingAssetNode & { display: string })[] {
 if (!key) return [];
 const byKey = new Map(nodes.map((n) => [n.key, n]));
 const path: FindingAssetNode[] = [];
 const seen = new Set<string>();
 let current = byKey.get(key);
 while (current &&!seen.has(current.key)) {
 seen.add(current.key);
 path.unshift(current);
 current = current.parent? byKey.get(current.parent): undefined;
 }
 return path.map((node, index) => ({ ...node, display: shortLabel(node, path[index - 1]) }));
}

// filterTree Filter by keyword:Nodes hit retained,And retain its entire ancestor chain(Ancestors can miss without hitting themselves).
// Retain all descendants of the matched node.,For easier further drilling.
function filterTree(nodes: TreeNode[], keyword: string): TreeNode[] {
 const kw = keyword.trim().toLowerCase();
 if (!kw) return nodes;
 const walk = (node: TreeNode): TreeNode | null => {
 const hit = node.label.toLowerCase().includes(kw);
 if (hit) return node;
 const children = node.children.map(walk).filter((c): c is TreeNode => c!== null);
 if (children.length === 0) return null;
 return { ...node, children };
 };
 return nodes.map(walk).filter((n): n is TreeNode => n!== null);
}

// collectKeys Collect a tree(sub)All keys in the tree,Used for 'expand all matches'
function collectKeys(nodes: TreeNode[], out: Set<string> = new Set()): Set<string> {
 for (const node of nodes) {
 out.add(node.key);
 collectKeys(node.children, out);
 }
 return out;
}

interface AssetTreeProps {
 nodes: FindingAssetNode[];
 selected: string | null;
 onSelect: (key: string | null) => void;
 loading?: boolean;
 truncated?: boolean;
 droppedKinds?: string[];
 /** Total number of findings displayed on the right when no assets are selected.,For the 'All Assets' row. */
 findingTotal: number;
 /** Asset view does not poll,Tree counts are refreshed by this button or page additions/deletions/edits. */
 onRefresh?: () => void;
}

export function AssetTree({
 nodes,
 selected,
 onSelect,
 loading,
 truncated,
 droppedKinds,
 findingTotal,
 onRefresh,
}: AssetTreeProps) {
 const [keyword, setKeyword] = React.useState("");
 const [expanded, setExpanded] = React.useState<Set<string>>(() => new Set());
 // Remember user manually folded nodes.,To avoid 'Default Expand Top Level' repeatedly opening them on each refresh.
 const [collapsed, setCollapsed] = React.useState<Set<string>>(() => new Set());

 const roots = React.useMemo(() => buildAssetTree(nodes), [nodes]);
 const visible = React.useMemo(() => filterTree(roots, keyword), [roots, keyword]);

 // When searching, expand all matched branches,Otherwise, items hidden in collapsed nodes are equal to no search.
 const searching = keyword.trim()!== "";
 const searchKeys = React.useMemo(() => (searching? collectKeys(visible): null), [searching, visible]);

 const isExpanded = React.useCallback(
 (node: TreeNode) => {
 if (searchKeys) return searchKeys.has(node.key);
 if (expanded.has(node.key)) return true;
 // Top-level default expand one layer:Deeper levels require user interaction to expand.,to avoid spreading thousands of lines at once.
 return node.depth === 0 &&!collapsed.has(node.key);
 },
 [collapsed, expanded, searchKeys],
);

 const toggle = React.useCallback(
 (node: TreeNode) => {
 const open = isExpanded(node);
 setExpanded((prev) => {
 const next = new Set(prev);
 if (open) next.delete(node.key);
 else next.add(node.key);
 return next;
 });
 setCollapsed((prev) => {
 const next = new Set(prev);
 if (open) next.add(node.key);
 else next.delete(node.key);
 return next;
 });
 },
 [isExpanded],
);

 let emptyHint = "No associated findings under the current filter.";
 if (loading) emptyHint = "Loading......";
 else if (searching) emptyHint = "No matching assets.";

 const rows: React.ReactNode[] = [];
 const pushRows = (list: TreeNode[]) => {
 for (const node of list) {
 const open = isExpanded(node);
 rows.push(
 <AssetTreeRow
 key={node.key}
 node={node}
 open={open}
 selected={selected === node.key}
 onToggle={() => toggle(node)}
 onSelect={() => onSelect(selected === node.key? null: node.key)}
 />,
);
 if (open && node.children.length > 0) pushRows(node.children);
 }
 };
 pushRows(visible);

 return (
 <div className="flex min-h-0 flex-1 flex-col gap-2">
 <div className="flex items-center gap-1">
 <InputGroup className="flex-1">
 <InputGroupInput
 type="search"
 value={keyword}
 onChange={(event) => setKeyword(event.target.value)}
 placeholder="Filter Assets"
 aria-label="Filter Assets"
 />
 <InputGroupAddon>
 <SearchIcon aria-hidden="true" />
 </InputGroupAddon>
 </InputGroup>
 {onRefresh && (
 <Button
 size="icon"
 variant="ghost"
 className="size-8 shrink-0 text-muted-foreground"
 onClick={onRefresh}
 disabled={loading}
 aria-label="Refresh asset tree;"
 title="Refresh asset tree;"
 >
 <RefreshCwIcon className={cn("size-4", loading && "animate-spin")} />
 </Button>
)}
 </div>

 <button
 type="button"
 onClick={() => onSelect(null)}
 className={cn(
 "flex items-center justify-between gap-2 rounded-md px-2 py-1.5 text-left text-sm",
 selected === null? "bg-accent font-medium": "hover:bg-accent/50",
)}
 >
 <span>All Assets</span>
 <span className="text-xs tabular-nums text-muted-foreground">{findingTotal}</span>
 </button>

 <div className="max-h-[24rem] min-h-0 flex-1 overflow-y-auto pr-2 lg:max-h-[calc(100vh-16rem)]">
 <div className="flex flex-col">
 {rows}
 {rows.length === 0 && <p className="px-2 py-8 text-center text-xs text-muted-foreground">{emptyHint}</p>}
 </div>
 </div>

 {truncated && (
 <p className="px-1 text-xs text-muted-foreground">
 Too many assets, hidden{(droppedKinds?? []).map((k) => KIND_LABEL[k as FindingAssetKind]?? k).join(" / ")}
 Level (count still included in upper level). Narrow with filters or filters to see the full hierarchy.
 </p>
)}
 </div>
);
}

function AssetTreeRow({
 node,
 open,
 selected,
 onToggle,
 onSelect,
}: {
 node: TreeNode;
 open: boolean;
 selected: boolean;
 onToggle: () => void;
 onSelect: () => void;
}) {
 const Icon = KIND_ICON[node.kind]?? GlobeIcon;
 const hasChildren = node.children.length > 0;
 return (
 <div
 className={cn(
 "group flex items-center gap-1 rounded-md pr-1 text-sm",
 selected? "bg-accent": "hover:bg-accent/50",
)}
 style={{ paddingLeft: `${node.depth * 10}px` }}
 >
 {hasChildren? (
 <button
 type="button"
 onClick={onToggle}
 className="flex size-5 shrink-0 items-center justify-center rounded text-muted-foreground hover:text-foreground"
 aria-label={open? "Collapse": "Expand"}
 aria-expanded={open}
 >
 <ChevronRightIcon className={cn("size-3.5 transition-transform", open && "rotate-90")} />
 </button>
): (
 <span className="size-5 shrink-0" />
)}
 <button
 type="button"
 onClick={onSelect}
 className="flex min-w-0 flex-1 items-center gap-1.5 py-1 text-left"
 title={`${KIND_LABEL[node.kind]?? node.kind} · ${node.label}`}
 >
 <Icon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
 <span className={cn("min-w-0 truncate", selected && "font-medium")}>{node.display}</span>
 </button>
 <span className="flex shrink-0 items-center gap-1 text-xs tabular-nums">
 {node.critical > 0 && (
 <span className="text-rose-600" title={`Support ${node.critical}`}>
 {node.critical}
 </span>
)}
 {node.high > 0 && (
 <span className="text-red-500" title={`Retest ${node.high}`}>
 {node.high}
 </span>
)}
 <span className="text-muted-foreground" title={`Total ${node.total} Findings`}>
 {node.total}
 </span>
 </span>
 </div>
);
}
