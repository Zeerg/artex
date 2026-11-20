package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/Zeerg/artex/db"
	"github.com/Zeerg/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// Planner is the event-driven LLM planner (docs §4.3): each time the asset or
// exploration graph changes (debounced), it reads the exploration route, queries
// assets, judges whether the task goal is met, and emits 0..N exploration intents
// into the frontier. It is the sole intent generator.
type Planner struct {
	findingRecorder FindingRecorder
	prov llm.Provider
	model string
	tx *transcript.Store // raw LLM conversation persistence (nil = off)
	window int // context window in tokens (for compaction)
	windowFn func() int // optional dynamic task-chain minimum
	maxTurns int // max agent turns per run (0 = unlimited)
	killWork func(intentID int64) error // engine callback to terminate a running work (nil = off)
	steerWork func(intentID int64, msg string) error // engine callback to steer a running work mid-run (nil = off)
	proxyAddr string // recording proxy for WebFetch (empty = direct)
	proxyCACert string // recording proxy's CA cert path (HTTPS verify)
	webSearch WebSearchOpts // web_search tool backend selection (off by default)
	workDir string // shared work dir (surfaced in prompt as artifact-output target)
	injectConstraints func() bool // resolver: inject task operation constraints into system prompt? (nil = yes)
	nonStreamingFn func() bool // resolver: use non-streaming (Complete) path? (nil = streaming)
	noaEnabledFn func() bool // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn func() int // resolver: per-reply output cap (nil/0 = send no cap)
	compactor *Compactor // cold-node compaction (§7); nil = disabled

	// todos keeps ONE plan-scratchpad per task (keyed by exploration id) so the
	// planner's multi-step plan survives across wake-ups — each Plan() is a fresh
	// session, but the shared store lets it record a serial exploit chain once and
	// dispatch it step-by-step over rounds instead of front-loading it in parallel.
	todoMu sync.Mutex
	todos map[int64]*actool.TodoStore
}

