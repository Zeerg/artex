package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Zeerg/artex/db"
	"github.com/Zeerg/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// Worker is an LLM work agent (docs §4.4): it claims ONE intent, completes it
// with real tools (Bash: kali tooling through the recording proxy), writes the
// FACTS it found back into the graph, and stops. It does NOT generate new
// directions (that is the planner's job) and does NOT keep exploring toward the
// goal on its own. Multiple workers run concurrently as goroutines.
// WebSearchOpts is the web-search backend selection the server pushes into each
// agent (planner/worker/main). Enabled=false leaves the web_search tool off.
// Backend is "ddgs" (no key), "brave-free" (BraveKey required), "tavily"
// (TavilyKey required), or "deepseek" (DeepSeek* required, filled from the
// active LLM profile). It maps directly onto agentcore.Options.
// Proxy is a dedicated egress proxy for the search request (http/https/socks5),
// independent of the traffic-recording MITM proxy — set it when the search endpoint
// is only reachable via a VPN/SOCKS proxy. Empty = direct.
//
// Note that deepseek backend has different properties from the other three: DeepSeek does not have a directly callable search interface,
// Search only exists within its Anthropic-compatible messages interface.(web_search_20250305 server
// tool), Therefore, each search consumes one model call, and the search request is issued by the DeepSeek server.——
// Does not go through this machine Proxy, nor does it leave traffic traces.
type WebSearchOpts struct {
	Enabled bool
	Backend string
	BraveKey string
	TavilyKey string
	Proxy string
	// DeepSeek* From the currently active LLM configuration.(Only DeepSeek official endpoints in anthropic format),
	// Not configured separately, changes with LLM configuration switch.
	DeepSeekBaseURL string
	DeepSeekAPIKey string
	DeepSeekModel string
}

type Worker struct {
	findingRecorder FindingRecorder
	prov llm.Provider
	model string
	workDir string
	proxyAddr string
	proxyCACert string // recording proxy's CA cert path (for WebFetch HTTPS verify)
	webSearch WebSearchOpts // web_search tool backend selection (off by default)
	tx *transcript.Store // raw LLM conversation persistence (nil = off)
	window int // context window in tokens (for compaction)
	windowFn func() int // optional dynamic task-chain minimum
	maxTurns int // max agent turns per run (0 = unlimited)
	// runTimeout is the wall-clock budget for the main exploration of one intent
	// (0 = unlimited). When it fires, the run is cut and a settlement round is
	// forced so already-identified facts get written back instead of being lost.
	runTimeout time.Duration
	// extraTools are host-provided tools (e.g. traffic query, oast) appended to
	// the worker's graph write-back tools.
	extraTools []actool.CoreTool
	// injectConstraints resolves whether this task's operation constraints get
	// injected into the worker system prompt. Read per run so the settings toggle
	// takes effect without rebuilding the agent. nil = inject (default).
	injectConstraints func() bool
	// nonStreamingFn resolves whether this run uses the non-streaming (Complete)
	// path. Read per run so a profile/task toggle takes effect without rebuilding
	// the agent. nil = streaming (default).
	nonStreamingFn func() bool
	// noaEnabledFn resolves whether this run uses the experimental noa context-
	// compression mechanism. Read per run, like nonStreaming. nil = off (built-in
	// compaction).
	noaEnabledFn func() bool
	// maxTokensFn resolves the per-reply output cap in tokens, on the same
	// per-run basis. nil or 0 = send no cap and let the endpoint decide.
	maxTokensFn func() int
}

// WorkerSessionID returns the stable transcript key used by a worker intent.
// Worker slots are reusable, so the intent id (rather than work#N) is the
// session identity. Keep this helper public so the Worker message API and UI
// can refer to exactly the conversation that will be resumed.
func WorkerSessionID(explorationID, intentID int64) string {
	return fmt.Sprintf("exp%d-worker-i%d", explorationID, intentID)
}

const workerChatMarkerPrefix = "<!-- ARTEX_WORKER_CHAT:"

func workerChatMarker(requestID string) string {
	return workerChatMarkerPrefix + requestID + " -->"
}

func hasWorkerChatMessage(messages []llm.Message, requestID string) bool {
	marker:= workerChatMarker(requestID)
	for _, message:= range messages {
		if message.Role == llm.RoleUser && strings.Contains(message.Text(), marker) {
			return true
		}
	}
	return false
}

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default). Read per
// run so a profile or task-chain toggle takes effect without rebuilding.
func (w *Worker) SetNonStreaming(fn func() bool) { w.nonStreamingFn = fn }

