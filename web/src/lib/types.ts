// ARTEX domain model — types used across the UI.
// Derived from the functional spec (section 7: Key data shape.).

export type TaskStatus = "created" | "queued" | "running" | "paused" | "done" | "failed" | "timeout";
export type EngineMode = "exploring" | "paused" | "stalled" | "idle";

export interface Task {
 id: string;
 name?: string; // Optional task name;Empty/Default=Unnamed,Revert to description when displaying.
 category_id?: number;
 category_name?: string;
 pinned?: boolean;
 pinned_at?: string | null;
 description: string;
 goal: string;
 status: TaskStatus;
 created_at: string;
 created_unix?: number; // created_at as unix seconds (run-duration calc)
 completed_at?: string; // RFC3339 finish time (done/failed); "" if unfinished
 completed_unix?: number; // completed_at as unix seconds (0/undef if unfinished)
 last_activity_unix?: number; // unix seconds of the last activity (0/undef if none)
 paused?: boolean;
 queued?: boolean;
 active?: boolean;
 in_flight?: number;
 findings?: { critical: number; high: number; medium: number; low: number }; // Number of registered vulnerabilities(Categorized by severity)
 last_activity?: string;
 stalled?: boolean;
 goals_total?: number;
 goals_met?: number;
 engine_mode?: EngineMode;
 tokens?: TokenTotal; // whole-task token consumption
 llm_profile_id?: number; // LLM profile used; absent = default profile
 llm_profile_ids?: number[]; // ordered task-level failover chain
 active_llm_profile_id?: number; // profile used by the next LLM call
 llm_failover_state?: "default" | "ready" | "chain_exhausted" | string;
 llm_failover_reason?: string;
 source_task_ids?: string[]; // directly related tasks inherited as read-only context
 archive_blocked_by_task_id?: string; // live direct dependent that must be archived first
 company_ids?: number[]; // associated company scopes; current company assets join the task at creation
 coverage_enabled?: boolean; // Asset coverage function switch(Set at creation,Default On); false=Do not calculate/show coverage.
}

export interface TaskCategory {
 id: number;
 name: string;
 task_count: number;
 created_at: string;
 updated_at: string;
}

export interface TaskTemplate {
 id: number;
 name: string;
 description: string;
 goal: string;
 category_id?: number | null; // Preset category; null/default=no
 intercept_rules?: AssetInterceptRuleInput[]; // Preset task-level intercept/allow rules.
 created_at: string;
 updated_at: string;
}

export interface DeleteTaskOptions {
 delete_assets: boolean;
 delete_traffic: boolean;
 delete_files: boolean;
 delete_findings: boolean;
 delete_llm_records: boolean;
}

export interface DeleteTaskResult {
 deleted: string;
 assets_deleted: number;
 assets_detached: number;
 traffic_deleted: number;
 files_deleted: boolean;
 findings_deleted: number;
 llm_records_deleted: number;
 cleanup_warning?: string;
}

export type TaskArchiveState =
 | "archive_queued"
 | "archiving"
 | "archive_failed"
 | "ready"
 | "restore_queued"
 | "restoring"
 | "restore_failed"
 | "delete_queued"
 | "deleting"
 | "delete_failed";

export interface TaskArchiveTokenStats {
 calls?: number;
 input_tokens?: number;
 output_tokens?: number;
 cache_read_tokens?: number;
 cache_write_tokens?: number;
}

export interface TaskArchive {
 id: number;
 task_id: number;
 state: TaskArchiveState;
 phase: string;
 progress: number;
 error?: string;
 warnings?: string[];
 format_version: number;
 sha256?: string;
 original_size: number;
 compressed_size: number;
 task_name: string;
 task_description: string;
 task_goal: string;
 original_status: TaskStatus;
 category_id?: number;
 category_name?: string;
 source_task_ids: number[];
 remaining_timeout_seconds: number;
 data_counts: Record<string, number>;
 aggregate_stats: {
 tokens?: TaskArchiveTokenStats;
 skills?: Record<string, number>;
 tools?: Record<string, number>;
 findings?: Record<string, number>;
 };
 archived_at?: string;
 requested_at: string;
 created_at: string;
 updated_at: string;
}

export interface TaskArchivePage {
 items: TaskArchive[];
 total: number;
 page: number;
 size: number;
}

export interface ArchiveBatchItem {
 id: string;
 archive_id?: number;
 ok: boolean;
 queued: boolean;
 error?: string;
}

// ---- Asset graph (global, shared across tasks) ----
export type AssetType =
 | "company"
 | "domain"
 | "ip"
 | "port"
 | "service"
 | "site"
 | "endpoint"
 | "parameter"
 | "tech"
 | "credential"
 | "data";

export type NodeState = "observed" | "confirmed" | "tombstoned";

export interface AssetNode {
 id: string;
 type: AssetType;
 name: string;
 key: string; // nkey
 value?: string;
 company_id?: string; // Owner company asset ID; empty=Unassigned
 state: NodeState;
 confidence: number; // 0..1
 attrs?: Record<string, unknown>;
 first_seen: string;
 last_seen: string;
}

export type AssetRel =
 | "owns"
 | "resolves"
 | "exposes"
 | "runs"
 | "serves"
 | "has_endpoint"
 | "has_param"
 | "fingerprinted"
 | "authenticates_as"
 | "reachable"
 | "has_subdomain";

export interface Edge {
 src: string;
 dst: string;
 rel: AssetRel | ExploreRel;
}

// Task asset view — server-side enriched, paginated.
export interface TaskAssetRef {
 id: string;
 name?: string;
 key: string;
 attrs?: Record<string, unknown>;
}

export interface TaskAssetItem extends AssetNode {
 techs?: TaskAssetRef[];
 auth?: TaskAssetRef[];
 params?: TaskAssetRef[];
}

export interface TaskAssetView {
 counts: Record<string, number>;
 total: number;
 items: TaskAssetItem[];
}

// ---- New unified asset model (new backend) ----
export type NewAssetType = "root_domain" | "ip" | "subdomain" | "app" | "service" | "endpoint";

