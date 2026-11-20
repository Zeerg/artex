package agent

import (
	"context"
	"fmt"

	"github.com/Zeerg/artex/db"
	"github.com/Zeerg/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// MainAgent is the thin human-interface orchestrator (docs §4.2 / §7). The human
// chats with it; it observes (read tools), and steers by injecting hints
// (→planner) or direct high-priority intents (→frontier). It does NOT run the
// autonomous intent-generation loop (that is the planner's job).
type MainAgent struct {
	findingRecorder FindingRecorder
	prov llm.Provider
	model string
	tx *transcript.Store // raw LLM conversation persistence (nil = off)
	window int // context window in tokens (for compaction)
	windowFn func() int // optional dynamic task-chain minimum
	maxTurns int // max agent turns per run (0 = unlimited)
	proxyAddr string // recording proxy for WebFetch (empty = direct)
	proxyCACert string // recording proxy's CA cert path (HTTPS verify)
	webSearch WebSearchOpts // web_search tool backend selection (off by default)
	workDir string // shared work dir (surfaced in prompt as artifact-output target)
	steerWork func(intentID int64, msg string) error // engine callback: steer a running work (nil = off)
	nonStreamingFn func() bool // resolver: use non-streaming (Complete) path? (nil = streaming)
	noaEnabledFn func() bool // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn func() int // resolver: per-reply output cap (nil/0 = send no cap)
}

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (m *MainAgent) SetNoaEnabled(fn func() bool) { m.noaEnabledFn = fn }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (m *MainAgent) SetNonStreaming(fn func() bool) { m.nonStreamingFn = fn }

func (m *MainAgent) nonStreaming() bool { return m.nonStreamingFn!= nil && m.nonStreamingFn() }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (m *MainAgent) SetMaxTokens(fn func() int) { m.maxTokensFn = fn }

func (m *MainAgent) maxTokens() int {
	if m.maxTokensFn == nil {
		return 0
	}
	return m.maxTokensFn()
}

func NewMainAgent(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *MainAgent {
	return &MainAgent{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns}
}

func (m *MainAgent) SetCompactionWindowResolver(fn func() int) { m.windowFn = fn }

func (m *MainAgent) compactionWindow() int {
	if m.windowFn!= nil {
		return m.windowFn()
	}
	return m.window
}

// SetProxy points the main agent's WebFetch at the recording proxy plus the CA
// cert it trusts to verify HTTPS through it (empty addr = direct).
func (m *MainAgent) SetProxy(addr, caCert string) { m.proxyAddr, m.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the main agent (off by default).
func (m *MainAgent) SetWebSearch(o WebSearchOpts) { m.webSearch = o }

// SetSteerWork wires the engine callback that lets the main agent's steer_work
// tool inject a mid-run course-correction into a running work (nil = tool off).
func (m *MainAgent) SetSteerWork(fn func(intentID int64, msg string) error) { m.steerWork = fn }

// mainAgentDefaultTmpl is the built-in EDITABLE body (Segment [A]) of the main agent
// prompt, seeded into agent_prompts. Goal is a {{.Goal}} template var; the Report
// Output specification tail is code-owned (artifactSpec), appended after rendering.
const mainAgentDefaultTmpl = `You are an authorized penetration testing system."Main agent", Human operator interface. You do not explore manually or autonomously generate intents (that's the planner's work). Your responsibilities:

1. Observe: Use graph_overview / list_findings / list_facts / list_assets / get_worker_output to answer the responder's questions about current progress.
2. Steer (translate human intent into the system):
 - what you want"Change direction/emphasize a certain class of vulnerabilities/focus on a specific area." → Write a hint using add_hint (the planner will read this next time).
 - what you want"Immediately test a specific target." → Use add_intent directly to inject a high-priority intent (priority 8-10). The system will automatically pull completed tasks back to the running state, allowing the worker to execute this intent, which will return to the completed state upon completion.
 **When all task objectives are met** (graph_overview If there is no constraint (constraints), omit constraints directly and provide the top-level text/type."New and desired result". If implicit, repeat your guessed target in one sentence and**Ask if you want to register as an official target**——person's → Registered with set_goals (the task then enters regular planning, the planner will continue independently); people don't / just want to temporarily probe → Only add_intent this one, and the worker will return to the completed state once the task is finished (will not continue autonomously). If this intent is clearly a one-time verification without implying new targets, you can directly add_intent; there's no need to ask each time.
 - what you want"For a running intent(work)Real-time correction (don't go X, focus on Y)" → Use steer_work (without interrupting or discarding existing progress; takes effect before worker's next action); first use get_worker_output to see what it's doing. If the entire direction is wrong, switch to add_intent for a new intent.
 - what you want"Add a new final goal to be achieved." → Add goals using set_goals. The system will write this goal to the task graph and**Automatically pull completed/paused tasks back to running state and continue running** (The planner will then re-evaluate whether the goal has been achieved based on this, without needing manual intervention to resume.
 - what you want"Add/modify test constraints (allow/prohibit certain operations, like 'test only current port' 'prohibit brute force' 'only passive reconnaissance')." → Register with set_constraints (type=allow Allow / type=deny Prohibited). Constraints will be injected into planner/worker prompts during the next round of planning to frame exploration boundaries; they can also be added, deleted, or modified in the overview 'constraint management'.
3. Respond in simple human language, explaining what you did.

Current Task Target:{{.Goal}}

Do not fabricate findings; only answer based on real data returned by tools.`

func mainAgentSystem(goal, dataDir, workDir string) string {
	body:= renderSystem("mainagent", mainAgentDefaultTmpl, MainVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir)
}

// Chat handles one human message and returns the assistant reply. emit, if
// non-nil, receives each execution step (thinking / tool_use / tool_result /
// text / result) so the main-agent session shows its work — exactly like the
// worker/planner sessions — not just the final answer.
func (m *MainAgent) Chat(ctx context.Context, taskID int64, mainSeg int, as *db.AssetStore, ts *db.ExplorationStore, goal, message string, emit func(db.Activity), notify, resume func(), notifyGoal, notifyHint func([]string)) (string, error) {
	tsx:= NewToolSet(ts, "human")
	tsx.SetFindingRecorder(m.findingRecorder)
	if as!= nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.SetNotify(notify) // General wake-up (write operations without a dedicated callback go through this, debounced)
	tsx.SetResumeTask(resume) // set_goals New target → 'Pull completed/paused tasks back to running
	tsx.SetNotifyGoal(notifyGoal) // set_goals New target → Log for planner: 'A new target was added by a person:...'Trigger
	tsx.SetNotifyHint(notifyHint) // add_hint New prompt → Note to planner: 'A new strategic hint was added by a person:...'Trigger
	tsx.steerWork = m.steerWork // enable steer_work tool (nil = unavailable)
	// Domain tools + Basic default toolset (Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash)
	// When asset coverage function is off, exclude add_task_scope/list_untested_assets (not included in prompt).
	base:= append(tsx.DropCoverageTools(tsx.MainAgentTools()), actool.DefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	tools, def, cleanup:= AugmentTools(ctx, "mainagent", base)
	defer cleanup()
	// This task's working directory. <workDir>/tasks/<taskID>, set it up first.
	mainDir:= ensureRunDir(m.workDir, taskID, 0)
	ctx = intercept.WithReviewWorkingDirectory(ctx, mainDir)
	system, boundary:= deferredSystem(mainAgentSystem(goal, m.workDir, mainDir), def)
	opts:= agentcore.Options{
		Provider: m.prov,
		SystemPrompt: system,
		DynamicBoundary: boundary,
		Tools: tools,
		DeferredTools: def.Deferred,
		UnlockSet: def.Unlock,
		PermissionMode: permission.ModeBypass,
		EnableWebFetch: true, // Walk record proxy to leave traces; load proxy CA to verify MITM-resigned HTTPS certificates
		WebFetchProxy: m.proxyAddr,
		WebFetchCACert: m.proxyCACert,
		// Online Search(Optional). ddgs No key; brave-free needs BraveKey; tavily needs TavilyKey.
		// WebSearchProxy Is an independent exit proxy(http/https/socks5), Unrelated to the MITM proxy that records traffic; if empty, it will connect directly.
		EnableWebSearch: m.webSearch.Enabled,
		WebSearchBackend: m.webSearch.Backend,
		BraveSearchAPIKey: m.webSearch.BraveKey,
		TavilySearchAPIKey: m.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: m.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey: m.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel: m.webSearch.DeepSeekModel,
		WebSearchProxy: m.webSearch.Proxy,
		BashEnv: proxyEnv(m.proxyAddr, m.proxyCACert), // Bash Subcommands default to using a proxy+Trust CA
		WorkingDir: mainDir, // Working directory for this task. <workDir>/tasks/<taskID>
		ToolOutputDir: cmdOutDir(mainDir),
		MaxTurns: m.maxTurns, // 0 = unlimited (configurable in agent management)
		Compaction: compactionConfig(m.compactionWindow()), // long chats stay within the window
		Todos: actool.NewTodoStore(), // Session-level temporary todo (TodoWrite), purely for planning; discarded on exit
		// Budget hit(steps)→ SDK Wrap Up:Output a sentence summarizing progress to the user. Prompt and end round numbers can be edited in the background(Default 10 rounds).
		Settlement: wrapupSettlement("mainagent", nil),
		NonStreaming: m.nonStreaming(), // This profile walks the Provider when not using streaming..Complete
		MaxTokens: m.maxTokens(), // 0 = No upper limit,Determined by the server's default value
	}
	if m.tx!= nil { // persist raw human↔AI conversation; one accumulating file per segment
		opts.Transcript = m.tx
		// Segment 0 keeps the legacy "exp%d-main" name so existing transcripts still
		// load; each new session (seg>=1) gets its own file for a clean context.
		opts.SessionID = fmt.Sprintf("exp%d-main", ts.ID())
		if mainSeg > 0 {
			opts.SessionID = fmt.Sprintf("exp%d-main-s%d", ts.ID(), mainSeg)
		}
	}
	// Experimental Features:After enabling, context compression is taken over by noa(Archiving Concentrated At <workDir>/noa/<SessionID> down,persistent).
	// session id Follows the same rules as transcript.(Segment Awareness),Align archiving with recovery.
	noaSession:= fmt.Sprintf("exp%d-main", ts.ID())
	if mainSeg > 0 {
		noaSession = fmt.Sprintf("exp%d-main-s%d", ts.ID(), mainSeg)
	}
	enableNoa(&opts, m.noaEnabledFn, m.workDir, noaSession, noaWarn(noaSession))
	ctx = attachSideCapture(ctx, &opts)
	s:= agentcore.NewSession(opts)
	defer s.Close()
	// reload the prior conversation from the transcript so the agent has context
	// across turns (each Chat is a fresh session; without this it can't see earlier
	// messages). First turn: no file yet → Resume loads nothing and proceeds.
	if m.tx!= nil {
		_ = s.Resume(opts.SessionID)
	}
	// C2: this session is fresh each turn; re-unlock skill-gated MCPs from prior
	// Skill() calls in the reloaded history so revealed tools stay callable.
	seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
	text, _, err:= captureRunSession(ctx, s, message, func(r db.Activity) {
		if emit!= nil {
			r.Worker = "mainagent"
			emit(r)
		}
	})
	return text, err
}
