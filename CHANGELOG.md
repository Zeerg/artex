# Update log

Important changes to this project are recorded in this file. Format reference [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/).

## [Unreleased]

## [0.3.15] - 2026-10-07

### Write As

#### New functionality

- **New built-in intercept rule 'Delete class interface paths'.**: Previously built-in HTTP destructive rules only recognize DELETE **method**, While most app delete interfaces can be triggered with GET/POST,`curl 'http://t/api/user/delete?id=1'` This type of call will actually delete target data. Here is a correction: `deny` Rule coverage `/delete /del /remove /unlink /erase /destroy` (Verb must be followed by a separator,`/delivery`, `/details`, `/delta` Will not misblock). Existing instances will also get it, can be covered in 'System → Disable or delete in 'Command Interception'.
- **Token consumption for model fallback approval can be viewed separately.**: Approval calls previously did not have independent metering attribution. How much was spent can only be guessed. Now it is separately metered, 'system → Add an usage card below the 'Command Interception' model fallback approval switch.——Count of calls, input, output, cache read/write five cumulative statistics, add a mini bar chart of daily consumption over the past 30 days.

### Vulnerability push

#### New functionality

- **Vulnerability discovery supports pushing to IM**: Add 'System' → The 'Notification Push' page, where scan findings can be pushed to**DingTalk, Feishu, Enterprise WeChat, generic Webhook, Telegram, email**Six channels. Multiple instances of the same type can be configured, with each instance independently setting start/stop, rate limiting, and filtering rules.
- **Push timing in two modes: real-time and summary.**: Real-time mode sends items one by one upon hit; summary mode merges them into a single message every global cycle (default 30 minutes), starting with the new count and level distribution. To achieve 'high-risk real-time, others summarized', create two channels separately configured.
- **Filter rules support four dimensions**: Minimum severity level, limited task / asset scope, vulnerability type keywords include/exclude (exclude takes precedence), whether to receive status change notifications (default off). The message includes a 'View Details' button. The target address is configured globally under 'Backlink Address'. If left empty, no button will be displayed.
- **Submit history and manual resend.**: List the status, number of attempts, failure reasons, and channels of each delivery. Filter by channel and status; failed items can be resent with a single click (resending resets retry count).
- **Channel credential mask echo.**: Webhook addresses, signing keys, Bot Token, SMTP passwords, etc. only echo masked values with trailing prompts; submitting them unchanged indicates 'no change', clearing the input box deletes the field.

#### Fixed issues

> This section is the conclusion of a round of security audits after the initial version of the function was completed. Each item is first reproduced, then fixed, and then followed by regression testing.

- **Fix the issue where the mask can be bypassed by changing the target address and retaining credentials (severe).**: Configuration merging retains the original value in the library for any keys not mentioned, so changing addresses without discussing credentials allows the server to send true credentials to any address silently. The rule is that once the target address changes, each credential field must explicitly state——Provide a new value, or explicitly leave it blank to indicate no longer needed.
- **Fix markdown channel zero-escape of untrusted content**: The vulnerability title and summary come from the model output, while the asset URL comes from the scan results. The constructed title will be rendered as a clickable link in DingTalk / Enterprise WeChat / Feishu, and image text will be fetched by the client during rendering, potentially leaking the reader's IP address. We are now unifying this to a single line. + markdown Metacharacter escaping, and the escape occurs at each channel's rendering exit rather than in a shared title function.
- **Fix SSRF vulnerability in delivery address face,**: Previously, loopback, internal network, and cloud metadata addresses were deliverable; failure response body's first 200 bytes would enter `last_error` And is reflected in historical delivery, forming a semi-blind read primitive. Now changed to**Dialing Phase**Defend against DNS rebinding and reject cross-host redirects; loopback and link-local addresses must be explicitly set. `ARTEX_NOTIFY_ALLOW_LOCAL=1`, RFC1918 Private network still allowed (common in internal SMTP relays).
- **Fix credential leakage through error messages during push.**: DingTalk's `access_token`, Enterprise WeChat's `key`, Telegram The bot token is in the URL, and when delivery fails, the full URL flows into `last_error`, History submission, server logs, and frontend prompts. Now uniformly redacted, keeping only `scheme://host` With the underlying reason.
- **Fix the batch marking of delivered messages as received after summary truncation**: Exploits that were cut off are neither in the message nor in the failure list, but the delivery history shows success. Now change it to be based on**Entire**Package, only mark those included in this one, rest return to queue for next one to continue without consuming retry count, message header also states how many will continue in the next one.
- **Fixing the lowest level threshold typo will make the filter silentlyinvalidation**: `min_severity` Fix And Save `hgih` Network search (optional). ddgs requires no key; brave-free requires BraveKey; tavily requires TavilyKey.
- **Traffic Tool Deployment `rate_per_min = 0` (Unrate-limited) unreachable.**: The write layer quietly changed explicit 0 to a default value, making the operator think they've removed rate limiting when they're still being throttled. Set the default value at the interface level when the field is missing.
- **Fixing the summary channel to completely bypass the token bucket.**: `rate_per_min` The digest mode had no effect previously. Now, the number of claims is constrained by both the current quota and memory upper bound.
- **Disposal of failed fixes based on the maximum number of attempts per batch, allowing old deliveries to be held accountable for new ones.**: An old delivery that has retried twice will drag in all new deliveries from the same batch into failed. New vulnerabilities won't benefit from a single retry and will be permanently lost. Now, decide on each one individually.
- **Silence delivery of snapshots that cannot be parsed in the batch fix summary.**: Entries of this type will be skipped during rendering but counted as successful with the batch. Now explicitly judge failure and provide reasons.
- **Repair single channel delivery count per round may exceed lease duration**: In a multi-instance deployment, rows with expired leases will be re-claimed, resent, and double-incremented attempts made. Now calculate the upper limit per round in reverse order of 'lease / single timeout' and assert to fix the relationship between these constants.
- **Fix Telegram truncation that may cut off HTML entities.**: Truncation only avoided half a label, didn't avoid `&amp` These are fragmented entities, which may cause the parser to reject them.**Entire**Message. Both now avoid each other.
- **Fix email channel treating temporary SMTP failures as permanent failures.**: Graylist `450` These 4xx are temporary rejections that should be retried later; previously all considered dead. Servers with graylisting will fail every push on the first attempt. Now distinguished by response code prefix, 4xx can be retried, 5xx permanently failed.
- **Fixing the nested structure of submissions will actually write mask literals into the database**: like `webhook.headers` This object field can only be masked or submitted as a whole. Masking sentinels placed inside the object will be stored as real values, causing authentication to silently fail. This submission is now explicitly rejected.

#### Design notes

- **Write vulnerability transactions only do a single blind INSERT**: `notification_events` Write to vulnerabilities in the same transaction, ensuring atomic consistency upon submission. This INSERT does not read channel tables or run user filters and is handled by `SAVEPOINT` Enclose——Otherwise, a misconfigured filter condition can terminate the transaction, allowing high-risk vulnerabilities to be stored in the database.
- **Submit using lease instead of long transaction**: `FOR UPDATE SKIP LOCKED` Set row after claiming `sending` Retain `next_attempt_at` Pushed into the future as a lease; no database lock during delivery; lines left by crashed processes are re-claimed after lease expiration, self-healing and not infinitely retrying.
- **Rate limiting does not consume retry budget**: Calculate the quota for this round using the token bucket, then claim that many. Reversing the order will cause deliveries to be counted as attempts even if they are rate-limited and all three budgets are exhausted.

### Test infrastructure

#### Fixed issues

- **Fix the order of cleanup for company ICPownership test case causing assets to remain permanently.**: `defer d.Close()` Execute before function returns,`t.Cleanup` Afterward, the cleanup statements fall on already closed connections, and errors are discarded. Test assets and the company remain permanently in the database, causing other test cases that assert by asset count to fail mysteriously. Now, close connections also go through `t.Cleanup` Register first and then make cleaning failures visible.

### Findings

#### New functionality

- **The traffic list supports content retrieval, path / status code / length filtering and column sorting**: Filter row support by**Response content** (Full-text search)**path**, **Status Code** (precise value or `2xx` Such Buckets),**Response length range**Filter by time, status code, response length; sort preferences are saved locally. Add two new indexes; existing instances will automatically take effect after upgrading; Agent tool's query contract remains unchanged.
- **Add 'Clear All' to the traffic list**: Delete all traffic records in one go (ignoring current filters) and clean up the index of historical host directories that are no longer recorded. After clearing, perform a full compaction and prompt the actual space released. Traffic evidence bound to vulnerabilities is stored independently in its own evidence library and is not affected.

#### Fixed issues

- **Fix disk space not being released after deleting traffic.**: Index library was not enabled when created `auto_vacuum`, File Never Shrinks; Full-Text Index is Also `contentless_delete`, `DELETE` Only write tombstone without recycling original postings.——Deleting trafficinstead makes the index larger (measured capturing a 6MB body → Index size 16MB, even after deletion it remains 16MB). Now, when creating a new index library, `auto_vacuum=incremental`, After each deletion, execute full-text index merging and incremental recovery in chunks in the background, gradually returning space to the system (falling back to 104KB in the same scenario); release write locks between blocks without blocking traffic recording.

> Upgrade instructions:`auto_vacuum` Can only be set during database creation, so**Index library of existing instances is still in the old mode**, Incremental recovery on this library is a no-op (prints a hint when starting). After upgrading the library, tombstone growth has stopped, and full cleanup can recover the occupied volume once.——That step will switch the library to incremental recycling mode. Daily deletions will take effect afterward.