export interface Asset {
 id: number;
 type: NewAssetType;
 company_id?: number;
 task_ids: number[];
 domain?: string;
 root_domain?: string;
 ip?: string;
 c_segment?: string;
 port?: number;
 icp?: string;
 bound_domains?: string[];
 open_ports?: { port: number; service?: string }[];
 record_type?: string;
 record_value?: string[] | string;
 bundle_id?: string;
 app_name?: string;
 category?: string;
 app_description?: string;
 app_icp?: string;
 url?: string;
 service_type?: string;
 service_name?: string;
 favicon_mmh3?: string;
 status_code?: number;
 content_length?: number;
 page_title?: string;
 technologies?: string[];
 auth?: Record<string, unknown>[];
 method?: string;
 params?: Record<string, unknown>[];
 extra?: Record<string, unknown>;
 last_seen: string;
 task_source?: string;
 task_source_summary?: string;
 task_source_node_id?: number;
}

export interface IntentAsset {
 intent_id: number | string;
 asset_id: number;
 type: NewAssetType;
 label: string;
 source: string;
 source_summary: string;
 source_node_id?: number;
 source_task_id: number;
 inherited: boolean;
}

export interface TaskAssetMutation {
 requested: number;
 attached: number;
 existing: number;
}

export interface TaskAssetScopeMutation {
 requested: number;
 assets_linked: number;
 assets_existing: number;
 scopes_added: number;
 scopes_existing: number;
}

// ---- Asset coverage graph (per task) ----
// Node in force-directed 'asset coverage graph'. Key unique: Asset="a:<id>", Company="c:<id>",
// Root domain with no assets="r:<domain>". in_scope=false This is a gray context node used only for connecting.
export interface CoverageGraphNode {
 key: string;
 kind: "company" | "root_domain" | "subdomain" | "ip" | "service" | "app" | "endpoint";
 label: string;
 tested: boolean;
 in_scope: boolean;
 asset_id?: number;
 company_id?: number;
 domain?: string;
 root_domain?: string;
 ip?: string;
 url?: string;
 port?: number;
 service_type?: string;
 app_name?: string;
 page_title?: string;
 status_code?: number;
}

export interface CoverageGraphEdge {
 src: string;
 dst: string;
}

export interface CoverageGraphData {
 nodes: CoverageGraphNode[];
 edges: CoverageGraphEdge[];
}

// Intent/fact/vulnerability associated with an asset in this task exploration graph (to cover the graph node drawer).
export interface CoverageAssetRef {
 id: number;
 kind: string;
 state: string;
 summary: string;
 source_task_id?: string;
 inherited?: boolean;
}
export interface CoverageAssetRefs {
 intents: CoverageAssetRef[];
 facts: CoverageAssetRef[];
 findings: CoverageAssetRef[];
}

// ---- Workspace file manager (workDir) ----
export interface WorkspaceEntry {
 name: string;
 path: string; // workspace-relative, forward slashes
 dir: boolean;
 size: number;
 mtime: number; // unix millis
}
export interface WorkspaceListing {
 path: string;
 entries: WorkspaceEntry[];
}
export interface WorkspaceFile {
 path: string;
 size: number;
 binary: boolean;
 too_large?: boolean;
 content?: string;
}

// One of the task's test scopes (denominator for coverage) + Authorization boundary).
export interface TaskScopeRow {
 id: number;
 task_id: number;
 kind: "company" | "root_domain" | "subdomain" | "ip" | "cidr" | "icp" | "keyword";
 company_id?: number;
 company_name?: string; // Backend JOIN companies parsing, only kind=company Has Value
 domain?: string;
 net?: string;
 value?: string;
 source: "auto" | "agent" | "manual";
 reason?: string;
}

export type CompanyScopeKind = "domain" | "ip" | "cidr" | "icp" | "keyword";

// Structured asset range rules submitted when adding a new company.
export interface CompanyScopeRule {
 kind: CompanyScopeKind;
 value: string;
}

// The result of writing the asset range. Errors are lines that are illegal in this submission; Warnings are unrelated to this submission,
// But it will cause existing data issues with unexpected attribution results (e.g., ip field storing hostnames of assets).
export interface CompanyScopeMutation {
 added: number;
 skipped: number;
 invalid: number;
 errors?: string[];
 warnings?: string[];
}

// A rule for the company asset range (unique truth source of ownership).
export interface ScopeRow {
 id: number;
 company_id: number;
 kind: CompanyScopeKind;
 domain?: string; // kind=domain Has value when
 net?: string; // kind=ip|cidr Has value when
 value?: string; // kind=icp|keyword May be directly returned by the backend
 raw: string; // Original user input, used for display and refilling
 reason?: string;
}

// Company: type=company Asset node of + Icon + Asset count + Asset range rules.
export interface Company {
 id: number;
 name: string;
 logo?: string; // 'Remote icon URL; if empty, use the first letter of the name',
 asset_count: number;
 scope?: ScopeRow[];
}

// ---- Exploration graph (per task) ----
export type ExploreKind = "task" | "begin" | "goal" | "intent" | "fact" | "finding" | "hint" | "digest";
export type GoalState = "open" | "met" | "abandoned";
export type IntentState = "open" | "running" | "paused" | "done" | "blocked" | "exhausted" | "stopped";
export type FindingState = "confirmed" | "dismissed";
export type HintState = "active" | "consumed";
export type ExploreRel = "spawns" | "derived_from" | "yields" | "proves" | "covers";

export interface TaskNode {
 id: string;
 type: ExploreKind;
 payload?: string;
 priority: number; // 0..10
 state: string; // GoalState | IntentState | FindingState | HintState
 origin: string;
 ts: string;
 source_task_id?: string;
 inherited?: boolean;
 delete_reason?: string; // Intent fake delete(state='deleted')Reason for deletion at the time
}

// Bulletin board page:Nodes paginated by creation order + Edges involved on this page. + Node on the other end of the edge(refs,Indexed by ID.),
// So each broadcast should clearly state 'where it came from and what was produced'.,Without having to pull down the entire image.
export interface ExplorationNodePage {
 items: TaskNode[];
 total: number;
 page: number;
 size: number;
 edges: Edge[];
 refs: Record<string, TaskNode>;
 // Node ID → The asset anchored by this node(Show alongside the scoreboard when expanded.,Including this page's nodes and neighbors).
 assets: Record<string, FindingAsset[]>;
}

export interface ExplorationNodeQuery {
 page?: number;
 size?: number;
 kinds?: ExploreKind[];
 states?: string[];
 q?: string;
 order?: "asc" | "desc";
}

// Targets managed by the card(The backend has already split the payload into text/vulnclass.).
export interface TaskGoal {
 id: string;
 text: string;
 vulnclass?: string;
 state: string; // GoalState
 origin?: string;
 ts: string;
}