func NewPlanner(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *Planner {
	return &Planner{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, todos: map[int64]*actool.TodoStore{}}
}

func (p *Planner) SetCompactionWindowResolver(fn func() int) { p.windowFn = fn }

// SetCompactor wires the cold-node compactor (cold-digest §7). Called each
// planner wake-up to advance the round counter, maintain cold stamps, and
// (off the hot path) fold cold nodes into digests. nil = feature disabled.
func (p *Planner) SetCompactor(c *Compactor) { p.compactor = c }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (p *Planner) SetNonStreaming(fn func() bool) { p.nonStreamingFn = fn }

func (p *Planner) nonStreaming() bool { return p.nonStreamingFn!= nil && p.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (p *Planner) SetNoaEnabled(fn func() bool) { p.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (p *Planner) SetMaxTokens(fn func() int) { p.maxTokensFn = fn }

func (p *Planner) maxTokens() int {
	if p.maxTokensFn == nil {
		return 0
	}
	return p.maxTokensFn()
}

func (p *Planner) compactionWindow() int {
	if p.windowFn!= nil {
		return p.windowFn()
	}
	return p.window
}

// SetProxy points the planner's WebFetch at the recording proxy plus the CA cert
// it trusts to verify HTTPS through it (empty addr = direct).
func (p *Planner) SetProxy(addr, caCert string) { p.proxyAddr, p.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the planner (off by default).
func (p *Planner) SetWebSearch(o WebSearchOpts) { p.webSearch = o }

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the planner system prompt. Read per round so the
// settings toggle takes effect without rebuilding the agent. nil = inject (default).
func (p *Planner) SetConstraintInject(fn func() bool) { p.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (p *Planner) wantConstraints() bool { return p.injectConstraints == nil || p.injectConstraints() }

// todoFor returns the task's persistent planning todo store, creating it on first
// use. Shared across all of this task's planner wake-ups.
func (p *Planner) todoFor(expID int64) *actool.TodoStore {
	p.todoMu.Lock()
	defer p.todoMu.Unlock()
	s:= p.todos[expID]
	if s == nil {
		s = actool.NewTodoStore()
		p.todos[expID] = s
	}
	return s
}

// SetKillWork wires the engine's per-work terminate callback so the planner's
// kill_work tool can stop a single running worker.
func (p *Planner) SetKillWork(fn func(intentID int64) error) { p.killWork = fn }

// SetSteerWork wires the engine's per-work steering callback so the planner's
// steer_work tool can inject a mid-run course-correction into a running worker.
func (p *Planner) SetSteerWork(fn func(intentID int64, msg string) error) { p.steerWork = fn }

// renderPlannerTodos formats the persistent planning todo for injection into the
// wake-up prompt (empty when there are no todos yet — first wake-up).
func renderPlannerTodos(items []actool.Todo) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[Your pending tasks (cross-awakening retained, what you wrote in the last round)]: \n")
	for _, it:= range items {
		mark:= map[actool.TodoStatus]string{actool.TodoPending: "☐", actool.TodoInProgress: "▶", actool.TodoCompleted: "✔"}[it.Status]
		if mark == "" {
			mark = "☐"
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", mark, it.Content))
	}
	b.WriteString("Proceed based on this: Only dispatch intents for the next step if the [prerequisite steps have been completed / their dependent facts already exist]. Use TodoWrite to update the list (mark steps that have been satisfied by facts as completed). Do not reschedule steps that are already pending/in_progress in the list.")
	return b.String()
}

// TriggerEvent describes what concretely caused this planning round to fire, so
// the planner looks first at the actual change instead of re-scanning the whole
// overview. Kind:
//
//	"done" — a worker finished intent IntentID (its output conclusion is fetched).
//	"finding" — a worker reported a finding on intent IntentID (Detail = Failure).
//	"goal" — the human (via Main agent's set_goals) added one OR MORE goals in a
//	 single call (Goals = New target text added in this session, 1+ Semicolon; set_goals supports batch processing.).
//	"goal_deleted" — the human deleted a goal from Overview of Target Management (Detail = Deleted target text).
//	"goal_edited" — the human edited a goal from Overview of Target Management (OldGoal→NewGoal text).
//	"cancelled" — the human deleted intent IntentID (Detail = deletion reason). The intent is
//	 stopped (not deleted) and the reason is attached to it as a fact.
type TriggerEvent struct {
	Kind string
	IntentID int64
	Detail string
	Summary string // Kind=="cancelled" Dedicated: Delete the captured intent summary before deletion (after true deletion, the node no longer exists and cannot be queried again)
	Goals []string // Kind=="goal" Dedicated: New target text for set_goals this time (1 or multiple)
	OldGoal string // Kind=="goal_edited" Dedicated: Original target text before modification
	NewGoal string // Kind=="goal_edited" Dedicated: Modified target text
	Hints []string // Kind=="hint" Dedicated: New hint text added by this add_hint (one or more)
}

// renderTriggers spells out the change(s) that fired this round: for a finished
// worker — which intent + its output conclusion; for a finding — which intent +
// what was found. Empty for time/heartbeat wakes. Reads the store (best-effort;
// a blank field never blocks the round).
func renderTriggers(ts *db.ExplorationStore, evs []TriggerEvent) string {
	if len(evs) == 0 || ts == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[This round's actual changes triggered this time (look here first, then decide if to adjust the direction):")
	for _, ev:= range evs {
		switch ev.Kind {
		case "goal":
			if len(ev.Goals) == 1 {
				b.WriteString(fmt.Sprintf("\n- A person (main agent) added a new target:%s —— New goals to be achieved, please supplement exploration directions based on this (if there is no corresponding intent).", ev.Goals[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- A person (main agent) added %d One Target:%s —— All are new goals to be achieved. Please provide exploration directions for targets without corresponding intents one by one.", len(ev.Goals), strings.Join(ev.Goals, "; ")))
			}
		case "hint":
			if len(ev.Hints) == 1 {
				b.WriteString(fmt.Sprintf("\n- A person (main agent) added a strategic hint:%s —— Already pinned to exploration graph. Adjust/supplement exploration direction based on this (if no corresponding intent exists).", ev.Hints[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- A person (main agent) added %d Strategic Tip:%s —— All are attached to the exploration graph. Adjust and supplement exploration directions accordingly.", len(ev.Hints), strings.Join(ev.Hints, "; ")))
			}
		case "goal_deleted":
			b.WriteString(fmt.Sprintf("\n- Someone deleted this target:%s —— This target has been removed, so please re-judge the remaining targets/directions (no need to assign intents for it).", ev.Detail))
		case "goal_edited":
			b.WriteString(fmt.Sprintf("\n- A human modified the target, from 'to%s'Change to ''%s'—— Please adjust exploration direction based on the new target (stop dispatching if original direction is no longer applicable).", ev.OldGoal, ev.NewGoal))
		case "finding":
			b.WriteString(fmt.Sprintf("\n- Intent #%d (%s)The worker reported a finding:%s", ev.IntentID, intentSummary(ts, ev.IntentID), ev.Detail))
		case "cancelled":
			// Intent content prioritizes using Summary captured during deletion (node no longer exists after true deletion, intentSummary not found).
			sm:= ev.Summary
			if sm == "" {
				sm = intentSummary(ts, ev.IntentID)
			}
			b.WriteString(fmt.Sprintf("\n- Intent #%d Deleted by user, intent content is:%s, Deletion reason:%s. This intent has been deleted (no longer executed); please re-plan accordingly.", ev.IntentID, sm, ev.Detail))
		default: // "done"
			b.WriteString(fmt.Sprintf("\n- Intent #%d (%s)When a worker ends, output the conclusion:%s", ev.IntentID, intentSummary(ts, ev.IntentID), workerOutput(ts, ev.IntentID)))
			if fids:= factIDsYielded(ts, ev.IntentID); fids!= "" {
				b.WriteString(fmt.Sprintf("; New facts generated by this intent:%s ", fids))
			}
		}
	}
	b.WriteString("\n (Full details can be checked with node_detail / get_worker_output / list_findings.")
	return b.String()
}

// factIDsYielded lists the fact ids an intent produced this run as "#12, #15", so the
// planner can jump straight to the round's incremental facts. Empty (best-effort) when
// the intent yielded no facts or the lookup fails.
func factIDsYielded(ts *db.ExplorationStore, id int64) string {
	ids, err:= ts.FactsYielded(id)
	if err!= nil || len(ids) == 0 {
		return ""
	}
	parts:= make([]string, len(ids))
	for i, fid:= range ids {
		parts[i] = fmt.Sprintf("#%d", fid)
	}
	return strings.Join(parts, ", ")
}

// intentSummary reads an intent node's one-line summary (best-effort, "?" on miss).
func intentSummary(ts *db.ExplorationStore, id int64) string {
	n, err:= ts.GetNode(id)
	if err!= nil || n == nil {
		return "?"
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok:= p["summary"].(string); ok && s!= "" {
			return s
		}
	}
	return "?"
}

// workerOutput returns the finished worker's conclusion for an intent — the last
// 'result' (else 'text') activity's full detail, truncated. Same source get_worker_output uses.
func workerOutput(ts *db.ExplorationStore, id int64) string {
	acts, _, err:= ts.ActivityList(&id, 0, 1000)
	if err!= nil {
		return "(Failed to Get Output)"
	}
	var pick *db.Activity
	for i:= range acts {
		if acts[i].Kind == "result" {
			pick = &acts[i]
		} else if acts[i].Kind == "text" && pick == nil {
			pick = &acts[i]
		}
	}
	if pick == nil {
		return "(This work has no output records yet.)"
	}
	out, _:= ts.ActivityDetail(pick.ID)
	if out == "" {
		out = pick.Summary
	}
	return truncOutput(out, 800)
}

// truncOutput caps a worker-output blob so the trigger context doesn't bloat the
// system prompt every round; full text is one get_worker_output call away.
func truncOutput(s string, n int) string {
	r:= []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " … (Truncated, see get_worker_output for full details)"
}

// renderGraphOverview folds the pre-computed graph_overview snapshot into the
// wake-up prompt so the planner starts each round with the full situation in
// hand — saving the round-trip it would otherwise spend calling the tool. It is
// the exact same JSON graph_overview would return; deeper detail is still one
// tool call away (node_detail / list_facts / ...).
func renderGraphOverview(data map[string]any) string {
	b, err:= json.Marshal(data)
	if err!= nil {
		return "" // fall back to the model calling graph_overview itself
	}
	return "\n\n[Current Situation (graph_overview prefetch, same as calling the tool; details can be fetched on demand with node_detail/list_facts etc.):\n" + string(b)
}

// plannerDefaultTmpl is the built-in EDITABLE body (Segment [A]) of the planner prompt,
// seeded into agent_prompts. Goal is a {{.Goal}} template var; the Intermediate product output normalization
// tail is code-owned (artifactSpec) and appended by plannerSystem after rendering.
const plannerDefaultTmpl = `You are a cybersecurity platform authorized penetration testing system."Planner", Frequently awakened (triggered when the diagram changes). Responsibility: Read the situation. → Identify Target → **Only when there are new directions not yet covered**Supplement exploration intents. You are a planner, not an executor: all products from this round can only be [generate/clear intent] or [determine target], never do the work in plan.

Task objective:{{.Goal}}

**How many intents should be produced in this round (think clearly about this first)**:
- **Hard limit (highest priority)**: As long as [goal not achieved] and [no current open or running intent] (frontier_open=0 And if running_intents is empty), this round must produce at least one intent that advances towards the target.——When there are no running work items or no direction in the queue, produce 0 intent.=Task on hold; even if a known direction exists, only in recent_done, open a new one or continue with an existing one based on the done/exhausted/blocked judgment.
- Beyond the hard limit,**Producing 0 intents is a normal result, but there must be a valid reason.** (Is not"Less is more stable"(The Default):①**Covered**——All directions you thought of are already being handled by open/running intents (rephrasing existing intents is a serious error).②**Wait for dependencies**——The next step depends on the output of the currently running work item, which hasn't been produced yet (at this point, strict enforcement would cause downstream tasks to spin without a prerequisite, so wait for the next wake-up and graph update before dispatching).
- Conversely: there must be [uncovered and not dependent on running work] new directions, or if the goal is not achieved and there are still untested areas within the scope, dispatch——Don't treat 0 intent as a lazy default.

**Decision flow each time the system wakes up**:

1. **The full situation is appended below this prompt** (It's the return of graph_overview, no need to call it again): task (original title).+Target/Root node), asset count, goals+Status, open/running/recent_done intents, sites_without_endpoints (sites without endpoints, hinting at potential probing directions), facts (exploration facts, distinct from vulnerabilities), recent_facts ({id,summary,confidence?}).
 - **range**: Exploration nodes (goals/intents/facts/findings) only contain this task;**Global sharing of asset graph.** (Multiple tasks share the same set, asset count is global within the scope and not unique to this task)——Ignore assets unrelated to this task
 - **Lineage**: Each intent includes parents (upstream: derived from which facts/intents) and yields (downstream: produced which facts/discoveries), each recent_fact with from_intent; based on this, understand"Which facts come from which direction, whether new directions can be synthesized".
 - **Negative/unsure observation** (recent_facts in"Port closed/injection impossible.") is worker observation, not a definitive conclusion: verify before node_detail(id) View evidence.——evidence Firm, confidence=observed Only considered temporarily blocked if means are exhausted; evidence missing or just"Looks like/only probe once", Or confidence=inferred Handle as 'unexplored' if so. If within range and no other intent overrides it, dispatch a re-examination intent to verify or refute.**No more than one recheck in the same negative direction**; If re-evaluated and still negative with reasonable evidence, respect that conclusion and do not dispatch again.
 - **Only call for deeper details when needed**: list_facts (Pagination, newest first, default 20, can be filtered with q, paginated with before, with total/has_more), list_findings (all vulnerabilities), node_detail(id) (Complete evidence/details; list/recent_facts only provide summaries); list_assets (pull: q search, type/company_id/task_id filter, pagination, or id/ids direct retrieval); asset_neighbors. Assets are globally shared, don't default to pulling full data.

2. **Identify target (core responsibility)**: goals Field already contains target and status; for unachieved goals proven by a discovery/fact, call prove_goal.(goal_id, evidence_id, reason) Mark met.**When you mark the exact last incomplete target, the system automatically determines that the entire task is complete.**——The examiner is driven by one prove_goal at a time, no other"one-click completion"Methods.
 - ⚠️ **Quantitative acceptance verification (no premature stamping allowed).**: If the target contains quantifiable conditions (coverage reaches X%)%, When proving a goal with N flags or gaining certain permissions, you must verify the actual values in graph_overview (coverage) before prove_goal..pct, findings_total Counting etc.): If not met, [prohibit] prove_goal, reassign intent to make up the difference; do not kill"Basically achieved / core already taken"Mark as met by default. Example: Require coverage 100% And actual test coverage.pct=40% → Not achieved, continue dispatching supplementary test intents.

3. ** (Optional, only for initial setup or extremely lightweight probes.**: Only use Bash or similar for minimal, read-only probing of targets (e.g., 1) when there are almost no facts (recent_facts is mostly empty, task just started), and the initial intent cannot be made specific based on the situation.–2 Try curl to see the homepage/fingerprint).**The only legitimate output is a more precise intent description**——Not a discovery/validation/utilization of a vulnerability, nor an enumeration result of endpoints/directories/parameters (those are worker's work, written as intent to be dispatched). Three hard boundaries:
 - Facts (facts) already produced by worker in graph>0 / recent_facts Non-empty)→[Prohibited] from self-probing; all judgments are based on existing facts. The output of this round can only be"Dispatch New Intent"Or"End"; Want To Dig Deep Into A Clue → Assign the intent to let worker query, not curling themselves.
 - Even at the start, probe no more than ≤3 Stop now, just to clarify the initial intent; once you realize you're"Dig deeper to verify"rather than"Quickly determine direction" (Enumerate endpoints/directories one by one, try ids one by one, decode chains, repeatedly probe the same interface, any injection/privilege escalation/vulnerability testing verification——All worker's heavy work), stop immediately and write as intent.
 - There's no need to probe if it can be determined from existing facts/situations.

4. **Decide which new directions to supplement.**: **Here's the"restrain"Only refers to [non-repeated existing intents], not"Dispatch as few as possible"**——Default follow-up when the goal is not achieved"For how to get closer to the target, what deeper and more ruthless attacks have not yet been covered.", Rather than"Can it be wrapped up?". The intent is an 'open exploration direction' (not a fixed type/menu), combining known facts, assets, and goals to self-assess the direction, then sequentially compare with open + running + recent_done Comparison:
 - Already covered by open/running → No longer generated (Currently processing).
 - Appeared in recent_done → **First check the state of this intent (each with one) to determine how to stop it, then decide**:
 · **done (Runs normally)**: Covered → Do not reschedule; whether it's a dead end depends on the fact conclusions yielded by 'yields', rather than the state. Only reschedule if there is a [materially new mechanism] (new facts/assets/parameters/clearly different approaches); also, clearly state what differs from the last one in the summary."Try again, maybe it works"Not counted, no retry allowed.
 · **exhausted (Budget exhausted, interrupted halfway, only write partial results) / blocked (model or network failure, barely probed)**: Both are unfinished, information incomplete——First, use get_worker_trace / get_worker_output to see where it actually got to and where it stalled, then choose from the following options: close to breaking budget → dispatch"Continues from last progress"; Pure external faults that didn't run (blocked is common)→ Directly reassign in the same direction; each time stuck at the same place → Change tactics/strategy. The basis is always the real progress in trace, not the state itself.
 - A completely new direction with no intent coverage. → Generate.
 - All known directions are covered by the still open/running intent. → Do not generate, directly end (if there are running/enqueued work, wait for them to progress); but if only recent_done is covered, no open/running tasks remain, and the goal has not been achieved → Must open or continue dispatching according to the top hard bottom line.
 - **Depth prioritized over coverage**: coverage A lower limit/acceptance item, not an exploration target itself; after discovering high-value entry points (possibly leading to RCE/privilege escalation/data exfiltration), prioritize assigning intentions to drill that path[deeply]instead of spreading coverage and shallow testing each asset.
 - **Maintain diverse routes, don't prematurely converge**: If the current intent is stuck on the same route/entry and there are [essentially different] unexplored directions (another entry point/another type of asset/another exploitation chain), prioritize filling that divergent direction instead of adding synonyms on the same line (look at substance differences, not wording). If that divergent direction has already been covered by existing intents, do not generate it. The ideal scenario is 2–3 Coexisting routes with different mechanisms (like"Start from Upload Chain"And"Attack from authentication bypass"), Only gather resources after evidence is submitted for 'target proximity'.**But diversity must always obey the top-level [Operation Constraints].**: Excludes entry points/ports/hosts/operations that are constrained and excluded, even if fundamentally different, never generating intent.

 **Serial exploitation chain: Step-by-step dispatch, not split into parallel.** Strong Dependency Serial Chain (①→②→③, Each subsequent step depends on the actual output of the previous step): do not issue in parallel (downstream cannot receive non-existent prerequisites and will only repeat/idle); use TodoWrite to record the entire chain as a todo item (one per step), dispatching only this round"Prerequisites Already Met"The step (usually the first) that produces fact, next wake up after it produces fact (hint will carry to-do list), then dispatch next step and mark completed."Same thing"Don't split into two (("Confirm trigger point"And"Trigger Trigger Point"The same step); only dimensions that are [parallel and independent] (like enumerating multiple unrelated endpoints) use multi-intent parallelism.

5. **Submit**: Use [one] add_intent to batch submit new directions filtered out (intents array, up to 4 highest-value ones, don't call multiple times):
 - **summary**: A single natural language description of this direction (complete test target address + What to do + Why, it doesn't use fixed categories; deduplication mainly relies on its comparison with existing intents.
 - **asset_ids**: Target asset ID to test/attack in this direction (try to pass, 0/1/multiple, from list_assets).——Whenever the direction is around specific assets (site/interface/parameter/host), it must be passed to cover deduplication, connecting asset chains, and across multiple assets. For purely global reconnaissance with no specific assets, leave it empty.
 - **parent_ids**: Which upstream nodes contributed to this direction (optional, 0/1/multiple)——Pass all facts that combine to form an intent; if derived from an upstream intent/discovery, also pass its id. Leave the top-level new direction empty.

Unique, not forced; but if the goal is not achieved and there are unexplored deeper plays, that team will be dispatched. Concise, focused, efficient.`

func plannerSystem(goal, dataDir, workDir string) string {
	body:= renderSystem("planner", plannerDefaultTmpl, PlannerVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir)
}

// Plan runs one planning round. emit, if non-nil, receives the planner's execution
// steps (so users can see how it reads the situation and judges goals — the
// planner is the intent generator and was previously a black box). Returns whether
// the planner judged the goal met.
// triggers carries the concrete change(s) that fired this round — worker(s) done
// and/or finding(s) reported (may be several — the engine debounces a burst; empty
// for time/heartbeat wakes). They are spelled out at the top of the prompt so the
// planner looks first at the actual change (which intent, its output/finding).
func (p *Planner) Plan(ctx context.Context, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, goal string, triggers []TriggerEvent, emit func(db.Activity)) (met bool, reason string, err error) {
	// cold-digest §2.3/§7: advance this task's planner-round counter, maintain the
	// cold_since_round stamps, and (if a threshold is hit) kick off background
	// compaction. Synchronous part is cheap (a few queries); the LLM compaction
	// runs in a detached goroutine so it never adds latency to this round.
	p.compactor.OnPlannerRound(ctx, ts)
	tsx:= NewToolSet(ts, "planner")
	tsx.SetFindingRecorder(p.findingRecorder)
	if as!= nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.killWork = p.killWork // enable kill_work tool (nil = unavailable)
	tsx.steerWork = p.steerWork // enable steer_work tool (nil = unavailable)
	if origin, _:= ts.OriginFactID(); origin > 0 {
		tsx.SetOwnerNode(origin) // planner-side anchors default to the task root (origin fact)
	}
	// Domain tools + Basic default toolset (Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash)
	// When asset coverage function is off, exclude add_task_scope/list_untested_assets (not included in prompt).
	base:= append(tsx.DropCoverageTools(tsx.PlannerTools()), actool.DefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	tools, def, cleanup:= AugmentTools(ctx, "planner", base)
	defer cleanup()
	// Key Situation (Most recent intent) + Move the fully fetched graph to [this round's user input].(See the input below.), system
	// Keep only static planning body. move-out makes system stable each round, more conducive to caching; the cost is that if a single round becomes longer, the situation may
	// Compressed by compaction (planner's single round is usually short, risk low). situational will be appended to the input below.
	situational:= renderTriggers(ts, triggers) + renderGraphOverview(tsx.graphOverviewData())
	// Task-level deadline / terminal mode(Injected via ctx.,See taskclock.go). The final round timed out the task.
	// planner Endings as the [current operation instructions] to be appended to the current user input(Follow situational),Let It Only Do The Final
	// Target determination, no new intent produced.
	tc:= taskClockFrom(ctx)
	if tc.Final {
		situational += "\n\n[Task conclusion (special instructions for this round, overriding the usual planning process):" + resolveTaskTimeoutWrapup("planner")
	}
	// This task's working directory. <workDir>/tasks/<taskID>, set it up first.
	taskDir:= ensureRunDir(p.workDir, taskID, 0)
	ctx = intercept.WithReviewContext(ctx, taskDir, intercept.ReviewBackground{})
	sysBody:= plannerSystem(goal, p.workDir, taskDir)
	if p.wantConstraints() {
		sysBody += constraintBlock(ts) // Operation Constraints(If there is)Injection system prompt,Define exploration boundaries
	}
	system, boundary:= deferredSystem(sysBody, def)
	// planner No self-clock budget.;When there is a deadline, constrain MaxDuration to the remaining time.,Let the running plan rounds continue
	// Task wraps up when due.(Due to timeout→Task Timeout Phrase,Due to steps→per-run Word).
	maxDur, clamped:= clampMaxDuration(tc.DeadlineUnix, 0)
	settle:= wrapupSettlement("planner", nil)
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("planner", nil, clamped)
	}
	opts:= agentcore.Options{
		Provider: p.prov,
		SystemPrompt: system,
		DynamicBoundary: boundary,
		Tools: tools,
		DeferredTools: def.Deferred,
		UnlockSet: def.Unlock,
		PermissionMode: permission.ModeBypass,
		EnableWebFetch: true, // Walk record proxy to leave traces; load proxy CA to verify MITM-resigned HTTPS certificates
		WebFetchProxy: p.proxyAddr,
		WebFetchCACert: p.proxyCACert,
		// Online Search(Optional). ddgs No key; brave-free needs BraveKey; tavily needs TavilyKey.
		// WebSearchProxy Is an independent exit proxy(http/https/socks5), Unrelated to the MITM proxy that records traffic; if empty, it will connect directly.
		EnableWebSearch: p.webSearch.Enabled,
		WebSearchBackend: p.webSearch.Backend,
		BraveSearchAPIKey: p.webSearch.BraveKey,
		TavilySearchAPIKey: p.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: p.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey: p.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel: p.webSearch.DeepSeekModel,
		WebSearchProxy: p.webSearch.Proxy,
		BashEnv: proxyEnv(p.proxyAddr, p.proxyCACert), // Bash Subcommands default to using a proxy+Trust CA
		WorkingDir: taskDir, // Working directory for this task. <workDir>/tasks/<taskID>
		ToolOutputDir: cmdOutDir(taskDir),
		MaxTurns: p.maxTurns, // 0 = unlimited (configurable in agent management)
		MaxDuration: maxDur, // 0=Unlimited;When there's a deadline=Time remaining until the deadline
		Compaction: compactionConfig(p.compactionWindow()),
		// Cross-wakeup shared planning tasks: Keep the serial chain between rounds (session is new, store is not).
		Todos: p.todoFor(ts.ID()),
		// Hit the current round step budget→ SDK Wrap Up:Land the conclusions made this round(This dispatch's add_intent,
		// Prove_goal that can be proven, serial chain record TodoWrite),Rather than stop planning——planner It will still be repeatedly awakened.
		// clamped(and is constrained by the task deadline,)Switch to PromptByReason(See wrapupSettlementForTask).
		Settlement: settle,
		NonStreaming: p.nonStreaming(), // This profile walks the Provider when not using streaming..Complete
		MaxTokens: p.maxTokens(), // 0 = No upper limit,Determined by the server's default value
	}
	if p.tx!= nil { // persist raw LLM conversation; one accumulating file per task's planner
		opts.Transcript = p.tx
		opts.SessionID = fmt.Sprintf("exp%d-planner", ts.ID())
	}
	// Experimental Features:After enabling, context compression is taken over by noa(Archiving Concentrated At <workDir>/noa/<SessionID> down,persistent).
	noaSession:= fmt.Sprintf("exp%d-planner", ts.ID())
	enableNoa(&opts, p.noaEnabledFn, p.workDir, noaSession, noaWarn(noaSession))
	// Situation (recently completed intent + The complete graph is now integrated with this round's user input (see below input). There is still
	// command + Cross-awake to-do items (todo is a model's own planning note, renewable, place it under user).
	// Opening remarks divided into two types based on whether there are specific changes this round: with changes → Points to the section below [Actual Changes]; No changes
	// (Heartbeat timed inspection / hint / recovery etc.) → Don't lie about"Graph Has Changed",Switch to prompt for additional review of running intents.
	lead:= "Based on any specific changes (see below for 'Actual Changes Triggering This Round'), plan the next step:"
	if len(triggers) == 0 {
		lead = "This round is**Scheduled inspections (heartbeat at point) / no specific change signal.**Wakeup——The diagram may not have new changes. Alongside reviewing the running intent: if progress is stagnant or off course, use steer_work to correct it; if the entire direction is wrong, use kill_work to stop the loss; then re-assess the target and decide whether to adjust the direction:"
		// Wake on heartbeat/without change,If the entire graph has no open or running intent, → Exploration on hold(No worker is running,
		// No queue direction). Clearly inform the planner and force it to come up with new directions this round,Don't just re-check after running intent for a round.
		if active, err:= ts.HasActiveIntent(); err == nil &&!active {
			lead = "This round is**Scheduled Inspection (Heartbeat at the right time)**Wakeup,And current**There are no more open or running intents.**——No worker running, no pending direction,Exploration has stalled. You**must**Produce one or more actions that advance towards the target and align with the existing intent in the graph.**no duplicates**The New Intent(Must not produce 0 intent);First, determine if the target has been achieved based on the current situation.,If not achieved, immediately adjust the direction:"
		}
	}
	input:= lead + situational + "\n\nBased on the currentsituation, determine the target. When the target is[truly achieved](already obtained target results/confirmed target vulnerabilities), use prove_goal to mark one by one.**Hard limit: As long as the target has not been achieved and there are no open or running intents (frontier_open).=0 And running_intents is empty), this round must produce at least one intent that advances the target.——No running work can wait, no directions in queue, output 0 intent.=Task paused. Only when there is an existing open/running intent advancing or the target has been achieved can this round not produce a new intent.**" +
		renderPlannerTodos(opts.Todos.List())
	// MaxDuration Now it will interrupt running tools at the scheduled time and finish them in place(On live ctx),Single round stuck no more
	// Bypass start and end,No need for external hard ctx fallback. ctx only carries pause / kill / shutdown.
	_, _, err = captureRun(ctx, opts, input,
		func(r db.Activity) {
			if emit!= nil {
				r.Worker = "planner" // planner activity has no intent_id (it generates them)
				emit(r)
			}
		})
	return tsx.GoalMet, tsx.Reason, err
}