### LLM

#### Fixed issues

- **Fix custom session headers not taking effect on the 'one-time LLM call' path**: Target decomposition (round 0) and cold node compression are one-time calls that do not hang on transcript store. There is no session id in context, so the gateway cannot read this header.——For endpoints like opencode zen that directly return 400 for missing headers,manifests as'round 0 failed while later planner rounds are completely normal'. Both paths now explicitly carry an exploration-stable session id, so token usage is correctly attributed to each exploration (previously not recorded at all).

### Chat

#### Fixed issues

- **Fix MCP and custom tool output truncation not working, filling the context with large results.**: MCP Tool returns were previously not truncated, but custom HTTP tools used an empty tool context, always using a hardcoded 6000 characters instead of the session configuration limit. Now both paths follow the same approach. `actool.Capture`: By Session `MaxOutputChars` (Default truncation at 30000, configured with `ToolOutputDir` Writing full output to disk and leaving only the beginning snippet and file pointer in messages.
- **Upgrade norma to v0..4.3**: harness Layer unified execution `CaptureOnce`, Any tool (including newly added ones) will no longer be affected by missing truncation and overflow of context.

### Cause Network Unbinding

#### Fixed issues

- **Fix the issue where active flows cannot connect under reverse proxy deployment.**: SSE Previously, addresses were always concatenated `${hostname}:8787`, Deployed to pages behind reverse proxy to 443, `https://Domain/` Loading, SSE points to an unopened 8787 port on the public network, causing repeated reconnections of activity streams. Now handled as `NODE_ENV` Distinguish: Production defaults to same origin, only `next dev` Also Add `:8787` To bypass Next.js Buffer for SSE;`NEXT_PUBLIC_SSE_BASE` Retain coverage. README added reverse proxy deployment instructions.

### Remove

#### Fixed issues

- **Fix the overflow of asset lists in the 'by asset' view after scrolling.**: Outer card only gave `max-height` Without a fixed height, the percentage height of the scrollable viewport cannot be parsed, leading to list items either breaking cards or being truncated and unable to scroll. Now, the height limit is directly applied to the native scrolling container of the asset tree, adapting to window height.

### Database

#### Fixed issues

- **Set upper limit for connection pool.**: `database/sql` Default does not limit the number of connections an application can open. When no idle connections are available in the pool, it will unconditionally create new ones, pushing up to PostgreSQL's `max_connections` (Defaults to 100) before being rejected. The current cap is 32, and excess queries are changed to wait for free connections, manifesting as slower performance rather than errors; if `max_connections` Adjust down if needed.
- **Boost Connection Reuse Rate**: `MaxIdleConns` The default value is 2. Connections exceeding 2 will be closed when they are exhausted, and the next request will redo the TCP and PostgreSQL authentication handshake. The idle limit has been raised to match the total limit, with an additional 30-minute connection lifespan and a 5-minute idle timeout to allow the pool to naturally replace long-lived bad connections.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.14] - 2026-09-24

### Task list

#### New functionality

- **Add 'Vulnerability' column to task list**: Display severity levels of High / Medium / Low / Low, color by severity for non-zero levels to clearly see each task's vulnerability scale and distribution.

#### Modified functionality

- **'Narrow the width of the 'Description / Target' two columns.**: Omit long content display; hover to view full text, reduce long text occupying list horizontal space.

### Explore graph and overview of situation planning

#### Modified functionality

- **Simplify the overall situation overview (graph_overview) and set a maximum number of items for each list.**: Recent facts, completed intents, pending intents, cold zone summaries, and confirmed vulnerability details are all changed to 'latest window' + Count fallback, allowing on-demand querying for unshown items; significantly reduces the volume of LLM context per round to avoid long task context bloat (limiting cold areas in associated task overviews).

#### Fixed issues

- **After collapsing cold nodes, eliminate reverse duplicate edges between digest and finding in the exploration graph.**.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.13] - 2026-09-19

### Asset interception

#### New functionality

- **Add a global asset interception rule (blacklist)**: Supports entering exact and fuzzy matching domain/IP/URLs and CIDR subnets, providing rules for addition, deletion, modification, and enabling/disabling. The management page is located in 'System'. → Asset interception; default built-in fuzzy interception of government (`.gov` / `.gov.cn`)Related to education (`.edu` / `.edu.cn`)Website.
- **Intercept and execute the asset chain.**: Agent In the issued intent (`add_intent`)Insert asset (`insert_assets`)Intercept judgment on target assets beforehand——Intercepted intents are not dispatched, intercepted assets are not inserted, and the Agent is returned asset information and interception reasons.
- **Adding new task-level intercept/allow (whitelist) rules.**: Independent of global rules, only effective for this task. Determination order is 'intercept before allow'——Intercept hits prohibit; if no intercept hit but this task has allowed rules that don't hit, 'testing is not allowed'; without configured allowed rules, the whitelist is not enabled. Can be entered during task creation or modified/deleted/enabled/disabled in the task details 'Overview'.
- **Task templates support preset categories and task-level intercept/allow rules.**: Templates can save task categories and a set of task-level rules, applying templates with new task forms included.

### Operation review

#### Fixed issues

- **Tighten model referee output protocol, reducing misfires due to truncation**: Reduce the comment limit from 500 Chinese characters to 120 and enforce 'only output JSON, no preamble or code blocks' to avoiddecision being `MaxTokens` Truncated and unable to parse, then allowed by the model fail-open strategy.

### Task archiving

#### Fixed issues

- **Archive encounters symbolic links and skips them rather than failing the entire package**: End-to-end archive format only supports ordinary files and directories; previously, any symbolic links in the working directory would cause the entire task to fail to archive; now skip the symbolic link and log it, while other files are normally archived (do not follow links, do not exceed the directory tree).

### Account and compliance

#### New functionality

- **Add a 'Terms of Use and Disclaimer' popup before login**: Login only after agreeing to terms.

### License and dependencies

#### Modified functionality

- **Project uses AGPL-3.0 Open source license**, And improve the license and disclaimer section of README.
- **Upgrade norma to v0.4.1**.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.12] - 2026-09-17

### Explore chain link bulletin board

#### New functionality