// Operation constraints for constraint management card.(allow=Allow / Deny=Prohibit).
export type ConstraintKind = "allow" | "deny";
export interface TaskConstraint {
 id: string;
 kind: ConstraintKind;
 text: string;
 origin?: string;
 ts?: string;
}

// ---- Findings ----
export type Severity = "critical" | "high" | "medium" | "low";

// Vulnerability Disposal Status:Pending / Processing / Confirmed / Handled / Fixed / False Positive / Ignored / Duplicate / Risk Accepted.
export type FindingStatus =
 | "pending"
 | "in_progress"
 | "confirmed"
 | "resolved"
 | "fixed"
 | "false_positive"
 | "ignored"
 | "duplicate"
 | "risk_accepted";

// FindingAsset An asset bound to a vulnerability.(Labels have already been pre-rendered on the backend.).
export interface FindingAsset {
 id: string;
 type: string;
 label: string;
}

export interface Finding {
 traffic_count?: number;
 evidence_version?: number;
 report_evidence_version?: number;
 report_stale?: boolean;
 id: string;
 finding_id?: string; // Independent findings table row id,Handle for status updates(Old nodes in tasks may be missing)
 vulnclass: string;
 name?: string; // Vulnerability name;When empty, it displays a fallback to vulnclass.
 severity: Severity;
 status: FindingStatus;
 summary: string;
 evidence: string;
 report?: string; // Detailed Report,(Markdown);Only the details interface returns,List is empty
 intent_id?: string;
 param_id?: string;
 task_id?: string;
 task_description?: string;
 source_task_id?: string;
 inherited?: boolean;
 assets?: FindingAsset[];
 ts: string;
}

// FindingsPage It is a server-side pagination response for the discovery list.
export interface FindingsPage {
 items: Finding[];
 total: number;
 page: number;
 page_size: number;
}

export interface FindingGroup {
 task_id: string | number | null;
 task_name?: string; // Optional task name;Empty/Default=Unnamed
 task_description: string;
 task_status: string;
 count: number;
 critical: number;
 high: number;
 medium: number;
 low: number;
 last_found_at: string;
}

export interface FindingGroupsPage {
 items: FindingGroup[];
 total: number;
 finding_total: number;
 page: number;
 page_size: number;
}

export interface FindingDeepenResponse {
 task_id: string;
 intent_id: string;
 state: IntentState;
 queued: boolean;
}

// FindingStats Full table aggregation is discovered.(Statistics Card + Vulnerability type dropdown.),Server-Side Calculation,Not affected by pagination.
export interface FindingStats {
 total: number;
 pending: number;
 critical: number;
 high: number;
 medium: number;
 low: number;
 vulnclasses: string[];
 tasks: FindingTaskOption[];
}

// FindingTaskOption An item on the discovery page's 'By Task' filter dropdown.:Tasks with vulnerabilities(An empty description indicates that the task has been deleted,
// Frontend backtracking display ID.)And its vulnerability count.
export interface FindingTaskOption {
 id: string | number;
 name?: string; // Optional task name;Empty/Default=Unnamed
 description: string;
 count: number;
}

// FindingQuery Parameters for discovery list pagination/filtration/sorting.
export interface FindingQuery {
 page: number;
 pageSize: number;
 severity?: "all" | Severity;
 status?: "all" | FindingStatus;
 vulnclass?: string;
 task?: string; // Task ID;"all"/empty = Not filtered by task.
 query?: string;
 sort?: "severity" | "time";
 // Asset tree node key;Select a node = Select its entire subtree. Empty = Do Not Filter by Asset
 assetScope?: string;
}

// ---- Findings by asset (Asset view) ----
export type FindingAssetKind = "company" | "root_domain" | "subdomain" | "ip" | "service" | "app" | "endpoint" | "none";

// FindingAssetNode A node in the asset tree. key like a:<id>(asset), c:<id> (company),
// r:<domain>(No root domain for asset rows in the database.), __none__(Unassociated asset).
export interface FindingAssetNode {
 key: string;
 parent?: string;
 kind: FindingAssetKind;
 label: string;
 asset_id?: number;
 company_id?: number;
 self: number; // Number of Discoveries on This Asset
 total: number; // Contains descendants, deduplicate by finding
 critical: number;
 high: number;
 medium: number;
 low: number;
 last_found_at: string;
}

export interface FindingAssetTree {
 nodes: FindingAssetNode[];
 finding_total: number;
 truncated: boolean;
 dropped_kinds?: string[];
}

// FINDING_UNASSIGNED_ASSET With backend db.FindingUnassignedAsset Corresponds to
export const FINDING_UNASSIGNED_ASSET = "__none__";

// ---- Activity / sessions ----
export type ActivityKind =
 | "tool_use"
 | "tool_result"
 | "text"
 | "thinking"
 | "result"
 | "user"
 | "intent" // LLM-generated exploration objective leading a worker session (UI-synthesized)
 | "round" // planner round boundary marker (engine-emitted)
 | "usage" // live cumulative token usage (per model turn); not rendered
 | "llm_switch" // automatic/manual task-level LLM switch
 | "llm_failover" // task-level provider switch / chain exhaustion audit event
 | "intercept_request"; // user-approval request from the intercept layer

// ChatAttachment This is a file being uploaded.:path Relative to this session/task working directory.(Just agent's CWD,).
export interface ChatAttachment {
 name: string;
 path: string;
 size: number;
 abs?: string; // Absolute Path(scope=staging Temporarily store upload results;Write it into the description before creating a task)
}

export interface Activity {
 seq: number;
 intent_id?: string;
 worker: string; // session owner: planner | mainagent | work#1 ...
 ts: string;
 kind: ActivityKind;
 tool?: string;
 tool_use_id?: string;
 is_error?: boolean;
 summary: string;
 detail?: string;
 metadata?: {
 llm_transition?: LLMTransition;
 };
 source_task_id?: string;
 inherited?: boolean;
 main_seg?: number; // main-agent conversation segment (present only on worker="mainagent" rows)
 // token usage (present only on kind='result')
 input_tokens?: number;
 output_tokens?: number;
 cache_read_tokens?: number;
 cache_write_tokens?: number;
}

export interface LLMAuditProfile {
 id: number;
 name: string;
 format: string;
 model: string;
}