func (w *Worker) nonStreaming() bool { return w.nonStreamingFn!= nil && w.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (w *Worker) SetNoaEnabled(fn func() bool) { w.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (w *Worker) SetMaxTokens(fn func() int) { w.maxTokensFn = fn }

func (w *Worker) maxTokens() int {
	if w.maxTokensFn == nil {
		return 0
	}
	return w.maxTokensFn()
}

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the worker system prompt. nil = inject (default).
func (w *Worker) SetConstraintInject(fn func() bool) { w.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (w *Worker) wantConstraints() bool { return w.injectConstraints == nil || w.injectConstraints() }

// SetRunTimeout configures the per-intent wall-clock budget for the main
// exploration (0 = unlimited). When it fires, the SDK settlement phase still runs
// so facts are never lost to a timeout. Safe to call before Execute.
func (w *Worker) SetRunTimeout(run time.Duration) {
	w.runTimeout = run
}

// settleWrapUpPrompt is injected by the SDK settlement phase when a worker hits its
// turn/time budget: stop probing, write back what was found, then end with a
// plain-text one-liner (which becomes this run's displayed result).
const settleWrapUpPrompt = "You are about to be terminated due to budget depletion. Do not run any commands/probes anymore. Please do the following in order:(1) Write back the content you have identified but haven't written back yet, item by item——New assets use insert_assets, exploration conclusions/facts use record_fact, confirm vulnerabilities use report_finding;(2) **End with a single sentence in plain text.**Summarize what you did and the key conclusions you reached (this sentence will be shown as the result of this run, so make sure to output it)."

func NewWorker(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int, extra ...actool.CoreTool) *Worker {
	return &Worker{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, extraTools: extra}
}

// defaultToolsExcept returns actool.DefaultTools() minus the named tools (by
// CoreTool.Name()). Used to trim SDK default tools an agent shouldn't have.
func defaultToolsExcept(exclude ...string) []actool.CoreTool {
	drop:= make(map[string]bool, len(exclude))
	for _, n:= range exclude {
		drop[n] = true
	}
	all:= actool.DefaultTools()
	out:= make([]actool.CoreTool, 0, len(all))
	for _, t:= range all {
		if!drop[t.Name()] {
			out = append(out, t)
		}
	}
	return out
}

func (w *Worker) SetCompactionWindowResolver(fn func() int) { w.windowFn = fn }

func (w *Worker) compactionWindow() int {
	if w.windowFn!= nil {
		return w.windowFn()
	}
	return w.window
}

// SetProxy configures the recording proxy address that workers route target
// traffic through, plus the CA cert path WebFetch trusts to verify HTTPS through
// that MITM proxy. Empty addr disables the hint.
func (w *Worker) SetProxy(addr, caCert string) { w.proxyAddr, w.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for this worker (off by default).
func (w *Worker) SetWebSearch(o WebSearchOpts) { w.webSearch = o }

// proxyEnv builds the Bash-subprocess env that routes child-command HTTP through
// the egress proxy (the recording MITM when capture is on, or the global proxy
// directly when it is off) and, only when a MITM CA is present, makes the common
// toolchain trust it — so tools need no manual -x/--proxy/-k. Each ecosystem reads
// a different CA var (verified empirically): SSL_CERT_FILE→curl/urllib/Go/openssl,
// REQUESTS_CA_BUNDLE→python requests (it ignores SSL_CERT_FILE), CURL_CA_BUNDLE→curl,
// GIT_SSL_CAINFO→git, NODE_EXTRA_CA_CERTS→node; NODE_USE_ENV_PROXY makes Node 24+
// honor the proxy vars. ALL_PROXY is set too so a socks5 egress proxy (which curl
// only reads from ALL_PROXY, not HTTP(S)_PROXY) works in the capture-off path.
// Empty proxyAddr → nil (direct, unchanged env).
func proxyEnv(proxyAddr, caCert string) []string {
	if proxyAddr == "" {
		return nil
	}
	env:= []string{
		"HTTP_PROXY=" + proxyAddr, "HTTPS_PROXY=" + proxyAddr,
		"http_proxy=" + proxyAddr, "https_proxy=" + proxyAddr,
		"ALL_PROXY=" + proxyAddr, "all_proxy=" + proxyAddr, // socks5 egress: curl reads only this
		"NODE_USE_ENV_PROXY=1", // Node 24+: honor HTTP(S)_PROXY in built-in fetch/http
	}
	if caCert!= "" {
		env = append(env,
			"SSL_CERT_FILE="+caCert,
			"CURL_CA_BUNDLE="+caCert,
			"REQUESTS_CA_BUNDLE="+caCert,
			"GIT_SSL_CAINFO="+caCert,
			"NODE_EXTRA_CA_CERTS="+caCert,
		)
	}
	return env
}

// workerDefaultTmpl is the built-in EDITABLE body (Segment [A]) of the worker system
// prompt, seeded into agent_prompts. The trafficTool block and the Intermediate product output normalization
// are NOT here — they are code-owned and appended by workerSystem after rendering
// (Segment [B]/[C]), so editing the DB body can never drop them.
const workerDefaultTmpl = `You are a cybersecurity platform authorized penetration testing system."Executor"(work agent). You received a [single intent].(One-Word Exploration Direction), Unique responsibility:**Complete this intention, write the findings back to the knowledge graph, and then stop returning.**

**Boundary (red line)**:
1. **Only do what you are given this intent**. **When probing intent and spotting clues worth digging deeper than the current intent** (Error leaks paths, points that may link to other assets, suspected entry of another exploitation chain),**Add a sentence to the fact's summary for planner**.
2. Must be reproduced / fixed / inconclusive.——Outputting the conclusion after all bypass methods for this intent have been exhausted;
3. Operate only within authorized ranges. If there are [Operation Constraints] at the top of the system prompt, they are the highest priority red line: Each command/probe is self-inspected before execution; if violated, it will not be executed (even if it falls under your intended actions).

**Write Back As You Discover** (Write into the graph counts as valid; thoughts/words in the brain don't count; write each result immediately, don't accumulate until steps run out). Three types of feedback, no serializing graphs:
- **New asset/resource → insert_assets (asset graph)**: All assets [themselves] including subdomain / service / endpoint / fingerprint / credentials etc.**Only register assets here; exploration conclusions/judgments don't go here, use record_fact.**
- **Exploration conclusion/fact → record_fact (Exploration graph, passing intent_id).**: Use It All.**Multiple observations are summarized into one fact** (summary One sentence summary + detailExtend the summary by relying on the actual execution process), don't one attribute per line, one intent usually only one, breaking it down would cause the graph to infinitely expand——**Default to one entry. Merge with detail if possible**; Only use facts array for separate items when there is a [completely independent and non-mergeable] conclusion. This is an extremely rare exception, not regular.**Write Incrementally Only**: Only remember this 'newly obtained' one. Do not rephrase and re-record existing facts (only verify existing ones, no new additions are necessary).**Only write what you see truly.**: Provide evidence (one line: command+The most convincing one or two lines of output, concise; details in detail), confidence (observed=Directly see / inferred=Infer from phenomena).
- **Confirm vulnerability → report_finding (Explore graph, including PoC, pass intent_id)**: **Only use if you have genuinely triggered it this session and obtained reproducible evidence (request/response or command output).**. Strictly no putting"Version/fingerprint matches CVE""Parameters seem injectable.""External vulnerability library/update log/code diff inference"Even if confirmed, do not use the CVE library or compare patch versions to replace actual triggering. Triggered but with suspicion → Use record_fact to log an inferred fact (suspect point+Why didn't trigger) hand over to planner, don't memorize as finding.


Summarize what you did and which facts were written after completing the intent in one sentence.`

// workerTrafficBlock is Segment [B]: the traffic-tool note, code-injected only when
// traffic capture (recording) is on — i.e. the traffic_* tools actually exist.
// Gated on recording, NOT on the egress proxy: a global proxy with capture off
// routes traffic but records nothing, so the tools would not be there. Not stored,
// not editable.
func workerTrafficBlock(recording bool) string {
	if!recording {
		return ""
	}
	return "\n\n**Traffic tools**: \n- traffic_search / traffic_get / traffic_blob: Review the response and find visited resources,**First check traffic, do not repeat curl for the same URL.**. traffic_search **Must specify host**, Default to returning only 3 extremely lightweight indices.(id/method/url/status/resp_len, No response content), Need more explicit calls to increase limit; use body_contains for full-text search in request/response bodies.(At least 3 characters, supporting substrings and Chinese, such as finding passwords/keys/errors/internal network addresses.); To see the original text use traffic_get(id), The super large body text is displayed as @blob sha256:<hash>, Use traffic_blob,(hash) Segment and fetch full text."
}

// artifactSpec is Segment [C]: the code-owned, non-editable tail appended to every
// pentest agent's prompt — intermediate artifacts must land in the shared work
// dir, never /tmp. Guaranteed present regardless of how the DB body is edited.
func artifactSpec(dir string) string {
	return "\n\n**Intermediate product output normalization**: Scripts, payloads, captured response bodies, temporary data, etc., all intermediate products,**Write everything to the task's working directory. " + dir + "** (Relative paths can be written here or use this absolute path.——**Do not write /tmp, do not use other absolute paths**. "
}

// workerArtifactSpec is the worker's Segment [C]: its per-intent run dir is pre-created
// by the engine (ensureRunDir), so it just writes relative paths there — no manual
// mkdir, no cross-worker name collisions.
func workerArtifactSpec(runDir string) string {
	return "\n\n**Intermediate product output normalization**: Scripts, payloads, captured response bodies, temporary data, etc., all intermediate products,**They should all be written to the exclusive working directory of this intent. " + runDir + "** (It has already been set up, so you can simply use relative paths here without needing to manually create directories.——**Do not write /tmp, do not use other absolute paths**. "
}

// ensureRunDir builds and creates an agent's working directory under base:
// <base>/tasks/<taskID> for planner/main; <base>/tasks/<taskID>/i<intentID> for a
// worker (intentID<=0 → task dir only). The "tasks/" segment groups per-task dirs
// symmetrically with the chat agent's "sessions/<sessionID>". Best-effort mkdir — on
// failure, writes fail the same way an unwritable CWD would.
func ensureRunDir(base string, taskID, intentID int64) string {
	dir:= filepath.Join(base, "tasks", strconv.FormatInt(taskID, 10))
	if intentID > 0 {
		dir = filepath.Join(dir, "i"+strconv.FormatInt(intentID, 10))
	}
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// cmdOutDir is the SDK large-tool-output spill dir under an agent's run dir.
func cmdOutDir(dir string) string { return filepath.Join(dir, "cmd-output") }

func workerSystem(proxyAddr, caCert, dataDir, runDir string) string {
	body:= renderSystem("worker", workerDefaultTmpl, WorkerVars{ProxyAddr: proxyAddr, DataDir: dataDir, Now: nowStr()})
	// caCert is present only when the recording MITM is on, which is exactly when
	// the traffic_* tools are registered — so it gates the traffic-tool note.
	// Optional finding guidance is added for every role after tool resolution.
	return body + workerTrafficBlock(caCert!= "") + workerArtifactSpec(runDir)
}

// renderIntentTask formats the claimed intent for the worker's launch USER message:
// the intent is the worker's whole job. It used to live in the system prompt; it now
// rides in the first user turn (together with the situational overview) so the system
// prompt stays static/role-only — same move as the planner's situational block.
// intentAssetIDs pulls the intent's target asset ids out of its payload
// (planner's add_intent stores them as a numeric asset_ids array). nil on absence
// or malformed payload.
func intentAssetIDs(intent *db.Node) []int64 {
	if intent == nil {
		return nil
	}
	var p struct {
		AssetIDs []int64 `json:"asset_ids"`
	}
	if err:= json.Unmarshal(intent.Payload, &p); err!= nil {
		return nil
	}
	return p.AssetIDs
}

func renderIntentTask(intent *db.Node) string {
	return fmt.Sprintf("\n\n[The intent you received (this is the unique task this time: only do one thing, only generate facts, stop upon completion)]: \n%s\nIntent ID: %d (Pass it when writing back record_fact / report_finding.", string(intent.Payload), intent.ID)
}

// renderWorkerGraphOverview folds the global situational snapshot into the worker's
// launch USER message for AWARENESS ONLY. The framing is deliberately strong: the overview
// must NOT widen the worker's job — it still does only its assigned intent. Its sole
// purpose is letting the worker read context (existing facts/assets/hints)
// so it avoids redundant work and doesn't re-derive what others already found.
func renderWorkerGraphOverview(data map[string]any) string {
	// coverage A signal for planners to judge 'which type is tested less / whether to expand the scope', with workers only doing what they are assigned
	// The responsibilities of the intent and the segment that does not cover it are contradictory → Remove from worker view. data is the current worker
	// A dedicated new map, deleting keys does not affect planner.
	delete(data, "coverage")
	b, err:= json.Marshal(data)
	if err!= nil {
		return "" // fall back silently: the worker just won't have the global context
	}
	return "\n\n[Global explorationsituation (read-only, helps you see your intent in the broader context).\n" +
		"Below is the current exploration overview for this entire task. Two purposes: one is to know what others have already discovered so you don't repeat it; two is to help you think about your own intent in relation to the global context.\n" +
		"**Divergence is good**: While probing an intent, deeply think and make many associations. The only boundary is——Don't actually go ahead and execute other intents (that's someone else's worker's job, scheduled by the planner). Whenever you think of valuable leads (cross-assetlinkages, suspected entry points of another exploit chain, and globally suspicious points),**Must be written into fact for planner**——This is your important output; it's not optional. Rather than missing one, report an extra one for the planner to judge.\n" +
		string(b)
}

// Execute runs one intent. hooks (the per-task Guard) gates every tool call; may
// be nil. emit, if non-nil, receives one ActivityRecord per execution step.
// notifyFinding, if non-nil, is called (intentID, summary) when this worker writes
// a finding (report_finding) so the task's planner wakes mid-flight — with context
// on which intent found what — instead of waiting for the worker to finish.
// Returns the terminal reason (so the engine can distinguish completed vs
// max_turns) and a per-kind breakdown of what was written back (so an intent that
// explored but persisted nothing isn't mistaken for done, and the engine can log
// facts/assets/findings separately instead of lumping them under "facts").
func (w *Worker) Execute(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string)) (harness.TerminalReason, WriteCounts, error) {
	return w.execute(ctx, name, taskID, as, ts, intent, hooks, emit, enr, notifyFinding, "", "")
}

// ExecuteWithMessage runs the next turn in the same intent conversation with a
// human-authored message. The HTTP handler does not edit the transcript;
// agentcore records the message as a normal user turn when this Worker starts.
// This keeps Worker continuation identical to the regular agent chat flow.
func (w *Worker) ExecuteWithMessage(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string), requestID, message string) (harness.TerminalReason, WriteCounts, error) {
	return w.execute(ctx, name, taskID, as, ts, intent, hooks, emit, enr, notifyFinding, strings.TrimSpace(requestID), strings.TrimSpace(message))
}

func (w *Worker) execute(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string), requestID, message string) (harness.TerminalReason, WriteCounts, error) {
	tsx:= NewToolSet(ts, name)
	tsx.SetFindingRecorder(w.findingRecorder)
	tsx.SetTaskID(taskID)
	coverageEnabled:= as == nil || as.CoverageEnabled(taskID)
	tsx.SetCoverageEnabled(coverageEnabled)
	if as!= nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetOwnerNode(intent.ID) // assets this worker discovers anchor to its intent → visible to the task
	tsx.SetEnrich(enr) // async DNS/HTTP auto-completion for assets this worker writes
	tsx.SetNotifyFinding(notifyFinding) // report_finding Wake planner immediately upon data exfiltration,carrying“which intent+finding'
	// base = built-in worker tools ∪ host tools (traffic) ∪ default tools (incl. Bash);
	// then augment with the agent's visible skills/MCP. During the SDK settlement
	// phase, Bash is hidden via Settlement.DisabledTools (no local gating needed).
	base:= append(tsx.WorkerTools(), w.extraTools...)
	// worker Intentionally omit MultiEdit/Glob/Grep: file fine-tuning use Edit, search with Bash.(grep/find),
	// Converge tool panel, reduce low-value calls. Other SDK default tools(Read/Write/Edit/LS/Bash/Sleep)As usual.
	base = append(base, defaultToolsExcept("MultiEdit", "Glob", "Grep")...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts), IntentID: intent.ID})
	tools, def, cleanup:= AugmentTools(ctx, "worker", base)
	defer cleanup()

	// The intent is worker's [unique responsibility, constant across the entire run].→ Include startup instructions and the target asset for intent anchoring
	// Original data goes into the system prompt: system is rebuilt every run and will never be compacted,
	// The intent is always present during long runs, and it does not depend on whether the transcript history retains that first message when continuing a run. The cost is system
	// Mixing per-intent mutable data, losing cross-intent cache reuse; this is an intentional trade-off (loss of intent is much more serious than saving tokens).
	// The fork from planner 'situation block for user turn' is intentional: planner itself produces intents and has no single mandate,
	// worker Only the global overview remains in the startup user message.——It can degrade and tolerate stale, overriding without hindrance.
	// Exclusive work directory for this intent <workDir>/tasks/<taskID>/i<intentID>, Engine-side setup first.
	runDir:= ensureRunDir(w.workDir, taskID, intent.ID)
	// The run-wide intent is not the current tool action. Do not forward it or
	// inherit a parent run's background into the action reviewer.
	ctx = intercept.WithReviewContext(ctx, runDir, intercept.ReviewBackground{})
	overview:= renderWorkerGraphOverview(tsx.graphOverviewData())
	sysBody:= workerSystem(w.proxyAddr, w.proxyCACert, w.workDir, runDir)
	if w.wantConstraints() {
		sysBody += constraintBlock(ts) // Operation Constraints(If there is)Injection system prompt,worker Strictly follow during execution
	}
	// Intent Block → Intent anchor asset block → Start command, appended to system one after another (same append method as constraintBlock).
	sysBody += renderIntentTask(intent)
	if as!= nil {
		if ids:= intentAssetIDs(intent); len(ids) > 0 {
			if assets, err:= as.GetByIDs(ids); err == nil && len(assets) > 0 {
				if b, err:= json.Marshal(assets); err == nil {
					sysBody += "\n\nThe intended asset ids correspond to the target assets:\n" + string(b)
				}
				// Intent clearly targeting these assets → 'Automatically include in task test scope (same set as insertAssets)',
				// Conservative granularity). upsertTaskScope ON CONFLICT DO NOTHING + uq_task_scope
				// Unique index ensures no duplicate additions; reruns/retries are idempotent no-ops.
				// When the asset coverage feature is disabled, do not accumulate test scope.(Denominator).
				if coverageEnabled {
					for _, a:= range assets {
						_ = as.AddAutoScope(taskID, a.Type, a.Domain, a.URL, a.IP)
					}
				}
			}
		}
	}
	sysBody += "\n\nStart executing the above intent: do only it, generate only facts, assets, findings, and stop when done."
	system, boundary:= deferredSystem(sysBody, def)
	// Task-level deadline(Injected via ctx.)Clamp the wall-clock budget for this run + Decide on the opening and closing phrases(See taskclock.go).
	tc:= taskClockFrom(ctx)
	maxDur, clamped:= clampMaxDuration(tc.DeadlineUnix, w.runTimeout)
	settle:= wrapupSettlement("worker", []string{"Bash"})
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("worker", []string{"Bash"}, clamped)
	}
	opts:= agentcore.Options{
		Provider: w.prov,
		SystemPrompt: system,
		DynamicBoundary: boundary,
		Tools: tools,
		DeferredTools: def.Deferred,
		UnlockSet: def.Unlock,
		PermissionMode: permission.ModeBypass,
		// WebFetch Use the record proxy, its HTTP and curl are traced like usual; load the proxy CA to allow MITM
		// The re-signed HTTPS certificate can be **verified normally** (rather than disabling verification). If proxy is empty, it connects directly.
		EnableWebFetch: true,
		WebFetchProxy: w.proxyAddr,
		WebFetchCACert: w.proxyCACert,
		// Online Search(Optional). ddgs No key; brave-free needs BraveKey; tavily needs TavilyKey.
		// WebSearchProxy Is an independent exit proxy(http/https/socks5), Unrelated to the MITM proxy that records traffic; if empty, it will connect directly.
		EnableWebSearch: w.webSearch.Enabled,
		WebSearchBackend: w.webSearch.Backend,
		BraveSearchAPIKey: w.webSearch.BraveKey,
		TavilySearchAPIKey: w.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: w.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey: w.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel: w.webSearch.DeepSeekModel,
		WebSearchProxy: w.webSearch.Proxy,
		// Bash Subcommand HTTP defaults to recording proxy + Trusting its CA (the tool does not need -x/-k).
		BashEnv: proxyEnv(w.proxyAddr, w.proxyCACert),
		WorkingDir: runDir,
		MaxTurns: w.maxTurns, // 0 = unlimited (configurable in agent management)
		// wall-clock budget,Round Boundary Judgment,Do not interrupt halfway;0 = Unlimited. When there's a task-level deadline, constrain it to min.(Own budget,
		// Time remaining until the deadline),Allow this run to naturally conclude when the task is due.(See taskclock.go).
		MaxDuration: maxDur,
		// Budget hit(Round OR duration)→ SDK Run one round and wrap up(Hide Bash),Write back identified items.,Avoid a bad end.
		// clamped(and is constrained by the task deadline,)Use PromptByReason when needed.:Due to timeout=Task completed→Task Timeout Phrase,
		// Due to steps=Steps within the entrapment window exhausted first→Fallback per-run word. Non-clamped maintains pure per-run.
		Settlement: settle,
		// large tool output spills to cmd-output/ with a head + pointer (SDK tool.Capture);
		// full output preserved on disk. Truncation limit uses SDK default(30000 character).
		ToolOutputDir: cmdOutDir(runDir),
		Compaction: compactionConfig(w.compactionWindow()), // long tool-heavy runs stay within the window
		Todos: actool.NewTodoStore(), // Session-level temporary todo (TodoWrite), purely for planning; discarded on exit
		NonStreaming: w.nonStreaming(), // This profile walks the Provider when not using streaming..Complete
		MaxTokens: w.maxTokens(), // 0 = No upper limit,Determined by the server's default value
	}
	if hooks!= nil { // typed-nil guard: only set when concrete (avoids harness panic)
		opts.Hooks = hooks
	}
	if w.tx!= nil { // persist raw LLM conversation; one file per worked intent
		opts.Transcript = w.tx
		opts.SessionID = WorkerSessionID(ts.ID(), intent.ID)
	}
	intentID:= intent.ID
	emitWrap:= func(r db.Activity) {
		if emit!= nil {
			r.NodeID, r.Worker = &intentID, name
			emit(r)
		}
	}
	// Intent / start command / intent anchor asset has been issued with the system prompt (see above sysBody assembly).
	// This startup user message only carries [global situation overview]——Downgradable understanding of overall information, overshadowing irrelevant details.
	// overview Rarely fails to marshal as empty; falls back to a startup phrase to avoid an initial empty user message on the first round.
	input:= overview
	if strings.TrimSpace(input) == "" {
		input = "start executing system-received intent: do it, only produce facts, assets, findings, and stop when done."
	}

	// Experimental Features:After enabling, context compression is taken over by noa(Archiving Concentrated At <workDir>/noa/<SessionID> down,persistent).
	noaSession:= WorkerSessionID(ts.ID(), intent.ID)
	enableNoa(&opts, w.noaEnabledFn, w.workDir, noaSession, noaWarn(noaSession))
	ctx = attachSideCapture(ctx, &opts)
	s:= agentcore.NewSession(opts)
	defer s.Close() // release the session's background-task manager (temp dir + processes)

	// Resume prior conversation if this intent was paused/blocked/exhausted and is
	// being re-run. The transcript ID is deterministic per intent, so if a prior
	// session exists the worker continues from where it left off instead of
	// restarting from scratch.
	alreadyRecorded:= false
	if w.tx!= nil {
		_ = s.Resume(opts.SessionID)
		alreadyRecorded = requestID!= "" && hasWorkerChatMessage(s.Messages(), requestID)
		if len(s.Messages()) > 0 && message == "" {
			seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
			input = "Continue execution."
		} else if len(s.Messages()) > 0 {
			seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
		}
	}
	if message!= "" {
		if alreadyRecorded {
			input = "Continue executing the new intent from the last manual dialog input. Do not repeat already completed actions."
		} else if len(s.Messages()) > 0 {
			input = workerChatMarker(requestID) + "\n[New intent entered during human conversation]\n" + message +
				"\n\nPlease immediately execute this artificial input, complete it, then decide whether the original task needs to continue based on the context."
		} else {
			input += "\n\n" + workerChatMarker(requestID) + "\n[New intent entered during human conversation]\n" + message +
				"\n\nPlease prioritize executing this manual input."
		}
	}

	// Budgets + settlement are owned by the SDK (MaxTurns/MaxDuration + Settlement):
	// on hit it runs a wrap-up turn and finishes with ReasonMaxTurns/ReasonTimeout.
	// MaxDuration now interrupts an in-flight tool at the wall-clock deadline and
	// enters the wrap-up phase on the live ctx, so a run whose tool overran the budget
	// still settles (no external hard-timeout backstop needed). ctx itself carries only
	// pause / planner kill / shutdown, which the engine distinguishes and re-queues/stops.
	_, reason, err:= captureRunSession(ctx, s, input, emitWrap)
	return reason, tsx.Writes(), err
}