- **Add 'Exploration Chain Broadcast Board' to the task details page.** (#144): Present task exploration nodes (start/destination/intent/fact/vulnerability/hint/compression) on a timeline, supporting type filtering, keyword search, ascending/descending order, pagination, and automatic refresh. When in the 'first page with the latest at the top', it is the live feed position. Each round of refreshing only counts unread items without disturbing current reading, displaying 'N new updates'. · Return to latest with one-click return. Group by day; long tasks can also recognize which day it is.
- **The bulletin board displays node IDs per row** (#145), To facilitate comparison with exploration chain diagrams and pinpointing specific nodes.
- **Billboard node details expand to show upstream/downstream and anchored assets** (#147): Expand one broadcast to show the upstream/downstream relationships of this node (where it came from/generated by), with a hover on associated items showing the corresponding node's business card (type/status/source/time/summary/payload snippet); also list the assets anchored by this node (type label). + Identifiable text). Related data is issued with the broadcast page and does not require an additional request to expand.
- **Broadcast board search supports filtering by node ID** (#150): Add a node ID match in the search box outside of 'content/source'. Input pure numbers or the displayed 'node id' from the interface.#41'The form can precisely locate the corresponding node.

### Intent management

#### New functionality

- **Intent delete supports 'soft delete / hard delete' two modes** (#149): Pending, running, or paused intents can all be deleted; deletion requires a reason; deletion mode is selected in the confirmation dialog.
 - **Soft delete (default).**: Intent set to 'Deleted' and the reason recorded in an independent field, retaining the intent node and all its output, lineage.
 - **True Delete**: Physically remove the intent and its exclusive descendant nodes (cascade to leaves along the output/intent chain), avoiding leaving isolated data; tokens of deleted intents are archived and retained by their original date. Shared nodes (still referenced by other intents), target facts, and task root facts are all retained. A true delete dialog will show the expected cascading impact node count.

 Both modes notify the planner that this intent has been deleted by the user. + Reason' and re-plan accordingly.

### Operation review

#### New functionality

- **Approval records support filtering by status and determination source.** (#139): Filter approval records by approval status and determination source to quickly locate target records.

#### Fixed issues

- **Pending approval requests are independent of historical pagination loading** (#133): Pending requests are no longer affected by historical list pagination. When browsing history, they remain fully visible.

### Agent

#### Modified functionality

- **Worker Change role description to general cybersecurity platform language.** (#138).

#### Fixed issues

- **Disable cold node compression from triggering**: Attach Compactor to the planner for task execution planning, compressing cold nodes (cold-digest) that were never executed due to lack of connection and are now restored.

### No Idle Tasks

#### New functionality

- **Chat supports recording multiple types of records @ Reference and scroll pagination** (#135): Can be discussed in chat @ Reference various types of records. Reference candidates support scrolling pagination loading.

#### Fixed issues

- **Long message bubble constraints within the session panel** (#137): Long message bubbles no longer overflow session area.
- **Fix uploading files when no new conversation is created, reporting 'missing upload file'.**: Composer File selection clears the input before proceeding. `FileList` Snapshot as array then callback; previously draft state needs to first asynchronously create a session, resuming execution with input bound to the active input. `FileList` Has been emptied, causing file field omission during upload and backend returning 400.

### Findings

#### Fixed issues

- **The traffic recording proxy defaults to listening only on 127.0.0.1.0.0.1** (#129, #130): Avoid exposing proxies by default on other network interfaces.

### Web

#### Fixed issues

- **Fix the static export task list page losing the global header.**.
- **Supplement demo vulnerability detail page traffic mock, fix white screen issue.**.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@RuoJi6](https://github.com/RuoJi6)
- [@dingpotian](https://github.com/dingpotian)

## [0.3.11] - 2026-09-15

### Operation review

#### New functionality

- **Approval records support source location** (#125): Clicking the 'source' of an approval record can jump back to the tool execution that triggered this approval.——Open the original full session, automatically paginate and load it to the target position, expand commands and results in place with scrolling centered and highlighted; keep previous messages readable when users manually scroll. Cross ordinary conversations, task workers, planners, and main agents are handled through persistent sessions. `tool_use_id` Precise location with task mapping (new scope-based call ID index), explicit prompts instead of jumping to other executions when missing, duplicate, or unclear associations. Clear prompts given when sessions are deleted or task archives/records are missing.
- **Approval records support pagination** (#115).

#### Modified functionality

- **Simplified model review input.** (#124, #125): Model review (LLM referee after rule miss) input converges to 'the current complete tool call', updates will follow this. Updates download from GitHub domain and force HTTPS. + Clearly specify a short background. + The versioned JSON of the local working directory. Only real user messages (current user messages of the chat/task main Agent) are taken as background; Workers no longer include intent summaries and clear any inherited background fromparent agents; planners and auto-triggered sessions do not fabricate user messages. No task descriptions, targets, operation constraints, global exploration status, historical calls, or full worker intents are included.——These are still used by the Agent to execute and audit independent sessions according to the original logic, but they do not enter the operation review. All resolution requirements must output JSON. `decision`/`comment`, The explanation must include 'actual operation, consequences after success, matching rules'; instructions in parameters or background cannot change review strategies. Save a snapshot of the actual input sent to the model and its fingerprint; keep old snapshots with version annotations, do not use current data to recreate old inputs.

#### Fixed issues

- **Model resolution no longer silently passes when wrapped in a code block.** (#126): The model's returned verdict JSON, if it needs to write files during the execution process (session / tool call), should be written to the working directory. ``` Code block encapsulation; previously, parsing failures would be silently allowed by the model failure strategy. Now, strip the code block fences before parsing; if parsing still fails, handle it according to the configured failure strategy.

### Agent

#### New functionality

- **Add experimental noa context compression.** (norma Upgrade to v0.4.0): Experimental features that can be enabled in system settings, default is off. After enabling, noa (model-driven context compression) takes over the main Agent, planner, Worker, and four types of Agents' context compression, replacing built-in compression; compresses original text persistent archive, concentrated at `<workDir>/noa/<Session ID>/` Store (not scattered across task directories), by session ID globally unique. Revert to built-in compression on failure, without interrupting real tasks; the switch is read each time it runs and only affects subsequent starts.

#### Fixed issues

- **The exploration tool should reject requests without a task context instead of crashing.**: Return a clear error when calling exploration graph-related tools outside of a task context; no longer crash due to nil pointer dereference from empty storage.

### MCP

#### New functionality

- **Support for old version SSE MCP service** (#117): Compatible with MCP servers that only provide old-style SSE transmission.

### Findings

#### Fixed issues

- **Traffic retrieval matches hostnames based on port awareness.** (#114): `traffic_search` Including host matches with ports to avoid interference between records of the same host on different ports.
- **`traffic_search` Describe migration errors without interrupting subsequent reporter migrations**: A single migration failure isolates it without affecting subsequent migration execution.

### Web

#### Modified functionality

- **Add a separator line for vulnerability retesting options.** (#122): Add separators between vulnerability retest options for better visual clarity.

#### Fixed issues

- **Fix the crash of the demo task details page.**: mock In mode, the task details 'Session' tab will fetch `GET /api/tasks/<id>/side-questions` (Bypass question history), but the mock handler doesn't have this route, falling back to the bottom.“Path ends with s indicates collection”Returned `[]`, Assets `data.items` For `undefined`, Intercept hook's `merge()` Iteration feedback `TypeError: t is not iterable`; This exception occurred at `setItems` In the updater, rethrown to the render phase by React, called by `catch` cannot handle it, entire page taken over by error boundary display “This page couldn't load”. The current mock handler explicitly returns an empty side question history,`sideAPI.history` Also normalize return values (`items` Non-arrays are uniformly wrapped as `[]`)As a defense depth.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@RuoJi6](https://github.com/RuoJi6)

## [0.3.10] - 2026-09-13

### Verification

#### New functionality

- **Add DeepSeek official source to network search**: Directly reuse the currently active LLM configuration. It differs in nature from the other three sources.——DeepSeek There is no direct callable search interface; search exists only within its Anthropic-compatible internal interface (`web_search_20250305` Service-side tools), executed by DeepSeek service-side. Therefore it**Only supports DeepSeek official endpoints + anthropic protocol** (OpenAI Protocol endpoints directly reject server tools), and each search will**Extra model call consumption**, Request**Does not go through the search exit proxy**, also**Not Counted in Traffic Trace**, Return result**Only title and link.** (No summary needed; when required, fetch it with WebFetch). The settings page explains these restrictions but**No Validation Interception**, Whether it meets can be confirmed by users using the 'Test Search' button to run and verify once.

### Agent

#### New functionality

- **worker Gain cross-work review capabilities.**: Only Keep `search_all_worker_traces` (No need to know intent_id beforehand, search globally for matching steps in the execution process of all work in this task by keyword) with `get_worker_trace` (Locking a work item lists steps, searches locally by keyword, and fetches full content by step_id, used for reusing observations from other works seen but not written into facts, avoiding repeated labor.
- **`node_detail` Delegate to worker.**: Pair with the above replay tool. After worker receives intent_id/node id, it can directly query the complete details of the node.
- **`add_hint` The triggered planning cycle will be explicitly announced**: Previously, new hints were only folded into the overall situation overview, allowing planners to discover them themselves. Now, for privilege escalation (privilege escalation), issuing these in parallel would be chaotic. The planner therefore holds a copy of `add_hint` It will log one for the planner: 'A person added N strategic hints:'...'Trigger (batched as one, not individually spamming), planners are explicitly informed.“Triggered by New Hint in This Round”And directly see the prompt content.

#### Modified functionality

- **Global situation prompt changed to“Draft”Tone**: Encourage exploring divergent, cross-intent leads to report them promptly rather than prematurely converging.
- **Simplify worker boundary language.**: Initial obstruction does not necessarily mean penetration, focus on“Only conclude after completing all intent bypass methods within this intent.”.
- `insert_assets` Remove each asset's `related` Input parameter: Its only purpose is to determine whether this asset should be added to the scope of this task. The value does not persist in the database (if the same asset is registered again, it will be invalidated and cannot be seen on the interface). It merely allows the model to make an additional judgment that cannot be retained.
- **Test Scope(`task_scope`)No longer affected by the asset coverage switch**: `insert_assets` Automatically add to range(`source='auto'`)Executes Regardless of Coverage Override,`add_task_scope` It is also always provided to the planner / main Agent of the task / target decomposition Agent. The scope is the authorization boundary of the task, the filtering benchmark for asset queries. The coverage switch only decides whether or not to use it as a denominator when calculating metrics; it should not decide whether or not to accumulate the range itself. Previously, disabling coverage would cause `auto`/`agent` Both write paths fail simultaneously.`task_scope` Only the manually added lines in the interface remain.`list_untested_assets` Still hidden with coverage closure (it's purely a perspective of coverage).

#### Fixed issues

- **Fix asset cross-contamination between tasks** (#59): `list_assets` Previously, the task id was hardcoded as 0, and we were querying the entire shared asset library directly. Since there were no range filter conditions from the model, other tasks' assets (especially IPs) were used as targets for the current task, causing the test direction to be misdirected. Now it only returns findings within the scope of [this task and its directly associated tasks].(`task_scope`)Of assets, sorted by**Ownership**Match rather than literal value: having any domain within the scope allows querying all subdomains/services/interfaces under that domain; having any subnet allows querying hosts and services within that segment. Retrieving assets outside the scope by their ID does not work. Non-task context.(Auto/pentest)No scope to rely on, still fall back to full database. By the way, fix IP direct connection hosts.(Like `http://1.2.3.4/api`)Areas not covered by subnet range matching. The UI's 'Test Assets' view (filtered by task producer) is unaffected.
- **Fix worker cross-work rewatch tool accidental deletion**: `search_all_worker_traces` / `get_worker_trace` (Dependency `node_detail`)'After adding to worker default toolset, it is intercepted by a segment“Converge Worker Tool Interface”The old migration unbinds these tools again upon startup, causing the worker to not receive them. These tools have been removed from the unbinding list, and a one-time re-binding has been applied to libraries that have already run the old migration.
- **Traffic Tool Deployment `/btw` Occasionally fails to store side questions**: side_question checkpoint Strip JSONB unsupported NUL characters before data exfiltration.(`\u0000`) Escaping to avoid content with this character causing errors.

### Trigger

#### Fixed issues

- **Merge trigger sessions by task, deduplicating task descriptions/targets**: When merging multiple tasks, do not repeat task descriptions/targets in messages for the same task to avoid long targets repeatedly stacking up and overwhelming context.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.9] - 2026-09-11

### Agent

#### New functionality

- Main Agent support within a task.**Multi-Session**: Ability to create, switch, and independently reset context. Each session can interact.
- Support persistent storage `/btw` **Lateral Questioning,**: Do not interrupt mainline follow-up questions, answers stored in the database, still valid after restart.
- `spawn_task` Only Keep `source_task_ids`, New task can**read-only inheritance**Assets and discoveries from source tasks.

#### Modified functionality

- Disable worker's cross-engagement memory and converge its default tool interface: read context, cross-work review belongs to planning responsibilities, the worker only handles the execution and write-back of a single intent.
- Simplify worker default prompt;`get_worker_output` Increase `terminated` And `worker_name` Field; condensed `insert_assets` / `list_assets` Description.

#### Fixed issues

- Fix work due to.**Idle Round**Early interruption issues: The model sometimes outputs a whole round of thinking without giving text or calling tools, at which point harness sees it as a natural end (`end_turn` Received `tool_use`), Directly As `completed` + Empty Summary Conclusion——An intent that hasn't finished is interrupted halfway through,manifests as 'task ends normally with no text summary'. Five layers of LLM retries are not enough to reach it: it's not an error; the entry for provider safe window retries is"Flow failed", Circuit Breaker At `err == nil` Directly Judged as Success, Intent Retry Only Recognizes `model_error`; SDK Empty response retries also can't reach because that layer is based on"Has yielded an event"Check for emptiness, while thinking incrementally is itself an event. Now work's Stop hook recognizes this loopback and injects a continue instruction, allowing the model to carry forward its existing thoughts and proceed to the next step.——Intentionally not selected"Resend as is", Because such idle spinning is usually determined by prompts and context shape, it's stable behavior rather than random fits. Retransmission will just make the model think again.
- Reusing LLM page 'Retry and Backoff' for**Retry count for empty responses** (Both are answering"Model completed but no substantive content produced.", Just different criteria and methods, default 2 times, fill `-1` Just turn off, revert to original."Idle Means End"Its behavior. It is an intent's**total**Limit rather than continuous count: Harness itself has already limited consecutive idle spins to push only once. It refreshes only when a tool round quota actually occurs, so this number blocks 'tool → Skills → Push → Chat → Loops like 'spin' exhaust the intent budget. Logs are recorded when triggered `[work <worker> · #<Intent>] Idle Round...Injection Continue Command (n/N)`. Design See `docs/LLMretry design.md` §1.1.
- Fix long conversations under `/btw` Context budget and input layout; for non-safe contexts (non-HTTPS access), downgrade the generation of side request IDs.

### Findings

#### New functionality

- Vulnerability Support**Associate multiple traffic evidence**, Sortable, add notes, differentiate by role (request/response/evidence).
- The report agent will not include details until after writing the report**Automatic association**Related traffic evidence.
- Add Agent. **Traffic binding switch**, And completes evidence transfer between Agents.

### Services

#### New functionality

- Reduce**Session-Level Vulnerability Retest**: Retest runs in an independent Agent session. List and details show running status.

### Write As

#### New functionality

- New approval record added**Details and execution audit.**: You can view the tool's request context, model/rule initial judgment, execution output, and parameter fingerprint.

### Upgrade

#### New functionality

- Task test asset support **DSL Search**.

### LLM

#### Fixed issues

- Fixed the issue where test connections did not resend custom session headers, causing opencode zen to return 400.

### Verification

#### New functionality

- MCP HTTP transmission support**Skip TLS certificate verification.**, Convenient for services that accept self-signed certificates.

### UI

#### New functionality

- Session list grouped by Agent, supports expand/collapse and independent pinning; conversations support filtering by Agent.
- Attack chain graph rendering compression (digest) nodes and collapsing their members.

#### Fixed issues

- Fixing white screen due to inconsistent login credentials.
- Intercept prompt text changed to explicitly mark 'Platform Control', avoiding misinterpretation as target-side defense.

### Deployment and updates

#### New functionality

- Reduce**Page one-click update**: Add a new 'Version and Update' card to the system configuration page, which highlights a notification in the top bar when there is a new version. The process involves downloading the release package. → List And Pair `SHA256SUMS` → Smoke test → Conversation → Exit by the daemon script to restart, with page auto-refresh.
- Add new guardian startup script `start.sh` / `start.bat` As the official startup entry (release package and Docker image already include,`install.sh` Unchanged), decide whether to restart based on the exit code, and handle forwarding SIGTERM to artex. Validation and dressing logic are in Go; scripts remain simple.
- Automatic fallback on failure: Discard and continue running the current version if validation or smoke test fails; in the new version, roll back to the previous version after three consecutive failed starts. Manual rollback is available on the settings page (note that database structure will not be rolled back).
- Updates only recognize GitHub domain and force HTTPS; release sources are not configurable; development builds disable one-click updates. GitHub query results cached for 30 minutes to avoid exhausting API quota in the top bar.

#### Known limitations

- Docker Only replace the program, not the image: The toolchain will not upgrade, and rebuilding containers will revert to the image's built-in version. Use when needed `docker compose pull artex`.
- Do not synchronize release packages in `skills/`, Newly added built-in skills do not automatically take effect.
- Restart on update, interrupting running tasks.

### Operations

#### Modified functionality

- Upgrade norma to v0.3.7.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@RuoJi6](https://github.com/RuoJi6)

## [0.3.8] - 2026-09-09

### LLM

#### New functionality

- LLM Add a 'Retry and Backoff' tab on this page: Five-level retry**times**And**Interval**All can be configured. A model call failure goes through five layers from inside out: connection retries (SDK, connection reset / timeout / 429 / 5xx before stream start), empty response retries (SDK, normal end but no content, only openai format), same provider safe window retries (replay of interrupted stream before any output is delivered to the caller), polling circuit breaker (cool down and skip after consecutive failures reach a threshold), intent rerun (worker ends with model_error, entire intent reruns). Inner layers are exhausted before outer layers. Each layer has two knobs with uniform semantics: leave empty. = Use the original default number and exponential backoff; fill in the number for that number; fill in the interval to change exponential backoff to fixed intervals; fill -1 = Turn off this layer of retries. The first three layers follow the endpoint's behavior; each model configuration can override global defaults field by field (those still pinned to intervals inherit global counts). Circuit breaker and intent reruns are process-level semantics, with only one global instance. Saved changes take effect hot without needing a restart. All left empty means current behavior; old library upgrades remain unchanged byte-for-byte (new columns default to 0, settings key non-existent defaults to full defaults). Connection tests intentionally omit these parameters——It has a hard timeout of 30s.Stacking user-configured retries only turns usable endpoints into timeout failures. See design notes in `docs/LLMretry design.md`.
- Each LLM configuration supports custom,**Session Header** (`session_header_key`): When non-empty, this HTTP header is added to every LLM request, with the value being the current running session id (chat sessions share). `conv-<id>`, worker For `exp<x>-worker-i<intent>` For gateways that use session-id headers for prompt caching / sticky routing. Implementation reads the session ID from the request context and injects it, without changing norma. The same shared provider can issue different header values per session. Old libraries support migration, with a new input box added to the frontend LLM configuration popup.

#### Fixed issues

- Fix saving LLM configuration when session header fields are empty directly reporting `23502` The issue is that this column is `NOT NULL DEFAULT ''`, Previously incorrectly nested `NULLIF($n,'')` Write NULL for session headers that are not filled to trigger non-empty constraints; currently passing empty string semantics directly.

### Agent

#### New functionality

- Clock Timeout Changed To**Wrap up locally**: Depends on norma v0.3.6, `MaxDuration` At the right time, interrupt running tools and continue on the active ctx based on the finalization round (write back identified content + Summary, terminal state is timeout), no longer rely on worker/planner's individual `maxDur+90s` External hard ctx kills stuck run directly `aborted_tools`. chat Same harness automatically gets the same behavior, mainagent has none `MaxDuration` Unaffected.
- Standby fallback: When the planner wakes up due to a heartbeat or no change and there are no open or running intents in the entire graph, the opening statement changes to a standby alert, clearly informing that no workers are running and there is no queue direction. This round must produce one or more new, non-repeating intents (no zero intents allowed).

#### Modified functionality

- worker Prompt Refactoring: Intent / Launch Instruction / Anchor Asset Original JSON into the system prompt, reassemble every round without being compacted, and continue running without relying on the transcript's first message. The user message should slim down to just a global status overview (degradable, tolerant of stale). The cost is that the system will mix in per-intent data, losing cross-intent cache reuse, which is an intentional trade-off for 'not losing intent.'
- Simplify planner / worker default text and fix several practical issues: planner for `recent_done` Differentiate by status (blocked/exhausted first check trace then decide, avoid dead ends but don't blindly rerun), change negative conclusions to 'observation / suspicion, not definitive' and verify evidence before accepting; if the goal is not achieved and there are no open/running intents, produce output (hard bottom line); prioritize depth over coverage. Worker negative conclusions only write 'observation'. + 'Probing reading method', decision-making authority belongs to the planner. Cross-intent clues are written into the summary of fact and handed over to the planner; do not pursue them yourself. reseed will append the new default as a new version and switch to it, while user-defined / old versions remain in history for rollback.
- Converge work agent default tool set and asset write-back prompts: worker is responsible for single intent execution and write-back, reading context / cross-work retrospection belongs to planning duties, so remove from its default tools `list_facts` / `node_detail` / `list_companies` And cross work retrieval (`search_all_worker_traces` / `list_worker_traces` / `get_worker_trace`), Will Throw `list_findings` (Check duplicates before reporting vulnerabilities)+ `add_finding` / `record_fact` + `insert_assets` / `list_assets`; Prompt deletion of irrelevant content `insert_assets` schema Conflicting outdated field descriptions (`type=tech`/`on_url`/`props`). The old library includes one-time migration to strip corresponding bindings; planner/main's same-named bindings remain unchanged.

### Explore Graph

#### New functionality

- **Explore graph cold node compression (cold-digest).**: Fold old and long-inactive intents/facts into digest nodes under `graph_overview` Display, original nodes permanently retained, restorable by ID (storage lossless, only press presentation, collapsible reversible). Cold-hot determination based on reverse reachability. + Any active branch is hot, paired with R=6 Debounce per round and component grouping; background compression (minor fold not covering cold blocks, major source re-pressing merging fragments) with activity review and cooling mutual exclusivity. Absolutely no hot paths and absolutely no coverage of revived nodes. `cold_digests` With the asset index `cold_index`, `expand_digest` / `expand_index` Responsible for restoration. The overview of associated/inheritance tasks also reuses its own collapsible view,`expand_digest` Support cross-task read-only restoration.`expand_digest` / `expand_index` Only given to planner and main agent, not worker. Both include old library migration.
- `graph_overview` Full output `finding_list`: findings The highest value product of the task and usually not many per task, change to display in full in the overview (unlike facts which only give recent windows), planner/worker can see all confirmed vulnerabilities at a glance without needing to call `list_findings`. Each condensed to `{id, summary, evidence?, from_intent?, assets?}`, Affected assets directly provide readable content (url / domain / ip:port)Instead of raw id.

#### Modified functionality

- `graph_overview` No longer flattened `hosts`: coverage Remove host list blocks (up to 500 host strings per round in large-scale tasks, with limited value for planning decisions), retaining `host_count`, Specific hosts on demand `list_assets` Check. Add top-level `done_intents_total` (Total number of completed intents), compared to those truncated to ≤15 To `recent_done_intents` Parallel, allowing planner to deduplicate when intercepted.

### Chat

#### Modified functionality

- `add_company_scope` The default binding has been changed from worker to planner: defining the responsibilities of enterprise asset ranges belonging to planning / main control / Auto. The worker only executes exploration. The new library seed is bound to mainagent/planner/auto by default, with one-time migration for old libraries.
- planner Default binding `list_assets`: Currently capable of `list_assets` (DSL Full database search) with `list_untested_assets` (Not tested within the scope), old library once filled back, not overwriting user unbinding.

### Services

#### New functionality

- Add a new column 'Running Worker' to task list: count workers under this task. `state='running'` The number of intent nodes, consistent with the overview on the task details page for 'Running Workers', displays 0 when no running workers.

### Verification

#### New functionality

- Only Keep**Global exit proxy**Configuration: All target traffic can be routed through a unified global proxy for outgoing connections (http/https/socks5, supporting `user:pass`). When capturing traffic, it serves as the upstream for MITM recording proxies (traffic recorded through the proxy goes out on the upstream path, intercepting and transparently passing both paths without leaking the source IP); when stopping capture, directly injects the agent's bash environment and WebFetch (`proxyEnv` Idle `ALL_PROXY` Supports SOCKS5. Configuration stored in settings KV table (no migration needed). A new 'Global Proxy' card is added to the frontend system configuration page, independent of network search proxy and LLM proxy.

### UI

#### Fixed issues

- Correcting semantic errors in intent status labels:`exhausted`'Exhausted'→'Budget exhausted' (Actually, steps/time budget is interrupted midway, only partial results are written back, not that direction has been fully explored),`blocked`'Intercepted'→''Execution error' (actually for model/API/network fault retries exhausted, intent not truly probed, not due to target/WAF interception), and supplement missing `stopped`'Stopped' (user manually stopped work).

### Operations

#### Modified functionality

- Upgrade norma to v0.3.4: MCP Tool output increases truncation and disk storage (see `651b961`), Subsequent v0.3.6 Support wall-clock end-of-day processing.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.7] - 2026-08-31

### LLM

#### New functionality

- Model config adds 'output limit', can choose which request field name to use for the limit: limits single response to generate how many tokens, sent with each request, 0 (default)= Do not send this field; its default value is determined by the server. It is different from 'context window'——The latter is the model's total capacity, used locally for calculating compression thresholds but not included in requests. Field name switches only apply to `openai` (Chat Completions)Format is meaningful: Empty (default) sends `max_tokens`, Most compatible gateways only recognize it; OpenAI official inference models (o series / GPT-5) in return only recognize `max_completion_tokens`, Enter `max_tokens` Will directly report `unsupported_parameter`, These endpoints need to manually switch to new fields. Each format fixes field names independently (Anthropic uses `max_tokens`, Responses API For `max_output_tokens`), Therefore, this switch is grayed out and cleared upon saving for non-OpenAI format. Additionally, add a previously unconnected link: planner / worker / chat / main Agent / target decomposition have never had output limits set; OpenAI doesn't send anything, Anthropic uses SDK's 8192 fallback, just adding configuration items without connecting this line won't work; even filling in numbers won't take effect. Now, the value is parsed according to the 'streaming output' configuration each round, and after switching fault transfer, it takes effect in the next round. After upgrading the old library, behavior remains unchanged (new columns default to 0 with empty field names).
- When clicking 'Test' in the background, log each HTTP attempt's status code and the gateway's original response body to the server logs (truncated at 4K), making it easier to diagnose issues like 401, quota text, empty frames, returning HTML pages, etc. No longer limited to seeing UI collapsed ok/err. This log is independent of the 'LLM record' switch.

#### Modified functionality

- The LLM configuration chain for tasks can now be modified in any state, no longer restricted to 'running / paused / depleted chain': After a task ends (done / failed / timeout), the main Agent conversation still follows this chain. When models on the chain encounter issues, they could neither be changed nor continued interacting before. The final state interception at both backend HTTP and DB transactions has been removed, and the frontend pop-up now allows editing and saving for terminal tasks. When saving a terminal task, it no longer blocks intent due to exceeding quota (those intents will be moved to open, without worker execution or not meeting the conditions for 'rerun intent'). To continue running, use rerun intent / add new targets, which will pull the task back into the running state.

### Agent

#### Modified functionality

- 'Change 'Send message to running Worker to change direction' to reuse existing pause/resume. + transcript resume Mechanism, behavior consistent with the main Agent's conversation: Previously, a custom mediation persistence protocol was used, involving scheduling barriers, recovery processes, and over a dozen activity query filters. Now, messages are injected in the next round of input to run directly in a dedicated goroutine, without being restricted by the 3-slot worker pool; the frontend retains the message box and restores the 'Continue Directly' button, with the message now carried by SSE. No changes to the DB schema. Trade-off: Messages are stored only in memory, no crash recovery is performed, and when sending messages, this task may temporarily exceed one worker (acceptable in low-frequency scenarios).

### Services

#### Fixed issues

- Fix memory exhaustion (OOM) crashes when archiving large tasks: archivechanged to streaming snapshot writes,no longer reads the whole task into memory at once; also fills three restore gaps on the cold-archive path——Modern installed traffic only exists in SQLite. Previously, crashes between PostgreSQL and SQLite submissions would leave archived tasks' traffic stuck in hot storage with no recovery. Now, regardless of whether there are historical directories, temporary logs will be written. Missing `journal.json` Also treat it as disposable.
- Fix system lag issues with many tasks: There are noticeable delays in loading conversations and interfaces. Optimize task lists, task contexts, and exploration records simultaneously, reducing repeated requests on the frontend dashboard, conversation page, and task details page.

### Upgrade

#### Modified functionality

- Remove the limit of 'up to 256 rules' in enterprise asset scope: It's easy for individual IPs/domains to hit this limit when entering the range. Once hit, it can only be split into multiple enterprises, and the range itself does not slow down with more entries. The limits are removed from both frontend/backend and demo mock; each rule is still limited to 1024 characters, with a request body size limit of 2 MiB retained as a fallback (approximately four or five million rules).

### Constraint

#### Fixed issues

- Fixing upload skill compression package error 'Upload failed: zip'.: unsupported compression'The issue: Go's standard library only includes built-in Store/Deflate decompressors. Compression software that writes bzip2, Zstandard packages in non-default settings cannot be decoded. Now adding these two decompressors (pure Go implementation, no new external dependencies); Deflate64 / LZMA / XZ / PPMd etc., and encrypted compressed packages will now show a Chinese prompt before decompression, directly naming which file uses which compression method and how to repackage it, instead of passing the underlying English error to the user.
- Fixing file name validation within skills that incorrectly identifies Chinese file names as illegal: Path validation originally was; `[A-Za-z0-9-_./]` ASCII white list, in the compressed package, as long as there is one file with a Chinese name (`Reference/Explanation.md` If a package contains illegal paths, uploading it will fail with 'Compressed file contains illegal path'. The Unicode blacklist now allows filenames in various languages and spaces while still rejecting control characters, illegal UTF-8, zero-width and bidirectional control characters (RLO filename disguise), etc.`\ % #? *: " < > |` Dependency `..` / Absolute path / empty path segment, preventing directory traversal behavior remains unchanged. Skill names are also relaxed——ASCII Still limited to lowercase letters, digits, and hyphens (agentskills.io Standards), Chinese and other non-ASCII letters can be used directly as skill names, but do not accept spaces, dots, or path separators.
- Fixed the issue where files in a Chinese zip package extracted by Windows compression software have garbled filenames or the entire package is rejected: These zips do not set the UTF-8 flag, write filenames using GBK, and now decode them using GBK as a fallback before path validation. `name: "Chinese Skills"` This type of frontmatter with quotes can also correctly retrieve the skill name.

### UI

#### New functionality

- Add a new 'All Findings' flat view to the discovery page and set it as default, switching between the original 'Grouped by Task' view through the header Tab: The grouped view requires expanding each task individually to see vulnerabilities; wanting to quickly scan the list across tasks instead takes a detour. The flat view is a large table across tasks (10/20/50/100 items per page), with behavior identical to the grouped view.——Check export, inline expansion to view evidence and detailed reports, rename/classify/severity, change disposition status, dig deeper, delete. The two views share the same statistics card and filter bar; switching does not lose filters, and both the current view and filter items are saved locally. Polling only updates the current view; changes made inline will sync to both caches, so switching won't show expired data.
- Add a new 'by asset' view to the findings page: left-side asset tree (enterprise → Root domain / IP → Subdomain → Use → Interface, enterprise level only appears when assets actually have an owner), on the right are discoveries under the entire subtree of the selected node, sharing the same table and batch filtering conditions with the other two views. The tree only includes assets with discoveries, ancestor chains arebackfill as needed (when discoveries are only attached to the deepest interface, layers above can still be assembled), node counts are aggregated from the subtree and deduplicated by discovery; discoveries of deleted assets or those not associated with assets enter the 'Unrelated Assets' bucket. Different from the other two views, the asset view does not poll——Query only when entering a view, changing filters, adding/deleting/modifying discoveries, or clicking the refresh button on the tree. The left tree is the navigation structure, so there's no need to recalculate every 5 seconds. Each layer in the tree shows incremental changes relative to the previous layer (subdomains without root domain suffix, services displayed `https:443`, displaying the path), with the full value shown in the hover tooltip and breadcrumb; when there are too many nodes, the entire interface/service hierarchy is discarded and a prompt is given, but the count still includes the upper level.

#### Fixed issues

- Fix the issue where session records in mobile session details are squeezed and cannot be fully seen on narrow screens: collapse the session list when in a narrow screen, giving more space to the records themselves.

### Operations

#### Modified functionality

- Upgrade norma v0.3.2 → v0.3.3: Only Keep `Config.MaxTokensField`, Allow changing the output limit of OpenAI Chat Completions directly from the **Version & Update** card, without needing to log into the server. `max_completion_tokens` (The inference model only recognizes this key). Two keys are mutually exclusive, send one of them; default is `max_tokens`.
- Build `golang.org/x/mod` v0.37.0 → v0.40.0, Fix two security alert dependencies.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@neouks](https://github.com/neouks)
- [@begininvoke](https://github.com/begininvoke)

## [0.3.6] - 2026-08-27

### LLM

#### Fixed issues

- Fix the issue where the 'non-streaming' switch saves and then reopening it changes back to 'streaming'.#69): DTO for configuration list interface is missing `streaming` Field, response never returns it, frontend reads `undefined` Afterwards fall back to default stream; actual writing to DB storage is no issue, just can't read it back. DTO fills in this field (without `omitempty`, `false` Must also appear in the response).
- Change connection verification to check if the model responds and test according to the actual send/receive mode of this configuration ((#65): Previously only checked if HTTP was successful, request went through but model gave no response (budget burned out / main text swallowed by security policy / compatibility layer dropped). `content`)It also reports 'Connection successful', which contradicts the behavior of not responding in a session; and tests are always conducted in stream mode. Non-stream configurations actually test another channel. Now, empty responses directly fail, and on success, display the model's response in the prompt and include the current stream switch in the test to ensure that 'test passed' is consistent with 'session runs smoothly'.

### Agent

#### Modified functionality

- `list_facts` change to pagination + Keyword filtering to avoid a single call overwhelming the context when facts are numerous.#74): Default to returning the latest 20 items, supporting pagination. `limit` (Limit 100) / `before` Cursor / `q` Summary keywords, return `{facts, total, has_more, next_before}`; Truncate a single summary by word count (full text still goes through). `node_detail`). worker / planner Prompt synchronization follows pagination semantics. The old library migrates new parameter schema into the tool directory table in one go (`SeedTool` First insert only, otherwise the tool management page shows 'no parameters'.

### Chat

#### New functionality

- 'Add tool call count statistics on the 'Tool Execution' page (#72): The 'statistics' button on the toolbar opens a popup, showing call counts, proportions, and failure counts by tool (sorted by count descending); use list tasks / keywords for filtering; statistics are of the entire result set rather than the current page. Data is fetched only when the popup opens.

### Operations

#### Modified functionality

- Upgrade norma v0.3.1 → v0.3.2: Fix the silent discarding of reasoning/refusal in OpenAI's response (Responses API). `reasoning_text`), reasoning Remove duplicate names from field three, bounded retries for empty responses (zero-content blocks), gateway 400 caused by tool pairings at compression boundaries (also fix orphaned orphans in transcript).

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.5] - 2026-08-25

### LLM

#### New functionality

- Each LLM configuration supports switching between streaming/non-streaming (default streaming): Turn off streaming SSE; turn on true·Non-Streaming (`stream:false`, Return complete JSON in one go, bypassing some gateways' poor SSE implementations (empty frames, missing thinking field frames). The cost is losing real-time progress and real-time token counting. worker / planner / mainagent / chat / goals dynamically take current active configuration values;`llmpool` / `llmrec` / All three Provider layers during task execution are compatible with non-streaming;`llm_profiles` Only Keep `streaming` Directly Install `ALTER` Supplement old database (default `true`, Old configuration without feeling).
- Support OpenAI Responses API format LLM configuration: Add third format to each configuration `openai-responses` (Attack `POST /v1/responses`), Parallels Chat Completions / Anthropic;`BaseURL` Normalize, default model `gpt-5`; `llm_profiles` To `format` Constraint added `openai-responses` And idempotent migration to update the old database; add 'OpenAI' in front-end format dropdown. (Responses API)'. Dependency upgrade norma v0.3.1 (Contain `reasoning_content` Return for repair).
- Record LLM request/response HTTP raw text: Capture real wire body at HTTP transport layer, retain normalized view unseen tool schema,`tool_use` a block with the original SSE frame; norma internal retries kept multiple attempts sequentially;`llm_records` Only Keep `raw_request` / `raw_response` Two Columns And `ALTER` Update old database; add 'original text' view switch and request/response copy buttons in the recording page details panel (compatible with non-secure contexts) `execCommand` Rollback).

### Generate

#### Modified functionality

- Change session list multi-select to 'multi-select' mode switch: default list no longer permanently resident on each row checkbox (more visually clean), header changed to 'N total + 'multi-select' button; click 'multi-select' to enter selection mode (checkboxes, select all, batch delete), 'completed' to exit and clear selections; batch deletion will automatically exit after success (stays in selection mode for retries on failures); single renaming/topping/deletion still goes through each row ⋯ Menu.

### UI

#### Modified functionality

- Task management, session operations, and traffic viewing experience enhancement (#57): Task list sorting preferences can be persisted, inline renaming uses icons directly triggering (no longer through the menu), drawer (sheet) interaction with traffic details optimization.

#### Fixed issues

- Worker Asset tags display only domain and IP, correctly handling scenarios with empty tags.

### Agent

#### New functionality

- planner When identifying targets, add 'Quantitative Acceptance Verification': The target contains quantifiable acceptance conditions (asset test coverage reaches X%, When obtaining N flags or gaining certain permissions,`prove_goal` Before must verify `graph_overview` The actual measured value (`coverage.pct`, findings Count Etc.), If Not Met in Actual Test Then Prohibited `prove_goal`, Modify the intent to supplement the gap, not marking as met due to 'main part completed/large achievement.' Fixed the issue where target requirements for coverage 100%, Actual test only 40% But is judged as completed.

#### Modified functionality

- Rewrite the planner prompt's criteria for determining 'round 0 intent': The original description of round 0 intent as 'the most common and important principle' would cause planners to prematurely stop when the target is not reached or there are untested areas within the scope. Now, it should only produce round 0 intent in two specific cases.——① All directions have been covered by open/running/recent_done intents;② The next step depends on the output of the currently running work, which has not yet appeared (should wait for it to finish, the next wake-up after the graph updates). Also add reverse constraints: Ensure there are new directions not covered and independent of running work, or if the target is not achieved and there are still untested areas, do not stop because '0 intents are common'.
- Strengthen the 'evidence threshold for negative conclusions' in worker prompts: For negative conclusions that might cause planners to abandon an entire direction, such as 'uninjectable/port closed/no login entry', require exhausting all reasonable means within the intent (changing encoding/parameters/path/method) before making a conclusion; if methods are not exhausted or evidence is weak, mark `confidence=inferred`, Avoid using light-hearted `observed` Don't lock down the whole route (early errors in tasks can particularly misdirect and be difficult to heal).
- The above planner / worker default prompt changes through a one-time migration (`reseedPlannerPrompt` / `reseedWorkerPrompt`, Appended as a new version and switched to the current version; old versions are retained in version history, users with custom prompts can recover them in version records.

### Please Use

#### New functionality

- Only Keep `reset-password.sh` Reset administrator (username fixed `ARTEX`)Password: Supports local / docker two types of deployment, connection information can be explicitly specified or automatically from `--dsn`/`$ARTEX_PG_DSN`/`config.json` Read; use in the library with `pgcrypto` Generate a bcrypt hash compatible with backend login and write it back `settings.auth.password_hash`, No need to restart the service after resetting. Passwords are passed through environment variables, not entered into process argv, and undergo escaping to prevent injection.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@neouks](https://github.com/neouks)

## [0.3.4] - 2026-08-24

### UI

#### New functionality

- After selecting multiple tasks in the task list, you can batch modify categories. The target category supports 'Uncategorized' to move tasks out of the current category; all items are written within a single transaction. Failed items will only be those that were deleted after selection.

#### Modified functionality

- The 'task category' for new tasks has changed from a dropdown selection to a searchable input box. As you type, it filters existing categories. If an entered name does not exist in the library (or if you click on the 'create' option in the dropdown), it will be created and selected immediately. Selected items are displayed as removable tags; limited to single categories.

### Generate

#### Fixed issues

- Fix the issue where a session has selected an LLM configuration, but there is no active global configuration, causing messages to be intercepted by 'LLM not configured, unable to chat'. The pre-send check originally only looked at the global active configuration, while actual execution prioritized the session's selected configuration. The two parsing logics were inconsistent; now they are unified (session/Agent binding takes precedence, with a fallback to global).
- Prompt for inability to chat is divided by status: no configuration prompt 'Add Configuration' when completely unconfigured, configuration present but inactive prompt 'Activate or specify a configuration for this session,' no longer showing 'Unconfigured' uniformly for easier localization.

### Agent

#### New functionality

- Tasks that have all objectives met, the main agent will execute a script: `add_intent` Issued intents can be directly claimed and executed by workers; they stop after completion. At this point, the Planner no longer runs (tasks without open targets do not enter planning), avoiding rejudging target achievement and canceling recently issued intents; tasks return to completed when the frontier is exhausted. Before the main agent issues an intent, it will ask the user if it should be registered as a formal target. If registered, the task resumes regular autonomous planning.

#### Modified functionality

- `get_worker_trace` / `get_task_worker_trace` To `step_ids` When exceeding the limit (5 times) no longer report directly; instead, return the complete content of the first 5 steps and indicate in the result `returned_step_ids` / `omitted_step_ids` / `notice` Inform which were taken and which are still pending, and prompt 'no need to take more if sufficient'. Duplicate and illegal IDs are first deduplicated and removed before counting.

#### Fixed issues

- The task list is now sorted by task ID in descending order (newest created at the top), replacing the original sorting by creation time. Multiple tasks created simultaneously have the same timestamp and unstable sorting; combined with 10-second polling and random traversal of memory maps, they frequently swap positions in the list.

### Upgrade

#### Fixed issues

- Fixed the issue with enterprise ownership recalculation being affected by single-line dirty data:`assets.ip` When storing host names (Agent or Asset API writes),`a.ip::inet` Default Configuration `22P02`, Resulting in new/modified enterprise asset range failure and rollback. Use safe conversion `try_inet()`, Skip illegal values instead of interrupting the entire statement.
- Unable to parse `ip` No longer silently skip: After enterprise-wide save, it will clearly prompt how many assets have `ip` Not a valid IP (including specific IDs and values), reminding these assets will not be categorized by IP/CIDR rules; the same information is also written to the server logs, covering paths without frontend responses (deleting companies, scopesentry synchronization, Agent writes).
- Synchronize IP/CIDR matching for task test range. `try_inet()`, Replace the original regular guard that only checks character sets (`abc.def` Previously, hostnames with all hex letters would still be missed and trigger the same error).

#### Modified functionality

- `ip`, `service`, `endpoint` Assets' `ip` Field no longer accepts hostnames:`insert_assets` The asset API returns each item with `index`) with an error and directly provide the correction method (use `type=subdomain` Fill `domain`, Or first parse A/AAAA records), Agent can correct and reinsert it. Other assets in the same batch are unaffected, stored normally.

### Contributors

- [@neouks](https://github.com/neouks)

## [0.3.3] - 2026-08-23

### Worker

#### New functionality

- Running Worker supports single pause, resume and cancel; pause retains intent, facts and vulnerabilities, cancel cleans up current intent and its direct products transactionally upon execution exit.
- Idle `paused` Intent status, execution barrier, and named termination reasons to prevent late blackboard writes after pause, cancellation, or task deletion.
- Increase optional task concurrency limit; new, recovery, vulnerability deepening, and queue supplementation all go through a persistent FIFO admission path.

#### Modified functionality

- Worker Single run wall-clock default duration adjusted from 600 seconds to 1200 seconds; timed-out tasks are revived and re-timed on the next actual execution.
- Worker, Planner Unified by the real execution state of the main Agent to drive task status badges; fixed Worker runtime still shows“Development Without Subdomains”The problem.
- Worker Session retains single-item control and summary of current session tokens, moving model name to the right side of the current session title.

#### Deleted functionality

- Delete Worker multi-select, all-select, and batch pause/continue interface, simultaneously remove Worker batch control API and Mock contract.
- Remove Token badge and Tooltip from Worker list row, but keep the underlying full Token ledger and task aggregation interface intact.

### LLM

#### New functionality

- Tasks support ordered LLM configuration chains; clearly identify when supplier quotas are insufficient and automatically switch to the next configuration, persisting the current configuration, exhaustion status, and error summary.
- Running and paused tasks support editing the full configuration chain, adjusting order, and manually switching current config from next LLM call.
- Automatic switching, manual switching, and full chain exhaustion write structured system activities through task activity streams and pop up deduplicated reminders.
- Add task role model parsing interface, unify parsing of Main Agent, Planner, and Worker next call configuration and models.
- Add optional global LLM Pool, supporting configuration call order, specifying model fallbacks, health status, cooldown recovery, and manual reset.
- Add a permanent LLM usage ledger, aggregating input, output, and cache tokens by task, session, model, and configuration.

#### Modified functionality

- Target decomposition, Planner, Worker, and the main Agent share task-level LLM runtime (parsing priority see below).
- Fault transfer only responds to explicit limits, balances, or billing errors; general rate limiting, authentication, network, server-side, and context errors do not cause incorrect switching.
- LLM The configuration page has been changed to a list of configuration cards and a right-side drawer for editing, providing Pool polling order, priority, exclusions, health status, and recovery operations.
- Configuration chain exhaustion information supports narrow screen line breaks; the current model is changed from session list icon to text label next to the current session title and complete Tooltip.
- Adjust the order of LLM parsing within tasks to 'Agent binding → Task configuration chain → Global**: Explicitly bound model roles always run on that model, unbound roles fall to the task chain, falling to global when task chain is empty.
- Outbound proxy left empty means direct connection, no fallback. `HTTP_PROXY`/`HTTPS_PROXY` Environment variables (explicit `ARTEX_LLM_PROXY` Not affected); proxy input supports account/password authentication `socks5://user:pass@host:port`.

#### Fixed issues

- Instantaneous streaming failure before submission (no output yet) on the same provider is safely retried with backoff, significantly reducing the likelihood of a test being interrupted after running only one or two tools without summary or final state `model_error`'In case; quota exhausted, context too long, and 4xx deterministic errors do not retry, still handled by fallback and compression recovery.

#### Deleted functionality

- Remove LLM icon identifiers from the session list to avoid confusion with Worker status icons.

### UI

#### New functionality

- Tasks support creation, renaming, deletion, and filtering of global categories; new tasks can directly select categories.
- Add global task template CRUD and right-side management drawer; new tasks can load preset descriptions and targets, or save current content as a template.
- New tasks support associating multiple source tasks and multiple enterprise asset ranges; enterprise range automatically identifies domains, URLs, IPs, CIDR, ICP, and enterprise keywords through a single multi-line text box.
- Task test assets support real-time addition and deletion, recording sources such as manual, enterprise, inheritance, or Agent discoveries. The Worker session title displays the current test asset and source summary.
- The discovery page groups tasks and provides independent pagination within each group, supporting descriptions for vulnerabilities and creation of high-priority deep intents.
- Dialog supports renaming, pinning, unpinning, and deletion; task list supports batch pause/continue on current page.
- Traffic details and tool execution details changed to right drawer; HTTP message completion `Host` And highlight the request line, status code, Header, JSON, and marked language body.
- Add 'Details' and 'Pause/Continue' buttons to the operation column of the task list; the session send key can be configured in system settings ().`Enter` / `Cmd+Enter`), add a new proxy input box in the web search area.
- Change the badge on the right side of the session title to display the LLM configuration name, move model ID to hover Tooltip; tasks can set optional names, add a new name column and search in the task list (empty reverts to description).
- Add usage count, last used time, and missing dependency statistics on the skills page for easier identification of unused or uninstalled skills.

#### Modified functionality

- Task title changed to focusable detail link; task assets, enterprise assets, discovery task groups, and group vulnerabilities use server real pagination, stable sorting, and precise total count.
- New enterprise uses the right drawer for adding, with range entry unified as multi-line text providing real-time recognition, validation, and type preview.
- The main Agent input box supports automatic growth,`Enter` Send,`Shift+Enter` Wrapping, and avoiding sending incorrect characters due to Chinese input method composition states.
- Agent Preview, task reports, and related details uniformly use shared Markdown rendering; fix confirmation deletion, long errors, and mobile drawer width.
- The task template selection box displays and searches by template name, submits by template ID; restore system original font size, application version displayed uniformly as `0.3.3`.
- The total number of tasks and the current session Token are only highlighted when input, cached, and output values are read; the send button uses a simpler up arrow icon.
- Task overview displays enterprise name instead of ID in test range.

#### Fixed issues

- Ordinary text containing dots is no longer misjudged as ICP filing numbers.
- Fix variables in the directory conflicting with global runtime variables (like `{{.Now}}`)Name collision causing Agent editor variable list rendering duplicate keys.

#### Deleted functionality

- Delete task card independently“Wake Up”button, unify clicking on task titles to enter details.
- Delete dashboard“New task”Button, new enterprise Logo URL input field, and vulnerability count Badges at the top of the discovery page and within task groups.
- Revert global font size by 10.% The style, and delete the Worker selection box, model icon, and row-level token statistics from the session list.

### Agent

#### New functionality

- New tasks can directly link to multiple existing tasks, inheriting real-time only read from the source task's targets, facts, vulnerabilities, completed intents, asset scope, and whiteboard context.
- Blackboard tool supports on-demand querying of source task nodes, facts, vulnerabilities, and execution trajectories; inherited nodes carry source marks, and all write tools refuse modification.
- Enterprise tools support domain names, URLs, IPs, CIDR, ICP, and enterprise keywords; keywords are only used as Agent scope prompts and do not participate in automatic asset attribution.
- Vulnerability deep operation will create an asset anchor point and `derived_from` High-priority manual Worker intent at the edge of the original task.
- Overview adds a 'Target Management' card, supporting manual viewing, adding, modifying, and deleting targets; adding or modifying notifies Planner and revives tasks, deletes hard-deleted target nodes (cascading delete edges and anchors) but does not revive.
- Main Agent defaults to receive `steer_work` Tool to inject correction instructions into running Worker instances in real-time without interrupting or losing progress (still validating intentbelongs to this task).

#### Modified functionality

- The source task's intent does not enter the new task frontier, the new task continues to use independent exploration, execution queue, working directory, and conversation history.
- Deleting running intents no longer destroys data: changed to stop at `stopped`, Require filling in the deletion reason, attach the reason as a fact to the intent and write it into the payload. The planner will handle `cancelled` Trigger perception (retain intent content and reason).
- Main Agent conversation changed to server-driven activity flow, recoverable input on failure; automatically stick to the bottom when opening a session and keep the last reply visible after details lazy load.
- Pausing a task will terminate the current Main Agent, Planner, and Worker calls, but it does not prevent users from initiating new main agent orchestration sessions during the pause.
- Cancel, close, and stream interruption uniformly retain the true termination reason, generated content, run rounds, time taken, Tokens, and uncalled tools.

#### Deleted functionality

- Delete main Agent client optimistic message echo to avoid duplicate messages and cross-session streaming under pause/failure/concurrent activity.

### Lateral Movement

#### New functionality

- In the target decomposition phase, extract operation constraints (allow/deny) before breaking down the targets; these can be supplemented by the main Agent during runtime. Constraints are injected into the Planner and Worker systems with the highest priority, ensuring that post-bias diversity and broad exploration explicitly comply with the constraints.
- Overview adds a 'Constraint Management' card and CRUD interfaces. Constraint injection range can be toggled by Planner/Worker (default is on, read and take effect immediately). New table `task_constraints` Reference `CREATE TABLE IF NOT EXISTS`, Old library auto-rebuild.

### Write As

#### New functionality

- Command interception adds model fallback approval outside regular/strings rules: when no rule hits, the model makes the decision. `ALLOW`/`ASK`/`DENY` Semantic judgment, failure fallback actions and approval timeout actions can be configured; intercept page splits 'Intercept Rules / Model Configuration' two Tabs, model judgment results annotated `[Model]` Prefixes and reasons.

#### Fixed issues

- Strengthening the judge's output parsing to avoid correct `DENY` Misidentified as allowed.

### Findings

#### Modified functionality

- Traffic recording changed to store the entire exchange in SQLite (large text overflowing to a de-duplicated blob bucket by hash), building trigram full-text indexes for text bodies, supporting any substring and Chinese search; delete degenerated into a single SQL transaction, reducing from hours to milliseconds without stopping the recording. Add large body streaming download and `traffic_search` Full-text parameters; old file tree data remains migration-free, still readable, searchable, deletable.

### Tasks and assets

#### New functionality

- Create a new 'asset coverage function' toggle for tasks (default on): If turned off, do not calculate or display coverage; thethe situation graph only shows assets, and automatic accumulation is not performed. `task_scope`, Relevant tools removed from Planner / Main Agent; enterprise associations are not affected by this switch.
- `insert_assets` New asset added to each asset `related` Mark (default `true`): Only takes effect when coverage is enabled,`false` Only enter shared asset library, not counted towards coverage of this task (such as incidental subdomains or unrelated assets).
- Enterprise-wide and task test asset support for domain names, URLs, IPs, CIDR, ICP, and keyword text recognition; domain/IP can create or reuse global assets, while CIDR/ICP/keywords remain within the context of the task scope.

### Intermediate

- `build.sh --release` Supports building Linux amd64/arm64, macOS amd64/arm64, and Windows amd64, generating with `skills/`, Zip package for configuration examples and README.
- Use Go linker to remove debug information and generate a zip for release; UPX changed to explicit optional, avoiding its self-extracting ELF causing segmentation faults on some Linux environments.
- Release Workflow Execute binary startup smoke test for Linux amd64 and provide with release package. `SHA256SUMS`.

### Contributors

- [@neouks](https://github.com/neouks)

## [0.3.2] - 2026-08-20

### Added

- New tasks support associating multiple existing tasks, inheriting directly from the source task's facts, vulnerabilities, completed intents, asset scope, and blackboard context in real-time and read-only; new tasks still use their own execution queue, working directory, and session history.
- Task supports ordered LLM configuration chain. Automatically switch to the next configuration when a supplier's quota is insufficient and persist the current configuration, exhaustion status, and structured audit activities.
- Running and paused tasks support editing LLM configuration chains, adjusting order, and manually switching the current configuration; automatic switching, manual switching, and full chain depletion will prompt in the task details page.
- Running Workers support pause, resume, and cancel. Pause retains the whiteboard data; cancel will transactionally clean up the intent and its directly produced facts, vulnerabilities, and execution records after the Worker stops writing.
- Deleting a task can choose to clean up associated assets, traffic, vulnerabilities, and tasklanded files, and increase deletion barriers, concurrent protection, and auditable deletion statistics.
- Increase optional task concurrency limit; new tasks reaching the limit are queued in FIFO order and automatically start when running slots become available.
- Add task asset pagination, company asset pagination, task category statistics and pagination intent mock contract.
- Idle `build.sh` Single binary build script, supporting static frontend export, resource embedding, cross-platform targets, and build version injection.

### Changed

- Coexistence of task-level explicit LLM configuration chain with the existing global LLM Pool; the explicit chain continues to use strict quota fault transfer semantics, while using Agent binding and global configuration rules when there is no explicit chain.
- Main Agent input box supports auto-growing multi-line input,`Enter` Send,`Shift+Enter` Wrapping, and avoiding sending incorrect characters due to Chinese input method composition states.
- Planner, Worker and the main Agent shares task-level LLM runtime; the task chain uses the smallest context window from candidate models as a safe compression threshold.
- Task details page displays running, idle, and paused states based on the real LLM call status; facts, vulnerabilities, intents, asset references, and graph nodes are uniformly marked with source information and kept read-only.
- Agent Preview, task reports, and related detail views use a shared Markdown rendering component.
- LLM Model configuration uses card and drawer interaction, model lists support independent scrolling within the drawer.
- Task pause no longer intercepts main Agent dialog: Main Agent orchestrates sessions independently of task pause, can continue sending new messages during pause (pause only terminates the current round).
- Worker Default wall-clock duration for single run adjusted from 600 seconds to 1200 seconds.

### Fixed

- Remove optimistic echo from the main Agent console and replace with pure server-side data rendering to fix issues like message misalignment and content intrusion in other sessions when paused or sending fails.
- Open the main Agent session by default to scroll to the bottom and fully display the last reply: the complete content of the last reply is lazily loaded and automatically sticks to the bottom, no longer being pushed off the screen.
- Fix the issue where the main Agent session continues to run after pausing a task, and the Orchestration Agent failing to synchronize stopping the main Agent when pausing a task.
- Fix the issue where task status badges and operation button statuses differ when a repair task is completed, Planner, Worker, or the main Agent actually runs.
- Fixed issues with delayed blackboard writes or residual files between Worker cancellation, task deletion, and concurrent writing.
- Fix Markdown preview failure, delete confirmation long text overflow, and misalignment of mobile width and some task detail buttons.
- Remove named termination reasons for all Agents in the path. Activity details can display the canceler, final state, number of runs, duration, token usage, and unreturned tool calls.
- Fixed backend issue where parent context may preemptively override named contexts upon shutdown. `shutdown` Race condition and Chinese activity summary byte truncation causingmojibake issues.
- Fix the issue of lost real termination reasons for cancellation events with partial streaming output, while retaining content generated before cancellation.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@neouks](https://github.com/neouks)

[Unreleased]: https://github.com/Autumn-27/ARTEX/compare/v0.3.10...HEAD
[0.3.10]: https://github.com/Autumn-27/ARTEX/compare/v0.3.9...v0.3.10
[0.3.9]: https://github.com/Autumn-27/ARTEX/compare/v0.3.8...v0.3.9
[0.3.8]: https://github.com/Autumn-27/ARTEX/compare/v0.3.7...v0.3.8
[0.3.7]: https://github.com/Autumn-27/ARTEX/compare/v0.3.6...v0.3.7
[0.3.6]: https://github.com/Autumn-27/ARTEX/compare/v0.3.5...v0.3.6
[0.3.5]: https://github.com/Autumn-27/ARTEX/compare/v0.3.4...v0.3.5
[0.3.4]: https://github.com/Autumn-27/ARTEX/compare/v0.3.3...v0.3.4
[0.3.3]: https://github.com/Autumn-27/ARTEX/compare/v0.3.2...v0.3.3
[0.3.2]: https://github.com/Autumn-27/ARTEX/compare/v0.3.1...v0.3.2