export interface LLMTransition {
 mode: "automatic" | "manual" | "exhausted";
 reason: string;
 previous?: LLMAuditProfile;
 next?: LLMAuditProfile;
}

export interface TaskLLMResolution {
 profile_id?: number;
 name: string;
 format: string;
 model: string;
 source: "task_chain" | "agent_binding" | "global_profile" | "environment" | "global";
 available: boolean;
 reason?: string;
}

export interface TaskLLMResolutions {
 mainagent: TaskLLMResolution;
 planner: TaskLLMResolution;
 worker: TaskLLMResolution;
}

// ---- Agent triggers (P3 Scheduling, only custom agent) ----
export interface AgentTrigger {
 id: number;
 agent_key: string;
 enabled: boolean;
 interval_sec: number; // Schedule:Every N seconds(0=Irregularly)
 on_finding: boolean; // Trigger on any task discovery finding,
 on_goal_met: boolean; // Trigger when any task achieves the goal'
 on_task_timeout: boolean; // Trigger on any task timeout
 on_tool_call: boolean; // Selected tool called.(Execution Completed)On trigger
 on_task_create: boolean; // Any task creation triggers
 interval_message: string; // Independent user messages for each trigger condition
 finding_message: string;
 goal_message: string;
 task_timeout_message: string;
 tool_call_message: string;
 task_create_message: string;
 tool_names: string[]; // on_tool_call Key of selected tool(At least one)
 last_fire?: string;
}

// ---- Conversations (chat page) ----
export interface ActiveFindingRetest {
 id: number;
 finding_id: string;
 conversation_id: number;
 status: "pending" | "running";
}

export interface FindingRetest {
 id: number;
 finding_id: number;
 conversation_id: number | null;
 status: "pending" | "running" | "completed" | "failed" | "stopped";
 verdict: "" | "reproduced" | "fixed" | "inconclusive";
 notes: string;
 summary: string;
 evidence: string;
 error: string;
 created_at: string;
 started_at: string | null;
 finished_at: string | null;
}

export interface Conversation {
 id: number;
 running?: boolean; // live server state, returned with the conversation list
 agent_key: string;
 title: string;
 llm_profile_id?: number;
 pinned?: boolean;
 pinned_at?: string | null;
 created_at: string;
 updated_at: string;
}

// ---- Backend logs (/logs page) ----
export interface LogLine {
 seq: number;
 db_id?: number; // server_logs.id; present for DB-persisted lines
 ts: string;
 level: "info" | "warn" | "error";
 tag: string;
 text: string;
}

export type SessionRole = "mainagent" | "planner" | "worker" | "system";
export type SessionStatus = "running" | "paused" | "done" | "blocked" | "exhausted" | "pending" | "stopped" | "deleted";

// Daily token aggregate bucket (GET /api/tokens/daily).
export interface DailyTokenBucket {
 date: string; // "YYYY-MM-DD"
 input_tokens: number;
 output_tokens: number;
 cache_read_tokens: number;
 cache_write_tokens: number;
}

// Per-worker token usage (GET /api/exploration/tokens).
export interface TokenUsage {
 worker: string;
 input_tokens: number;
 output_tokens: number;
 cache_read_tokens: number;
 cache_write_tokens: number;
}

export interface SessionTokenUsage {
 session: string;
 input_tokens: number;
 output_tokens: number;
 cache_read_tokens: number;
 cache_write_tokens: number;
}

export interface BatchControlItem {
 id: string;
 ok: boolean;
 status?: string;
 queued?: boolean;
 error?: string;
}

// Batch modification of classification results per task. Failure can only be due to the task being deleted, and writing the classification itself is atomic.
export interface BatchCategoryItem {
 id: string;
 ok: boolean;
 error?: string;
}

// Whole-task (all agents) token aggregate.
export interface TokenTotal {
 input_tokens: number;
 output_tokens: number;
 cache_read_tokens: number;
 cache_write_tokens: number;
}

// Global per-profile token spend from the llm_usage ledger (GET /api/tokens/usage).
export interface ProfileUsage {
 profile_name: string;
 calls: number;
 tasks: number;
 input_tokens: number;
 output_tokens: number;
 cache_read_tokens: number;
 cache_write_tokens: number;
}

// One (profile, UTC day) token bucket for the dashboard's daily chart (new source).
export interface ProfileDayUsage {
 profile_name: string;
 date: string; // YYYY-MM-DD
 input_tokens: number;
 output_tokens: number;
 cache_read_tokens: number;
}

// Response of GET /api/tokens/usage — the dashboard's "new" (llm_usage) token view.
export interface UsageStats {
 by_profile: ProfileUsage[];
 daily: ProfileDayUsage[];
}

// Per-model token usage for one task (GET /api/llm/records/by-model), from the
// always-on llm_usage metering ledger. calls = number of LLM calls on this model.
export interface ModelTokenStat {
 model: string;
 calls: number;
 input_tokens: number;
 output_tokens: number;
 cache_read_tokens: number;
 cache_write_tokens: number;
}

export interface Session {
 id: string;
 role: SessionRole;
 title: string;
 status: SessionStatus;
 live: boolean;
 last_activity: string;
 intent_id?: string;
 source_task_id?: string;
 inherited?: boolean;
 seg?: number; // main-agent session: which conversation segment (0 = original)
}

// ---- Security ----
export interface AuditEntry {
 ts: string;
 tool: string;
 action: "allow" | "block";
 reason?: string;
 command?: string;
}

export interface Audit {
 entries?: AuditEntry[];
 attributions?: Record<string, number>;
}

// ---- Traffic ----
export interface TrafficExchange {
 id: string;
 ts: string;
 host: string;
 method: string;
 url: string;
 status: number;
 content_type: string;
 resp_len: number;
}

export interface TrafficResp {
 enabled: boolean;
 proxy?: string;
 count?: number; // global total (unfiltered)
 total?: number; // rows matching the current filter (for pagination)
 page?: number;
 size?: number;
 exchanges?: TrafficExchange[];
}

// Full raw request/response of one exchange (lazy-loaded on row select).
export interface TrafficDetail {
 req: string;
 resp: string;
}

// One distinct recorded host with its exchange count (target picker).
export interface TrafficHost {
 host: string;
 count: number;
}

// ---- App settings (runtime toggles) ----
export interface Settings {
 traffic_capture: boolean;
 agent_traffic_binding: boolean; // Agent Automatic binding of traffic evidence, default off; does not affect manual binding.
 llm_record: boolean; // LLM Recording Switch (default off); when closed, no LLM calls are recorded
 // Web search. brave_key_set / tavily_key_set reflect whether a key is stored
 // (the values are never returned). On PUT, send the corresponding field to set/clear.
 web_search_enabled: boolean;
 web_search_backend: string; // "ddgs" | "brave-free" | "tavily" | "deepseek"
 brave_key_set: boolean;
 tavily_key_set: boolean;
 // write-only: only sent on PUT to store/clear the key.
 brave_search_api_key?: string;
 tavily_search_api_key?: string;
 // Independent exit proxy(http/https/socks5), Used to access search endpoint; unrelated to recording traffic MITM proxy. Empty=Direct Connection.
 web_search_proxy?: string;
 // Global exit proxy(http/https/socks5, Can include user:pass), All target traffic should pass through it. As a fallback when starting traffic capture,
 // MITM Upstream; directly inject agent's bash/WebFetch when capturing is turned off. Empty=Direct Connection.
 global_proxy?: string;
 python_interpreter?: string; // Python interpreter path for custom script tools(empty=Runtime detection)
 workers?: number; // Concurrent working agent number(Default 3); Effective for tasks started afterwards.
 // Task concurrency limit:The upper limit for simultaneously running tasks. Close=Unlimited;After enabling, new task limits will be queued.,Automatically start if there is an empty slot.
 task_concurrency_enabled?: boolean; // Default false
 task_concurrency_limit?: number; // Default is 5
 // LLM Polling(Fault transfer). Default is off; if enabled, an 'unspecified model' agent in the current configuration will be unavailable.
 // (Switch to the next configuration when balance is insufficient/key expires/rate-limited/service exception.
 llm_pool_enabled?: boolean; // Default false
 // Whether an agent/task bound to a specific configuration falls back to the polling chain when it fails. Default is false = Binding is Exclusive.
 llm_pool_bind_fallback?: boolean;
 // Operation constraint injection range(Default is on):Concatenate task's allow/deny constraints into corresponding agent system prompts.
 constraints_inject_planner?: boolean;
 constraints_inject_worker?: boolean;
 // Experimental Features:noa Model-driven context compression(Default off). Enable four types of agents that the platform connects to after enabling(planner/
 // worker/main agent/dialogue)Context compression taken over by noa,Replace built-in compaction;Read once per run,For
 // These will take effect after the next run.
 noa_compaction?: boolean;
 // ---- Vulnerability IM push (the channel itself is an independent resource, see /api/notify/).*, There are only three global configurations here)----
 notify_enabled?: boolean; // 'Total switch for push notifications; default is on; used for one-click bleeding stop during maintenance periods',
 notify_public_base_url?: string; // External access address for vulnerability detail redirection; empty=Message without linkback.
 notify_digest_interval_min?: number; // Summary mode cycle (minutes), default 30.
}

// ---- Vulnerability IM Push ----

// NotificationFilter Filter condition for channel, all fields optional, default is no filtering.
// Backend does not validate any fields: when misconfigured, handle as 'hit' (prefer more pushes over fewer).
export interface NotificationFilter {
 min_severity?: string; // "" | low | medium | high | critical
 task_ids?: number[]; // empty=Unlimited; if not empty, it must intersect with the task to which the vulnerability belongs
 asset_ids?: number[]; // empty=Unlimited; non-empty requires intersection with vulnerability anchor assets.
 vulnclass_include?: string[]; // empty=All received; if non-empty, requires any keyword match in vulnerability type (case-insensitive substring)
 vulnclass_exclude?: string[]; // 'Exclude any keyword that matches (exclusion takes precedence over inclusion)',
 on_status_change?: boolean; // Also receive vulnerability disposition status change events?
}

// NotificationChannel It is a channel instance. The fields of config vary with kind,
// And credential fields are replaced when read. "__masked__" Mask value at the beginning——Returning it as is indicates 'no change'.
export interface NotificationChannel {
 id: number;
 name: string;
 kind: string;
 enabled: boolean;
 mode: "realtime" | "digest";
 config: Record<string, unknown>;
 filter: NotificationFilter;
 rate_per_min: number;
 created_at: string;
 updated_at: string;
 // secret_keys Given by backend based on channel type, frontend renders password box and 'leave empty to not change' prompt accordingly,
 // Do not hardcode any channel knowledge.
 secret_keys: string[];
}

// NotificationKind The channel type metadata returned by /api/notify/meta.
export interface NotificationKind {
 kind: string;
 default_rate_per_min: number;
 secret_keys: string[];
}

export interface NotificationMeta {
 kinds: NotificationKind[];
 enabled: boolean;
 public_base_url: string;
 digest_interval_min: string;
 defaults: { digest_interval_min: number };
 stats: {
 channels: number;
 channels_on: number;
 pending: number;
 failed: number;
 sent_today: number;
 backlog_age_ms: number;
 };
}

// NotificationDelivery A delivery record used for historical deliveries and failed re-sends.
export interface NotificationDelivery {
 id: number;
 finding_id: string;
 event_kind: string; // finding_created | finding_status_changed
 channel_id: number;
 channel_name: string;
 channel_kind: string;
 state: "pending" | "sending" | "sent" | "failed" | "skipped";
 attempts: number;
 last_error: string;
 batch_id?: number;
 created_at: string;
 sent_at?: string;
 next_attempt_at: string;
 title: string;
 severity: string;
}

// ---- LLM config ----
export interface LLMProfile {
 id: string;
 name: string;
 format: "openai" | "anthropic" | "openai-responses";
 base_url?: string;
 proxy?: string;
 model: string;
 api_key_hint?: string;
 rate_per_second: number;
 rate_per_minute: number;
 context_window_k?: number;
 // Think toggle(thinking.type): ""=Do not send(Replacement) | "disabled"=Close | "enabled"=enable
 thinking_type?: string;
 // thinking intensity: ""=Do not send(Replacement) | "low"/"medium"/"high"/"xhigh"/"max"
 reasoning_effort?: string;
 is_default: boolean;
 // Polling priority: the larger the value, the earlier it is selected. Active configuration is always at the head of the chain, unrelated to this value.
 priority?: number;
 // true = Not used as a fallback target (still can be explicitly bound by agent/task);
 pool_exclude?: boolean;
 // true (Default)= stream(SSE) | false = True·Non-streaming(stream:false, Return once).
 streaming?: boolean;
 // Output limit for single response(token). 0 = Do not send this field, let the server decide on default value.
 // Note the difference from context_window_k: The latter is the model's total capacity, used only locally for compression thresholds.
 max_tokens?: number;
 // Specify which request field name to limit, only format.="openai" Meaningful:
 // ""=max_tokens(Replacement) | "max_completion_tokens"(OpenAI The inference model only recognizes it.)
 max_tokens_field?: string;
 // Custom session header name: if not empty, include this HTTP header with each request, header value=The session ID of the current session/intent.
 // ""=Do not send. Used as a gateway for prompt caching/sticky routing based on the session-id header.
 session_header_key?: string;
 // Coverage of this configuration for retries (connection/empty response/same provider security window). Leave empty/0 = Follow global policy
 retry?: LLMRetryOverride;
}

// ---- LLM Retry strategy ----
// Two knobs for one retry. Both are '0' = Unconfigured':
// attempts 0=Use default count | -1=Close retry at this layer | >0=Retry Count
// interval_ms 0=Use default exponential backoff. | >0=switch to this fixed millisecond interval
export interface LLMRetryRule {
 attempts: number;
 interval_ms: number;
}

// A single LLM configuration can cover three layers (all 'follow the endpoint' retries).
export interface LLMRetryOverride {
 connect: LLMRetryRule; // Reconnection Retry: Connection reset/timeout/429/5xx before stream starts
 empty: LLMRetryRule; // Empty response retry: completed but no content (only in openai format)
 stream: LLMRetryRule; // 'Retry on the same provider's security window: replay of a broken stream before delivery'
}

// Global policy = Default value for the three layers above + Only at the global level:
// breaker Polling circuit breaker (attempts=Continuous transient failures trigger a circuit breaker, interval_ms=Fixed cooldown duration.
// intent Retry intent (worker ends with model_error, retry entire intent)
export interface LLMRetryPolicy extends LLMRetryOverride {
 breaker: LLMRetryRule;
 intent: LLMRetryRule;
}

// ---- LLM Polling (Failover)----
// A configuration's position and health status in a polling chain. state:
// ok Deepen
// degraded Continuous Failures But Below Circuit Breaker Threshold
// tripped Already tripped, skipped during cooldown period (cooldown_secs remaining seconds)
export interface LLMPoolMember {
 profile_id: string;
 name: string;
 model: string;
 format: string;
 priority: number;
 active: boolean; // Is this the currently active configuration (always at the head of the chain)?
 excluded: boolean; // pool_exclude: Do not participate in polling
 state: "ok" | "degraded" | "tripped";
 fails: number;
 trips: number;
 cooldown_secs: number;
 last_error?: string;
 last_at?: string;
}

export interface LLMPoolStatus {
 enabled: boolean;
 bind_fallback: boolean;
 chain: LLMPoolMember[];
}

// ---- Agents ----
export interface Agent {
 id: string;
 key: string; // Built-in as goals/planner/mainagent/worker; custom as user-defined key
 name: string;
 description?: string;
 role: string;
 builtin: boolean;
 enabled: boolean;
 llm_profile_id?: number | null; // Bound LLM configuration; null/absent = Follows task/session/global.
 max_turns?: number; // 0 = No limit
 run_seconds?: number; // worker Single run wall-clock limit(Second); 0 = No limit
 web_search?: boolean; // Enable network search?(Controlled by system-wide switch.)
 interactive_shell?: boolean; // Enable interactive shell?(Persistent PTY session tool family)
 // P3 Handle strategy after trigger(Only custom agents are meaningful.)
 trigger_run_mode?: "serial" | "parallel"; // Serial queue / each trigger starts a session concurrently
 trigger_merge_mode?: "by_task" | "all" | "none"; // Only serial: merge with task / all / no merge
 trigger_max_parallel?: number; // Only for parallel execution: upper limit of concurrent operations per agent; 0=Unlimited
 // Binding Count(Only returned by list interface): Visible MCP / Visible Skill / Bound Tool
 mcp_count?: number;
 skill_count?: number;
 tool_count?: number;
}

export interface PromptVar {
 name: string;
 description: string;
 example: string;
 source: "exploration" | "runtime" | "distilled";
}

export interface PromptVersion {
 version: number;
 ts: string;
 note: string;
 template_text: string;
}

export interface AgentDetail {
 agent: Agent;
 prompt: string;
 variables: PromptVar[];
 versions: PromptVersion[];
 visibility: { mcp: number[]; skill: string[] };
 // Bindable LLM configuration candidates(For the 'Default Model' dropdown); Current binding see agent..llm_profile_id
 llm_profiles?: { id: number; name: string; model: string; is_default: boolean }[];
 wrapup_prompt?: string; // Saved end-of-task prompts(empty=Use built-in defaults)
 wrapup_default?: string; // Built-in default start and end prompts(Placeholder/Restore default.)
 wrapup_max_turns?: number; // Saved end loop count(0=Use built-in defaults)
 wrapup_max_turns_default?: number; // Built-in default number of rounds.(provide "0=Default N" Hint)
 // Task-level timeout closing words(Only worker/planner, task_timeout_wrapup_supported=true Only show this partition when ready)
 task_timeout_wrapup_supported?: boolean;
 task_timeout_wrapup_prompt?: string;
 task_timeout_wrapup_default?: string;
 task_timeout_wrapup_max_turns?: number;
 task_timeout_wrapup_max_turns_default?: number;
}

// ---- MCP ----
export interface MCPServer {
 id: number;
 name: string;
 transport: "stdio" | "http" | "sse";
 command?: string;
 args: string[];
 env: Record<string, string>;
 url?: string;
 enabled: boolean;
 insecure?: boolean; // http: skip TLS cert verification (self-signed servers)
 tools?: string[]; // mcp_tools_cache (names only, for the count)
}

export interface MCPTool {
 name: string;
 description: string;
}

// ---- Skills ----
// Fields align with the agentskills.io open specification.
// description covers both "what the skill does" and "when to use it".
export interface SkillItem {
 name: string; // unique key = directory name
 description?: string; // required per spec; covers what + when to use
 license?: string; // optional: SPDX identifier or free text
 compatibility?: string; // optional: environment requirements
 mcps?: string[]; // MCP server names this skill unlocks on load
 files: string[]; // files in the skill directory
 // Skill usage statistics (skill_usage ledger). Skills never called: calls=0, last_used Default.
 calls: number;
 tasks: number; // Number of tasks it has been loaded into (chat sessions not counted)
 usage_agents: string[]; // Load agent key that has loaded it
 last_used?: string;
}

// SkillCall It is a Skill once.() Call (recent call list for a single skill).
export interface SkillCall {
 ts: string;
 agent_key: string;
 task_id: number; // 0 = Non-task scenarios (dialogue sessions)'
 session_id: string;
 args_len: number;
}

// MissingSkill A named but non-existent skill. —— "Want to use but don't have"The Gap.
export interface MissingSkill {
 skill: string;
 calls: number;
 agents: string[];
 last_used?: string;
}

// ---- Tools (Built-in tool directory) ----
// key + handler live in Go; only these fields are page-editable. system tools lock
// the key and the parameter *structure* (name/type/required) — the per-param
// description/default and the agent binding are what move.
export interface Tool {
 key: string;
 system: boolean;
 description: string;
 // eslint-disable-next-line @typescript-eslint/no-explicit-any
 schema: Record<string, any>; // full JSON-Schema (object with properties)
 agents: string[]; // bound agent keys
 enabled: boolean;
 kind?: "builtin" | "shell" | "command" | "script" | "http"; // Custom tool type
 // eslint-disable-next-line @typescript-eslint/no-explicit-any
 exec?: Record<string, any>; // Custom tool execution specifications(kind!=builtin)
 deferred?: boolean; // schema Delay(SearchExtraTools/ExecuteExtraTool)
 calls?: number; // persistent runtime invocation count (older APIs may omit it)
}

// ---- Stats ----
export interface Stats {
 assets: number;
 engine_mode: EngineMode;
 llm_configured: boolean;
 roe_enabled: boolean;
 findings_confirmed: number;
 active_task?: Partial<Task>;
}

// ---- Intercept Rules ----
export type InterceptAction = "allow" | "deny" | "ask";
export type InterceptMatchTarget = "tool_name" | "tool_input";
export type InterceptMatchType = "string" | "regex";

export interface InterceptRule {
 id: number;
 name: string;
 enabled: boolean;
 priority: number;
 match_target: InterceptMatchTarget;
 match_type: InterceptMatchType;
 pattern: string;
 action: InterceptAction;
 message: string;
 timeout_enabled: boolean;
 timeout_seconds: number;
 timeout_action: "deny" | "allow";
 created_at: string;
 updated_at: string;
}

// ---- Asset Intercept Rules (Asset Interception: Global Blacklist) ----
export type AssetInterceptKind =
 | "exact_domain"
 | "exact_ip"
 | "exact_url"
 | "fuzzy_domain"
 | "fuzzy_ip"
 | "fuzzy_url"
 | "cidr";

// action Used only for task-level rules: block.=Write As(Prohibited testing) allow=Allow(Whitelist).
export type AssetInterceptAction = "block" | "allow";

// Task-level asset interception/allow rules input items (used in creating tasks and editing task details).
export interface AssetInterceptRuleInput {
 action: AssetInterceptAction;
 kind: AssetInterceptKind;
 pattern: string;
 note: string;
 enabled: boolean;
}

export interface AssetInterceptRule {
 id: number;
 enabled: boolean;
 action?: AssetInterceptAction; // Global rules do not include this field (always intercept); task-level rules differentiate block/allow
 kind: AssetInterceptKind;
 pattern: string;
 note: string;
 builtin: boolean;
 created_at: string;
 updated_at: string;
}

export interface InterceptPending {
 decision_source?: "rule" | "model" | "unknown" | "";
 id: number;
 rule_id?: number;
 conversation_id?: number;
 task_id?: string;
 agent_name: string;
 tool_name: string;
 // eslint-disable-next-line @typescript-eslint/no-explicit-any
 tool_input: Record<string, any>;
 status: "pending" | "allowed" | "denied" | "timeout";
 reason: string; // Rule message or model determination reason(Model judgment with [Model] Prefix)
 decided_at?: string;
 created_at: string;
}

// JudgeConfig: Model Bottom-Line Approval(Only determined by the model when no interception rules hit)Global configuration.
export interface JudgeConfig {
 enabled: boolean;
 profile_id: number; // 0 = Follow active/default configuration
 prompt: string; // Judgment prompt;GET When not set, the backend will fill in the built-in template text.
 timeout_seconds: number; // Model call timeout
 fail_action: "allow" | "ask" | "deny"; // Fallback when model errors/timeout/unparseable.
 ask_timeout_seconds: number; // 'Model approval wait timeout after ask to human conversion',
 ask_timeout_action: "allow" | "deny"; // Default action after approval timeout
}

// JudgeUsage: Model Bottom-Line Approval(judge Channel)Cumulative token usage + Daily sequence for the past N days
export interface JudgeDayUsage {
 date: string; // YYYY-MM-DD (UTC)
 calls: number;
 input_tokens: number;
 output_tokens: number;
}
export interface JudgeUsage {
 calls: number;
 input_tokens: number;
 output_tokens: number;
 cache_read_tokens: number;
 cache_write_tokens: number;
 daily: JudgeDayUsage[];
}

export interface InterceptApprovalFilter {
 status?: InterceptPending["status"];
 decision_source?: "rule" | "model" | "unknown";
}

// InterceptApprovalRow enriches InterceptPending with conversation/task and rule context.
export interface InterceptApprovalRow extends InterceptPending {
 conv_title: string; // "" if no linked conversation
 conv_agent_key: string; // "" if no linked conversation
 rule_name: string; // "" if rule was deleted
}

// ── Asset sync (ScopeSentry Data source) ──────────────────────────────────────────────
export interface SSProject {
 id: string; // MongoDB ObjectID — used as filter.project
 name: string;
 logo?: string;
 AssetCount?: number;
 tag?: string;
}

export interface SSTask {
 id: string;
 name: string; // used as filter.task
 status?: number;
 progress?: number;
 creatTime?: string;
 endTime?: string;
}

// ConvTokenSummary — one conversation's token total (+ profile/date) for merging
// chat usage into the dashboard token stats. GET /api/tokens/conversations.
export interface ConvTokenSummary {
 llm_profile_id: number | null;
 created_at: string;
 input_tokens: number;
 output_tokens: number;
 cache_read_tokens: number;
 cache_write_tokens: number;
}

// ---- Command recording (Bash execution history) ----
export interface CommandRecord {
 id: number;
 exploration_id: number;
 worker: string;
 tool: string;
 command: string; // raw tool input (JSON)
 output: string;
 is_error: boolean;
 created_at: string;
}

// Single tool call statistics (/commands/stats); errors is the number of failures among them.
export interface ToolStat {
 tool: string;
 total: number;
 errors: number;
}

// ---- LLM recording ----
export interface LLMRecordItem {
 id: number;
 ts: string;
 model: string;
 profile_name: string;
 session_id: string;
 task_id: string;
 worker: string;
 latency_ms: number;
 input_tokens: number;
 output_tokens: number;
 cache_read: number;
 cache_write: number;
 status: string;
 error?: string;
}

export interface LLMRecordDetail extends LLMRecordItem {
 request_body: string;
 response_body: string;
 // provider Actual HTTP original received/sent: request is buildBody() The complete body sent (including tools)
 // schema), The response is in raw SSE frames. The request_body/response_body are normalized views.
 // Discarded tool schema and tool_use block. Old record is empty.
 raw_request?: string;
 raw_response?: string;
}

// One distinct task with its LLM-record count (task picker on the records page).
export interface LLMTask {
 task_id: string;
 count: number;
}

// The exact JSON sent to the review model, retained for all model verdicts.
export interface InterceptReviewInput {
 version: number;
 background?: {
 // worker_summary is retained only for immutable v2/v3 snapshots.
 source: "user_message" | "worker_summary";
 text: string;
 truncated?: boolean;
 };
 // Version 1 snapshots are immutable and remain readable in historical audits.
 task?: {
 task_id: number;
 description: string;
 goal: string;
 constraints: { id: number; kind: string; text: string; origin: string; created_at: number }[];
 truncated?: boolean;
 };
 working_directory?: string;
 worker_intent?: string;
 turn_input?: string;
 background_truncated?: boolean;
 // Legacy v1/v2 snapshots only; v3 never sends execution history.
 history?: {
 tool_use_id: string;
 tool: string;
 arguments_preview: string;
 result: string;
 status: "succeeded" | "failed";
 truncated?: boolean;
 }[];
 history_truncated?: boolean;
 correlation?: "exact" | "ambiguous" | "unavailable";
 tool_name: string;
 arguments: Record<string, unknown>;
}

// Immutable review snapshot plus separately recorded execution outcome.
export interface InterceptAudit {
 model_input?: InterceptReviewInput;
 model_input_digest?: string;
 run_id?: string;
 tool_use_id?: string;
 correlation: "exact" | "ambiguous" | "unavailable";
 input_digest: string;
 user_message: string;
 user_truncated?: boolean;
 context:
 | { kind: string; tool?: string; tool_use_id?: string; text: string; is_error?: boolean; truncated?: boolean }[]
 | null;
 context_truncated?: boolean;
 captured_at: string;
 model_fallback?: boolean;
 initial_action: "allow" | "ask" | "deny";
 initial_reason: string;
 effective_action?: "allow" | "deny";
 decision_reason?: string;
 rule_name?: string;
 config_digest?: string;
 profile_id?: number;
 execution_status: "not_started" | "not_executed" | "awaiting_result" | "succeeded" | "failed" | "unknown";
 output?: string;
 output_truncated?: boolean;
 execution_ended_at?: string;
}
export interface InterceptDetail extends InterceptApprovalRow {
 audit: InterceptAudit | null;
}

export type TrafficEvidenceRole = "baseline" | "proof" | "verification" | "supporting";
export interface TrafficEvidenceRef {
 traffic_id: string;
 role?: TrafficEvidenceRole;
 note?: string;
}
export interface TrafficEvidenceSnapshot {
 id: string;
 source_traffic_id: string;
 captured_at: number;
 url: string;
 method: string;
 status: number;
 content_type: string;
 req_head?: string;
 resp_head?: string;
 req_hash: string;
 resp_hash: string;
 req_len: number;
 resp_len: number;
}
export interface FindingTrafficBinding {
 id: string;
 finding_id: string;
 snapshot_id: string;
 role: TrafficEvidenceRole;
 note: string;
 position: number;
 created_at: string;
 snapshot: TrafficEvidenceSnapshot;
}
export interface FindingTraffic {
 finding_id: string;
 version: number;
 report_version: number;
 bindings: FindingTrafficBinding[];
}
export interface EvidenceBodyPreview {
 content: string;
 offset: number;
 total: number;
 next_offset: number;
 truncated: boolean;
 binary: boolean;
}
export interface FindingTrafficDetail {
 binding: FindingTrafficBinding;
 request: EvidenceBodyPreview;
 response: EvidenceBodyPreview;
}

/** GET /api/update/check —— Comparison result of the current version with the latest formal release on GitHub. */
export interface UpdateCheck {
 /** Current running version; development build as "dev" Or in the form of git describe with suffix. */
 current: string;
 /** Operational form. Under docker, swapping only affects the writable layer of the container; rebuilding the container will revert to the image version. */
 mode: "docker" | "binary";
 os: string;
 arch: string;
 repo: string;
 /** Whether there is a rollbackable previous version (artex.old). */
 has_backup: boolean;
 /** The conclusion of this startup's self-update and bootstrap (upgrade failed / rolled back etc.), empty if nothing happened. */
 boot_notice?: string;
 rolled_back?: boolean;
 /** When querying GitHub fails, provide the reason. At this point, none of the fields below will be available. */
 error?: string;
 latest?: string;
 notes?: string;
 html_url?: string;
 published_at?: string;
 /** Current platform's corresponding package name and whether this Release actually includes it. */
 asset?: string;
 asset_available?: boolean;
 size?: number;
 has_update?: boolean;
 /** Can the two version numbers be compared; development builds are false, disabling one-click update at this time. */
 comparable?: boolean;
 /** comparable Explanation when false. */
 reason?: string;
}

/** /api/update/stream Push update progress notification. */
export interface UpdateProgress {
 phase: "idle" | "downloading" | "verifying" | "extracting" | "staged" | "failed";
 /** Only meaningful during the download phase (0-100); otherwise, it's -1. */
 percent: number;
 message: string;
 version?: string;
 error?: string;
}

// Original execution selected from an approval, never submitted to the reviewer.
export interface InterceptExecution {
 conversation_id: number | null;
 task_id: string | null;
 session: string;
 seq: number;
 items: Activity[];
}
