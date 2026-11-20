package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Zeerg/artex/db"
	acperm "github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// compactIntents distills intents to {id, summary, state, asset_ids, parents,
// yields} so the planner sees both the direction and its LINEAGE — parents (the
// upstream nodes it derived from: facts/intents/findings) and yields (the facts/
// findings it produced) — without pulling full payloads. parentsOf/yieldsOf are
// built from the exploration edges in graph_overview.
func compactIntents(ns []*db.Node, parentsOf, yieldsOf map[int64][]int64) []map[string]any {
	out:= make([]map[string]any, 0, len(ns))
	for _, n:= range ns {
		var p map[string]any
		_ = json.Unmarshal(n.Payload, &p)
		m:= map[string]any{"id": n.ID, "summary": p["summary"], "state": n.State}
		if n.Inherited {
			m["source_task_id"] = n.SourceTaskID
			m["inherited"] = true
		}
		// asset_ids is the structured "which assets this direction covers" signal for
		// dedup; fall back to legacy payload keys (target_ids plural, then target_id
		// single) so intents stored before the rename still surface their anchors.
		if tg, ok:= p["asset_ids"]; ok && tg!= nil {
			m["asset_ids"] = tg
		} else if tg, ok:= p["target_ids"]; ok && tg!= nil {
			m["asset_ids"] = tg
		} else if tg, ok:= p["target_id"]; ok && tg!= nil && tg!= "" {
			m["asset_ids"] = []any{tg}
		}
		if ps:= parentsOf[n.ID]; len(ps) > 0 {
			m["parents"] = ps // Upstream: This intent is derived from which nodes (multiple facts can jointly produce an intent)
		}
		if ys:= yieldsOf[n.ID]; len(ys) > 0 {
			m["yields"] = ys // Downstream: What facts/discoveries did this intent generate?
		}
		out = append(out, m)
	}
	return out
}

// ToolSet exposes the PG-backed dual graph (asset + exploration) to an LLM agent.
// One ToolSet is created per planner/worker run; per-run signals live here.
type ToolSet struct {
	findingRecorder FindingRecorder
	as *db.AssetStore // asset store (optional; nil = asset tools not available)
	cs *db.CompanyStore // company store (optional)
	ts *db.ExplorationStore
	worker string
	taskID int64 // PG tasks.id; 0 when unknown (tests / orchestrator cross-task reads)
	// coverageDisabled mirrors tasks.coverage_enabled=false. Stored inverted so the
	// zero value (all existing ToolSet constructions) means ENABLED — matching the
	// DB default (true). When true: graphOverviewData drops the coverage block, the
	// auto-scope hook (insertAssets) is skipped, and add_task_scope/list_untested_assets
	// are filtered out of the agent's tool list. The scope field stays regardless.
	coverageDisabled bool
	// ownerNode is the exploration node that writes attach to: assets this run
	// touches get anchored to it as lineage/provenance (NOT visibility — the asset
	// graph is global and shared). Worker = its claimed intent; planner = begin root.
	ownerNode int64
	GoalMet bool
	Reason string
	writes WriteCounts
	// killWork, if set, terminates a running work by intent id (engine callback,
	// wired by the planner). nil = the kill_work tool reports unavailable.
	killWork func(intentID int64) error
	// steerWork, if set, queues a mid-run course-correction for the work running an
	// intent id (engine callback, wired by the planner): the worker injects it before
	// its next tool call and re-plans, without being killed. nil = tool unavailable.
	steerWork func(intentID int64, msg string) error
	// enrich, if set, receives async auto-completion triggers (DNS resolve for a
	// domain, HTTP probe for a site). nil = no engine enrichment.
	enrich EnrichTrigger
	// notify, if set, wakes the task's planner after a graph change that should be
	// re-planned promptly (currently: a new hint). nil = no wake (the hint is still
	// stored and read on the next round triggered by other events). debounced.
	notify func()
	// notifyFinding, if set, wakes the task's planner when this run reports a finding,
	// carrying (intentID, summary) so the round can spell out which intent found what.
	// Wired for workers; nil elsewhere → falls back to notify (bare wake).
	notifyFinding func(intentID int64, summary string)
	// resumeTask, if set, revives the task after a graph change that should make a
	// stopped task run again (currently: set_goals adds a goal). It flips a terminal/
	// paused task back to running and (re)starts the engine loops — a plain notify()
	// can't, because the planner's terminal gate swallows wakes. Wired ONLY for the
	// main agent (human steering); nil for the goals decomposer and workers.
	resumeTask func()
	// notifyGoal, if set, wakes the planner AND records ONE "A person added N targets:…" trigger
	// for a whole set_goals call (batch-aware — one call, one trigger, not one per goal)
	// so the next round spells out the added goals (instead of the planner having to
	// spot new open goals in the overview). Wired ONLY for the main agent; nil for the
	// goals decomposer (round-0 has no running planner to inform) and workers → those
	// fall back to the bare notify.
	notifyGoal func(texts []string)
	// notifyHint, if set, wakes the planner AND records ONE "A person added N strategic prompts:…"
	// trigger for a whole add_hint call (batch-aware — one call, one trigger) so the next
	// round is told the round was fired by a new hint and spells the hint out, instead of
	// the planner having to spot it folded into the graph overview. Wired for the main
	// agent + cross-task orchestration; nil elsewhere → falls back to the bare notify.
	notifyHint func(texts []string)
}

// SetNotifyGoal wires the goal-add trigger callback (see ToolSet.notifyGoal). Set only
// by the main-agent chat, so runtime-added goals are announced to the planner by name.
func (t *ToolSet) SetNotifyGoal(fn func([]string)) { t.notifyGoal = fn }

// SetNotifyHint wires the hint-add trigger callback (see ToolSet.notifyHint). Set by
// the main-agent chat and cross-task orchestration, so a runtime-added hint fires a
// planner round announced by name instead of a bare wake.
func (t *ToolSet) SetNotifyHint(fn func([]string)) { t.notifyHint = fn }

// SetResumeTask wires the task-revive callback (see ToolSet.resumeTask). Set only by
// the main-agent chat, so runtime-added goals can pull a finished task back to running.
func (t *ToolSet) SetResumeTask(fn func()) { t.resumeTask = fn }

// SetNotify wires the planner-wake callback (see ToolSet.notify). Set by callers
// that hold the task handle (main-agent chat, cross-task orchestration).
func (t *ToolSet) SetNotify(fn func()) { t.notify = fn }

// SetNotifyFinding wires the finding-wake callback (see ToolSet.notifyFinding).
func (t *ToolSet) SetNotifyFinding(fn func(int64, string)) { t.notifyFinding = fn }

// EnrichTrigger is the enrichment engine seen from the tool layer (see package
// enrich). Kept as an interface here to avoid coupling agent → enrich.
type EnrichTrigger interface {
	ResolveDomain(id int64, host string)
	ProbeSite(id int64, url string)
}

// WriteCounts breaks down what a worker persisted this run, by node kind, so the
// engine can log an accurate "wrote back" summary instead of lumping assets and
// findings under "facts" (record_fact → Facts, insert_assets → Assets,
// report_finding → Findings; each element of a batch counts once).
type WriteCounts struct {
	Facts int
	Assets int
	Findings int
}

// Total is every node persisted this run, regardless of kind — the
// "explored but persisted nothing" signal (Total == 0).
func (w WriteCounts) Total() int { return w.Facts + w.Assets + w.Findings }

// String renders the per-kind breakdown for logs, e.g. "Fact 1 Asset 25 Vulnerability 0".
func (w WriteCounts) String() string {
	return fmt.Sprintf("Facts%d Assets%d Vulnerabilities%d", w.Facts, w.Assets, w.Findings)
}

// Writes reports what this run wrote back, split by node kind (so the engine can
// tell "explored but persisted nothing" apart from a completed intent, and log an
// honest breakdown instead of calling assets/findings "facts").
func (t *ToolSet) Writes() WriteCounts { return t.writes }

func NewToolSet(ts *db.ExplorationStore, worker string) *ToolSet {
	return &ToolSet{ts: ts, worker: worker}
}

// SetTaskID sets the PG task id on this ToolSet so that report_finding can
// dual-write to the standalone findings table (which survives task deletion).
func (t *ToolSet) SetTaskID(id int64) { t.taskID = id }

// SetCoverageEnabled records whether this task has the asset-coverage feature on
// (default enabled). Passing false makes graphOverviewData omit the coverage block
// and DropCoverageTools filter the two coverage-only tools out of the agent's tool
// list. It does NOT stop scope accumulation: insertAssets' auto-scope hook runs
// either way, because task_scope is the task's range boundary (the filter basis for
// asset queries), not merely a coverage denominator.
func (t *ToolSet) SetCoverageEnabled(enabled bool) { t.coverageDisabled =!enabled }

// CoverageDisabled reports whether the coverage feature is off for this task.
func (t *ToolSet) CoverageDisabled() bool { return t.coverageDisabled }

// coverageOnlyTools are the LLM tools that only make sense when asset coverage is
// on. When the feature is off they are filtered out of the agent's tool list so
// they neither pollute the prompt nor let the model build a disabled denominator.
// add_task_scope is deliberately NOT here: task_scope is the task's range boundary
// (the filter basis for asset queries), not merely a coverage denominator, so the
// agents that ownScope definition keep it either way — in lockstep with insertAssets'
// auto-scope hook, which also runs regardless of the switch.
var coverageOnlyTools = map[string]bool{"list_untested_assets": true}

// DropCoverageTools returns tools with the coverage-only ones removed when this
// task has the feature disabled; otherwise it returns tools unchanged.
func (t *ToolSet) DropCoverageTools(tools []actool.CoreTool) []actool.CoreTool {
	if!t.coverageDisabled {
		return tools
	}
	out:= tools[:0:0]
	for _, tool:= range tools {
		if coverageOnlyTools[tool.Name()] {
			continue
		}
		out = append(out, tool)
	}
	return out
}

// Cross-task reuse: exported accessors returning the per-task tool logic bound to
// THIS ToolSet's store. Host-side orchestration tools build a ToolSet for an
// arbitrary task, then Call these — so cross-task reads/hint reuse the exact
// same logic as the in-task tools. (readTool ignores ToolContext, so Call(...,nil)
// is safe; add_hint is a writeTool but also doesn't deref the context here.)
func (t *ToolSet) GraphOverviewTool() actool.CoreTool { return t.graphOverview() }
func (t *ToolSet) ListFindingsTool() actool.CoreTool { return t.listFindings() }
func (t *ToolSet) GetWorkerTraceTool() actool.CoreTool { return t.getWorkerTrace() }
func (t *ToolSet) ListWorkerTracesTool() actool.CoreTool { return t.listWorkerTraces() }
func (t *ToolSet) SearchWorkerTracesTool() actool.CoreTool { return t.searchAllWorkerTraces() }
func (t *ToolSet) NodeDetailTool() actool.CoreTool { return t.nodeDetail() }
func (t *ToolSet) AddHintTool() actool.CoreTool { return t.addHint() }

// SetEnrich wires the async enrichment engine (DNS/HTTP auto-completion).
func (t *ToolSet) SetEnrich(e EnrichTrigger) { t.enrich = e }

// SetOwnerNode sets the exploration node that writes anchor to (worker: its
// intent node; planner/main: the begin root). Assets created/referenced while
// ownerNode is set are anchored to it as lineage (not visibility).
func (t *ToolSet) SetOwnerNode(id int64) { t.ownerNode = id }

// anchorOwner records a lineage edge from this run's owner node to an asset
// (no-op if unset). Provenance only — the asset graph is global and shared, so
// this no longer affects which assets a task can read.
func (t *ToolSet) anchorOwner(assetID int64) {
	if t.ts!= nil && t.ownerNode > 0 && assetID > 0 {
		_ = t.ts.Anchor(t.ownerNode, assetID)
	}
}

// pid parses an id that may arrive as a JSON number or string ("" / 0 → 0).
func pid(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		v, _:= strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		return v
	}
	return 0
}

// pidList parses a list of ids (number|string), dropping zeros/invalids.
func pidList(raw []json.RawMessage) []int64 {
	var out []int64
	for _, r:= range raw {
		if v:= pid(r); v > 0 {
			out = append(out, v)
		}
	}
	return out
}

func obj(props map[string]any, required ...string) map[string]any {
	m:= map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}
func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func intp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func idp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func readTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		ReadOnly: func(json.RawMessage) bool { return true },
		Concurrent: func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, acperm.Context) acperm.Decision { return acperm.Allowed() },
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

func writeTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		Permissions: func(context.Context, json.RawMessage, acperm.Context) acperm.Decision { return acperm.Allowed() },
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

// readExpTool / writeExpTool build a domain tool whose handler dereferences the
// task-bound ExplorationStore. Two ToolSets carry a nil store: the catalog's
// seed-only shell (never called) and the server-level one behind buildDomainReg,
// which the tools table can bind to ANY agent — including ones that never run
// inside a task (auto/pentest/reporter/Custom agent/side question). Refusing there
// keeps a mis-bound tool a bad tool call; without the guard it was a nil deref,
// and tool handlers run on the harness's own goroutine, so the panic is out of
// reach of every recover() in the server and kills the whole process.
func (t *ToolSet) readExpTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return readTool(name, desc, schema, t.needExploration(name, run))
}

func (t *ToolSet) writeExpTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return writeTool(name, desc, schema, t.needExploration(name, run))
}

// needExploration wraps a handler so it only runs with an exploration store.
// Tools that degrade more usefully than "unavailable" (report_finding points at
// add_task_hint, set_goals/set_constraints at the task itself) keep their own
// bespoke guard instead.
func (t *ToolSet) needExploration(name string, run func(context.Context, json.RawMessage) (actool.Result, error)) func(context.Context, json.RawMessage) (actool.Result, error) {
	return func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
		if t.ts == nil {
			return actool.Errorf(name + " Needs task context (exploration graph): The current agent is not running within any task, cannot get the task's exploration graph, and this tool is unavailable. Use it within a task or switch to cross-task reading tools with task_id (get_task_node_detail / list_task_findings / get_task_graph, etc.)."), nil
		}
		return run(ctx, in)
	}
}

func jsonResult(v any) (actool.Result, error) {
	b, err:= json.Marshal(v)
	if err!= nil {
		return actool.Errorf(err.Error()), nil
	}
	return actool.Text(string(b)), nil
}

// --- read tools (planner + worker) ---

func (t *ToolSet) graphOverview() actool.CoreTool {
	return t.readExpTool("graph_overview",
		"(Explore link graph)Exploresituation-distilled summary: asset counts, sites without APIs, frontier, findings, hints(Strategic hints from human/primary agent to be included when generating intentions.). Call it before planning.",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			return jsonResult(t.graphOverviewData())
		})
}

// graphOverviewData computes the distilled situational snapshot shared by the
// graph_overview tool and the planner's wake-up prompt (which pre-injects it so
// the model needn't spend a turn calling the tool — every plan round starts with
// an empty context and always needs this first).
func (t *ToolSet) graphOverviewData() map[string]any {
	out:= map[string]any{}
	// goals summary folded in so the planner needn't call list_goals each round.
	goals, _:= t.ts.ListByKind(db.KindGoal, 100)
	gsum:= make([]map[string]any, 0, len(goals))
	for _, g:= range goals {
		var p map[string]any
		_ = json.Unmarshal(g.Payload, &p)
		gsum = append(gsum, map[string]any{"id": g.ID, "state": g.State, "text": p["text"]})
	}
	out["goals"] = gsum
	// hints: The human/main agent attaches a graph strategic hint via add_hint; folded in so the
	// planner reads them every round when generating intents (Otherwise only write no read).
	hints, _:= t.ts.ListByKind(db.KindHint, 50)
	hsum:= make([]map[string]any, 0, len(hints))
	for _, h:= range hints {
		var p map[string]any
		_ = json.Unmarshal(h.Payload, &p)
		hint:= map[string]any{"id": h.ID, "state": h.State, "text": p["text"]}
		if findingTrafficBindingEnabled() && p["traffic_refs"]!= nil {
			hint["traffic_refs"] = p["traffic_refs"]
		}
		hsum = append(hsum, hint)
	}
	out["hints"] = hsum
	// lineage from the exploration edges: an intent's parents (what it
	// derived_from — possibly several facts combined) and its yields (the
	// facts/findings it produced). factFrom maps a fact → the intent that
	// produced it. This is the relationship layer the flat lists lacked.
	edges, _:= t.ts.Edges(5000)
	parentsOf:= map[int64][]int64{}
	yieldsOf:= map[int64][]int64{}
	factFrom:= map[int64]int64{}
	for _, e:= range edges {
		switch e.Rel {
		case db.RelDerivedFrom, db.RelSpawns: // upstream: derived_from (fact/finding/intent→intent) or spawns (origin fact→goal, legacy begin→intent)
			parentsOf[e.To] = append(parentsOf[e.To], e.From)
		case db.RelYields: // intent --yields--> fact/finding
			yieldsOf[e.From] = append(yieldsOf[e.From], e.To)
			factFrom[e.To] = e.From
		}
	}
	// cold-digest §6: members folded into an active digest are shown via cold_digests
	// (below), not the flat recent_* lists. `covered` maps member id → its digest id.
	// §6 render-time revival check: a covered member that has become hot again (a new
	// intent derived from it) must reappear this round — so `hidden` folds a member out
	// only when it is covered AND still cold.
	covered, _:= t.ts.CoveredMembers()
	// Render-time hot set (ancestor of a live intent / fact under a live intent).
	// §6 revival check: a covered member that revived (now hot) must NOT stay folded
	// — hidden() only folds a member out when it is covered AND still cold. Computed
	// every round (cheap for real graph sizes); nil map degrades safely.
	var hotAtRender map[int64]bool
	if cg, _, err:= loadColdGraph(t.ts); err == nil {
		hotAtRender = cg.hotSet()
	}
	hidden:= func(id int64) bool { _, c:= covered[id]; return c &&!hotAtRender[id] }
	const openIntentsCap = 30
	fr, _:= t.ts.Frontier(openIntentsCap) // priority DESC, id ASC —— The top N highest priority items; real total count see frontier_open
	out["open_intents"] = compactIntents(fr, parentsOf, yieldsOf)
	all, _:= t.ts.ListByKind(db.KindIntent, 300)
	var running, recentDone []*db.Node
	for _, n:= range all {
		switch n.State {
		case "running":
			running = append(running, n)
		case "done", "blocked", "exhausted":
			if hidden(n.ID) {
				continue // in a cold_digest and still cold — shown via cold_digests (§6.2)
			}
			recentDone = append(recentDone, n) // Latest first (all sorted by id descending); collapsed items removed, output latest N when outputting.
		}
	}
	out["running_intents"] = compactIntents(running, parentsOf, yieldsOf)
	// done_intents_total: Total count of completed intents (done/blocked/exhausted), matching recent_done_intents.
	// Parallel Naming——The latter is just its latest window's truncated view. Two keys side by side are self-descriptive:"Seeing N/total.",
	// Don't let planner deduplicate"Not displayed"treat as"Not Dispatched", No need to explain separately in the prompt.
	if dt, err:= t.ts.CountFinishedIntents(); err == nil {
		out["done_intents_total"] = dt
	}
	// frontier_open: Open intent true total (open_intents is just the top N truncated view).
	if fo, err:= t.ts.CountOpenIntents(); err == nil {
		out["frontier_open"] = fo
	} else {
		out["frontier_open"] = len(fr)
	}
	// findings (confirmed vulns) and facts (worker exploration results) are
	// now distinct node kinds. recent_facts surfaces fact summaries (esp.
	// negative results) so the planner sees them in one call; full content
	// via node_detail(id).
	vulnNodes, _:= t.ts.ListByKind(db.KindFinding, 1000)
	factNodes, _:= t.ts.ListByKind(db.KindFact, 1000) // newest first
	out["findings_total"] = len(vulnNodes) // Confirm total number of vulnerabilities (target determination looks at it); details in finding_list (latest window)
	out["facts"] = len(factNodes) // Number of explored facts/conclusions (including negative conclusions)
	// findings The highest value product in the task → Overview with latest window (≤10 Rule, vulnNodes already sorted by id in descending order (latest first),
	// Let planner see the most recently confirmed vulnerabilities when judging targets in each round; for full or earlier ones, use list_findings to get them.
	// Keep only one per line {id, summary, from_intent?}: from_intent The intent that produced this vulnerability.
	// evidence/assets/vulnclass/severity/state Still available list_findings / node_detail(id) take.
	const findingListCap = 10
	findingList:= make([]map[string]any, 0, findingListCap)
	for _, n:= range vulnNodes {
		if len(findingList) >= findingListCap {
			break
		}
		var fp map[string]any
		_ = json.Unmarshal(n.Payload, &fp)
		m:= map[string]any{"id": n.ID, "summary": fp["summary"]}
		if from:= factFrom[n.ID]; from > 0 {
			m["from_intent"] = from // Which intent generated this vulnerability
		}
		findingList = append(findingList, m)
	}
	out["finding_list"] = findingList
	// recent_facts: Latest window in non-folded facts (≤N, factNodes Sorted by id descending (latest first). Folded into
	// digest And still cold (hidden) walk cold_digests; do not repeat. Each {id, summary, from_intent?,
	// confidence?}; evidence Wait for details using node_detail.(id). Earlier used list_facts to check.
	const recentFactsCap = 20
	recentFacts:= make([]map[string]any, 0, recentFactsCap)
	for _, n:= range factNodes {
		if len(recentFacts) >= recentFactsCap {
			break
		}
		if hidden(n.ID) {
			continue // Folded into digest and still cold. —— See cold_digests
		}
		m:= compactNode(n)
		if from:= factFrom[n.ID]; from > 0 {
			m["from_intent"] = from // Which fact generated this reality
		}
		// confidence Bring into overview: Let the planner see at a glance which conclusion is only inferred (especially negative conclusions
		// Don't treat it as a solid case); evidence is long, leaving it to node_detail.(id).
		var fp map[string]any
		if json.Unmarshal(n.Payload, &fp) == nil {
			if c, ok:= fp["confidence"].(string); ok && c!= "" {
				m["confidence"] = c
			}
		}
		recentFacts = append(recentFacts, m)
	}
	out["recent_facts"] = recentFacts
	// recent_done_intents: The latest window in non-collapsible completed intents (≤N, recentDone Already sorted by id in descending order).
	// Earlier, look at done_intents_total count + node_detail(id).
	const recentDoneCap = 12
	if len(recentDone) > recentDoneCap {
		recentDone = recentDone[:recentDoneCap]
	}
	out["recent_done_intents"] = compactIntents(recentDone, parentsOf, yieldsOf)
	// cold-digest §6.1: Collapse the cold area's digest body, taking the top N by latest member time; older digests that were truncated
	// Only give bare id (still can expand_digest), avoid cold zone unique exit being infinitely stretched.
	const coldDigestsCap = 15
	if cds, more:= coldDigestsRecent(t.ts, coldDigestsCap); len(cds) > 0 {
		out["cold_digests"] = cds // [{id, body, member_count}] —— Directly read body (§6.1)
		if len(more) > 0 {
			out["cold_digests_more"] = more // Truncated older digest IDs; use expand_digest(id) Expand
		}
	}
	// the original task (root) so the planner always has it, not just the
	// decomposed goals.
	if description, goal, err:= t.ts.Root(); err == nil {
		out["task"] = map[string]any{"description": description, "goal": goal}
	}
	// Direct source tasks are a live, read-only blackboard view. Keep their
	// summaries in a separate field so their intents never enter this task's
	// frontier or get mistaken for locally claimable work.
	out["related_tasks"] = t.relatedTaskOverviews()
	// coverage: Rough reference for asset testing coverage——range(task_scope)Assets within the inner ones that have been fact-checked
	// Proportion + by_type(Total/Checked by type). To see untested specific assets, agent calls list_untested_assets to judge them on demand. Only available in the task context.
	// When the asset coverage function is turned off(coverageDisabled): Only keep host_count,(Sensory information about the number of target hosts.),
	// Discard denominator/tested/pct/by_type/note etc. coverage metrics to avoid polluting the context and also not inducing
	// Hidden add_task_scope/list_untested_assets.
	if t.as!= nil && t.ts!= nil && t.taskID > 0 {
		{
			m:= map[string]any{}
			if!t.coverageDisabled {
				if cov, err:= t.as.TaskCoverageWithSources(t.taskID); err == nil {
					m["denominator"] = cov.Denominator
					m["tested"] = cov.Tested
					m["by_type"] = cov.ByType
					m["note"] = "coverageAsset test coverage (including interfaces and various related assets), rough estimate for reference: includes the scope of the current task and directly associated tasks, fact anchors; associated scopes are read-only. Container-type assets/many enumerations will make it lower; do not believe this means testing is complete; use add_task_scope to supplement this task's range, list_untested_assets to see untested assets [usually do not call list_untested_assets, proceed with the task as usual]."
					if cov.Denominator == 0 {
						m["pct"] = nil
						m["status"] = "Scope not anchored"
					} else {
						m["pct"] = cov.Pct
					}
				}
			}
			if hosts, err:= t.as.HostsByTaskWithSources(t.taskID); err == nil {
				// Only give the total number of hosts, no longer flatten the host list into graph_overview (in large-scale tasks, that's every round
				// carrying a large number of repeated strings that have limited value for planning decisions); specific hosts list_assets on demand.
				m["host_count"] = len(hosts)
			}
			if len(m) > 0 {
				out["coverage"] = m
			}
		}
	}
	return out
}

func inheritedMap(m map[string]any, sourceTaskID int64) map[string]any {
	m["source_task_id"] = sourceTaskID
	m["inherited"] = true
	return m
}

const (
	relatedOverviewTotalTextRunes = 48_000
	relatedOverviewMaxTextPerSource = 8_000
	relatedOverviewMaxGoalsPerSource = 8
	relatedOverviewMaxHintsPerSource = 6
	relatedOverviewMaxFactsPerSource = 12
	relatedOverviewMaxFindingsPerTask = 6
	relatedOverviewMaxIntentsPerTask = 8
	relatedOverviewMaxScopePerSource = 12
	relatedOverviewMaxDigestsPerSource = 6
)

// overviewTextBudget bounds inherited prompt text while preserving a fair slice
// for every direct source. Full evidence remains available through the on-demand
// read tools, so truncation here does not discard persisted blackboard data.
type overviewTextBudget struct {
	remaining int
	truncated bool
}

func relatedOverviewBudgetForSources(sourceCount int) int {
	if sourceCount <= 0 {
		return 0
	}
	if sourceCount > db.MaxTaskSourceCount {
		sourceCount = db.MaxTaskSourceCount
	}
	perSource:= relatedOverviewTotalTextRunes / sourceCount
	if perSource > relatedOverviewMaxTextPerSource {
		perSource = relatedOverviewMaxTextPerSource
	}
	return perSource
}

func (b *overviewTextBudget) take(value any, fieldLimit int) string {
	var text string
	switch value:= value.(type) {
	case string:
		text = strings.TrimSpace(value)
	case nil:
		return ""
	default:
		text = strings.TrimSpace(fmt.Sprint(value))
	}
	if text == "" {
		return ""
	}
	if b.remaining <= 0 || fieldLimit <= 0 {
		b.truncated = true
		return ""
	}
	runes:= []rune(text)
	limit:= fieldLimit
	if limit > b.remaining {
		limit = b.remaining
	}
	if len(runes) > limit {
		b.truncated = true
		if limit == 1 {
			text = "…"
		} else {
			text = string(runes[:limit-1]) + "…"
		}
		runes = []rune(text)
	}
	b.remaining -= len(runes)
	return text
}

func recentTerminalIntents(store *db.ExplorationStore, limit int) []*db.Node {
	if limit <= 0 {
		return []*db.Node{}
	}
	const batch = 300
	cursor:= int64(0)
	out:= make([]*db.Node, 0, limit)
	for len(out) < limit {
		page, more, err:= store.ListByKindPage(db.KindIntent, cursor, batch)
		if err!= nil || len(page) == 0 {
			break
		}
		for _, intent:= range page {
			switch intent.State {
			case "done", "blocked", "exhausted", "stopped":
				out = append(out, intent)
			}
			if len(out) >= limit {
				break
			}
		}
		if!more {
			break
		}
		cursor = page[len(page)-1].ID
	}
	return out
}

// relatedTaskOverviews distills persistent blackboard state from direct source
// tasks. It intentionally reads each source's local store methods, never its own
// related sources, so inheritance is one level only.
func (t *ToolSet) relatedTaskOverviews() []map[string]any {
	sources, err:= t.ts.DirectSourceStores()
	if err!= nil {
		return []map[string]any{}
	}
	if len(sources) > db.MaxTaskSourceCount {
		sources = sources[:db.MaxTaskSourceCount]
	}
	perSourceTextBudget:= relatedOverviewBudgetForSources(len(sources))
	out:= make([]map[string]any, 0, len(sources))
	for _, source:= range sources {
		ts:= source.Store
		// §2 cross-task: render the source task's OWN folded view — fold out the
		// members it has already folded, and surface its cold_digests read-only.
		hidden:= hiddenMembersFor(ts)
		budget:= overviewTextBudget{remaining: perSourceTextBudget}
		item:= map[string]any{
			"source_task_id": source.Task.TaskID,
			"inherited": true,
			"task": map[string]any{
				"description": budget.take(source.Task.Description, 800),
				"goal": budget.take(source.Task.Goal, 800),
				"status": source.Task.Status,
			},
		}
		stats, statsErr:= ts.Stats()

		edges, _:= ts.Edges(5000)
		parentsOf:= map[int64][]int64{}
		yieldsOf:= map[int64][]int64{}
		factFrom:= map[int64]int64{}
		for _, edge:= range edges {
			switch edge.Rel {
			case db.RelDerivedFrom, db.RelSpawns:
				parentsOf[edge.To] = append(parentsOf[edge.To], edge.From)
			case db.RelYields:
				yieldsOf[edge.From] = append(yieldsOf[edge.From], edge.To)
				factFrom[edge.To] = edge.From
			}
		}

		goals, _:= ts.ListByKind(db.KindGoal, relatedOverviewMaxGoalsPerSource)
		goalSummary:= make([]map[string]any, 0, len(goals))
		for _, goal:= range goals {
			var payload map[string]any
			_ = json.Unmarshal(goal.Payload, &payload)
			goalSummary = append(goalSummary, inheritedMap(map[string]any{
				"id": goal.ID, "state": goal.State, "text": budget.take(payload["text"], 400),
			}, source.Task.TaskID))
		}
		item["goals"] = goalSummary

		hints, _:= ts.ListByKind(db.KindHint, relatedOverviewMaxHintsPerSource)
		hintSummary:= make([]map[string]any, 0, len(hints))
		for _, hint:= range hints {
			var payload map[string]any
			_ = json.Unmarshal(hint.Payload, &payload)
			hintSummary = append(hintSummary, inheritedMap(map[string]any{
				"id": hint.ID, "state": hint.State, "text": budget.take(payload["text"], 400),
			}, source.Task.TaskID))
		}
		item["hints"] = hintSummary

		facts, _:= ts.ListByKind(db.KindFact, relatedOverviewMaxFactsPerSource)
		findings, _:= ts.ListByKind(db.KindFinding, relatedOverviewMaxFindingsPerTask)
		intentNodes, _:= ts.ListByKind(db.KindIntent, 300)
		terminalIntent:= make(map[int64]bool, len(intentNodes))
		for _, intent:= range intentNodes {
			terminalIntent[intent.ID] = inheritedIntentSummaryState(intent.State)
		}
		item["facts"] = len(facts)
		item["findings"] = len(findings)
		if statsErr == nil {
			item["facts"] = stats[db.KindFact]
			item["findings"] = stats[db.KindFinding]
			if stats[db.KindGoal] > len(goals) || stats[db.KindHint] > len(hints) ||
				stats[db.KindFact] > len(facts) || stats[db.KindFinding] > len(findings) {
				budget.truncated = true
			}
		}
		recentFindings:= make([]map[string]any, 0, len(findings))
		for _, finding:= range findings {
			entry:= inheritedMap(compactFinding(finding), source.Task.TaskID)
			entry["summary"] = budget.take(entry["summary"], 400)
			recentFindings = append(recentFindings, entry)
		}
		item["recent_findings"] = recentFindings
		recentFacts:= make([]map[string]any, 0, len(facts))
		for _, fact:= range facts {
			if hidden(fact.ID) {
				continue // folded into this source's cold_digests — shown there (§2/§6.2)
			}
			m:= inheritedMap(compactNode(fact), source.Task.TaskID)
			m["summary"] = budget.take(m["summary"], 400)
			if from:= factFrom[fact.ID]; from > 0 && terminalIntent[from] {
				m["from_intent"] = from
			}
			var payload map[string]any
			if json.Unmarshal(fact.Payload, &payload) == nil {
				if confidence, ok:= payload["confidence"].(string); ok && confidence!= "" {
					m["confidence"] = confidence
				}
			}
			recentFacts = append(recentFacts, m)
		}
		item["recent_facts"] = recentFacts

		recentDoneRaw:= recentTerminalIntents(ts, relatedOverviewMaxIntentsPerTask)
		recentDone:= recentDoneRaw[:0] // in-place filter: drop this source's folded intents (§2)
		for _, intent:= range recentDoneRaw {
			if hidden(intent.ID) {
				continue
			}
			recentDone = append(recentDone, intent)
		}
		for _, intent:= range recentDone {
			intent.Inherited = true
			intent.SourceTaskID = source.Task.TaskID
		}
		intentResults:= compactIntents(recentDone, parentsOf, yieldsOf)
		for i, intent:= range recentDone {
			intentResults[i]["summary"] = budget.take(intentResults[i]["summary"], 400)
			acts, _, err:= ts.ActivityPageForTerminalIntent(intent.ID, 0, 20)
			if err!= nil {
				continue
			}
			var resultSummary, textFallback string
			for _, activity:= range acts {
				switch activity.Kind {
				case "result":
					resultSummary = activity.Summary
				case "text":
					textFallback = activity.Summary
				}
			}
			if resultSummary == "" {
				resultSummary = textFallback
			}
			if resultSummary!= "" {
				intentResults[i]["result_summary"] = budget.take(resultSummary, 800)
			}
		}
		item["recent_intent_results"] = intentResults
		// §2 cross-task: the source task's folded cold region, read-only, newest-member
		// first & capped like the current task's. Members (and overflow digests) are
		// resolvable via expand_digest(id)/node_detail(id), which search source tasks.
		if cds, more:= coldDigestsRecent(ts, relatedOverviewMaxDigestsPerSource); len(cds) > 0 {
			for _, cd:= range cds {
				cd["inherited"] = true
				cd["source_task_id"] = source.Task.TaskID
			}
			item["cold_digests"] = cds
			if len(more) > 0 {
				item["cold_digests_more"] = more // an older digest id that has been truncated; expand_digest(id) Expand
			}
		}
		if statsErr == nil {
			item["node_stats"] = stats
		}

		if t.as!= nil {
			if scopeRows, err:= t.as.ListTaskScope(source.Task.TaskID); err == nil && len(scopeRows) > 0 {
				scopeCount:= len(scopeRows)
				if len(scopeRows) > relatedOverviewMaxScopePerSource {
					scopeRows = scopeRows[:relatedOverviewMaxScopePerSource]
					budget.truncated = true
				}
				scope:= make([]map[string]any, 0, len(scopeRows))
				for _, row:= range scopeRows {
					entry:= map[string]any{"kind": row.Kind, "source": budget.take(row.Source, 300)}
					switch {
					case row.Domain!= "":
						entry["value"] = budget.take(row.Domain, 400)
					case row.Net!= "":
						entry["value"] = budget.take(row.Net, 400)
					case row.Value!= "":
						entry["value"] = budget.take(row.Value, 400)
					case row.CompanyID!= nil:
						entry["company_id"] = *row.CompanyID
					}
					scope = append(scope, entry)
				}
				item["asset_scope"] = scope
				item["asset_scope_count"] = scopeCount
			}
			if coverage, err:= t.as.TaskCoverage(source.Task.TaskID, source.Task.ExplorationID); err == nil {
				item["asset_coverage"] = map[string]any{
					"denominator": coverage.Denominator,
					"tested": coverage.Tested,
					"pct": coverage.Pct,
					"by_type": coverage.ByType,
				}
			}
		}
		if budget.truncated {
			item["summary_truncated"] = true
		}
		out = append(out, item)
	}
	return out
}

func inheritedIntentSummaryState(state string) bool {
	switch state {
	case "done", "blocked", "exhausted", "stopped":
		return true
	default:
		return false
	}
}

// compactNode distills any exploration node to id + summary + state, dropping the
// big detail/evidence (fetch that on demand via node_detail).
func compactNode(n *db.Node) map[string]any {
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	m:= map[string]any{"id": n.ID, "state": n.State, "summary": p["summary"]}
	if n.Inherited {
		inheritedMap(m, n.SourceTaskID)
	}
	return m
}

// compactFinding is compactNode plus the vuln-specific vulnclass/severity.
func compactFinding(n *db.Node) map[string]any {
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	m:= map[string]any{"id": n.ID, "state": n.State, "summary": p["summary"]}
	if n.Inherited {
		inheritedMap(m, n.SourceTaskID)
	}
	if vc, ok:= p["vulnclass"]; ok && vc!= nil && vc!= "" {
		m["vulnclass"] = vc
	}
	if sv, ok:= p["severity"]; ok && sv!= nil && sv!= "" {
		m["severity"] = sv
	}
	return m
}

func (t *ToolSet) listFindings() actool.CoreTool {
	return t.readExpTool("list_findings", "List this task and directly related tasks' [Confirmed Vulnerabilities](Compact: id+task_id+intent_id+vulnclass+severity+Failure+Status). Task items associated with source_task_id/inherited=true And read-only. Here only contains vulnerabilities; normal exploration facts use list_facts, details use node_detail.(id). ",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			f, _:= t.ts.ListByKindWithSources(db.KindFinding, 500)
			if err:= t.ts.PopulateFindingTrafficIDs(f); err!= nil {
				return actool.Errorf(err.Error()), nil
			}
			intentOf, _:= t.ts.FindingIntentsWithSources() // finding id -> The intent ID that produced it
			taskID:= t.taskID
			if taskID <= 0 {
				taskID, _ = t.ts.TaskID()
			}
			out:= make([]map[string]any, 0, len(f))
			for _, n:= range f {
				m:= compactFinding(n)
				if n.FindingID > 0 {
					m["finding_id"], m["finding_node_id"], m["traffic_count"] = n.FindingID, n.ID, n.TrafficCount
				}
				if n.Inherited {
					m["task_id"] = n.SourceTaskID
				} else {
					m["task_id"] = taskID
				}
				if iid, ok:= intentOf[n.ID]; ok {
					m["intent_id"] = iid
				}
				out = append(out, m)
			}
			return jsonResult(out)
		})
}

// factsPageSize is the default page size for list_facts. Facts pile up on long
// tasks; returning all of them at once (the old behaviour) could blow up the
// context, so default to the newest page and let the agent page/filter for more.
const factsPageSize = 20

func (t *ToolSet) listFacts() actool.CoreTool {
	return t.readExpTool("list_facts", "Paginated list of exploration facts/conclusions for this task and directly related tasks, newest first.(Compact: id+Failure+Status, summaries longer than the limit will be truncated, use node_detail for full text(id)). Parameters are optional: limit(Default 20, upper limit 100), before(Cursor, use next_before from the previous page to get an older page; omit/0=Latest page), q(Filter by summary keywords). Support {facts, total, has_more, next_before}: total Is the total number after filtering, has_more.=true Use next_before to continue pagination when applicable. Associated task entries include source_task_id/inherited.=true And read-only. Vulnerabilities can be viewed via list_findings.",
		obj(map[string]any{
			"limit": intp("Return count, default 20, max 100"),
			"before": intp("Pagination cursor: Only return facts with id less than the specified value; omit or use 0. = Latest page"),
			"q": str("Filter by fact summary keywords (case-insensitive); omit = No filtering"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Limit int `json:"limit"`
				Before int64 `json:"before"`
				Q string `json:"q"`
			}
			_ = json.Unmarshal(in, &a)
			limit:= a.Limit
			if limit <= 0 {
				limit = factsPageSize
			}
			if limit > 100 {
				limit = 100
			}
			f, hasMore, total, err:= t.ts.ListByKindPageWithSources(db.KindFact, a.Before, limit, strings.TrimSpace(a.Q))
			if err!= nil {
				return actool.Result{}, err
			}
			out:= make([]map[string]any, 0, len(f))
			for _, n:= range f {
				out = append(out, compactFact(n))
			}
			res:= map[string]any{"facts": out, "total": total, "has_more": hasMore}
			if hasMore && len(f) > 0 {
				res["next_before"] = f[len(f)-1].ID // Pass it back to fetch the next page(Older)
			}
			return jsonResult(res)
		})
}

// factSummaryMax caps a fact summary in list_facts output. Facts carry one-line
// conclusions, but nothing enforces brevity; a runaway summary must not bloat a
// whole page. Full text stays available via node_detail(id).
const factSummaryMax = 160

// compactFact is compactNode with the summary rune-capped for list_facts, so a
// page of facts stays bounded regardless of how long any single summary grew.
func compactFact(n *db.Node) map[string]any {
	m:= compactNode(n)
	if s, ok:= m["summary"].(string); ok && len([]rune(s)) > factSummaryMax {
		m["summary"] = string([]rune(s)[:factSummaryMax]) + "…"
		m["summary_truncated"] = true
	}
	return m
}

func (t *ToolSet) nodeDetail() actool.CoreTool {
	return t.readExpTool("node_detail", "Get the complete content of the [exploration graph node] for this task by id or directly associate with another task. Inherited nodes include source_task_id/inherited=true And read-only. Only for exploration node ids returned by list_facts/list_findings/graph_overview; assets should use list_assets/asset_neighbors.",
		obj(map[string]any{"id": idp("Explore graph node id.(Not an asset id)")}, "id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				ID json.RawMessage `json:"id"`
			}
			_ = json.Unmarshal(in, &a)
			id:= pid(a.ID)
			if id <= 0 {
				return actool.Errorf("id Required"), nil
			}
			n, err:= t.ts.GetNodeWithSources(id)
			if err!= nil {
				return actool.Errorf(err.Error()), nil
			}
			if n == nil {
				return actool.Errorf(fmt.Sprintf("No exploration node found %d. If you want to check assets, use list_assets / asset_neighbors (assets and exploration nodes are different id spaces; asset ids cannot be passed to node_detail).", id)), nil
			}
			if err:= t.ts.PopulateFindingTrafficIDs([]*db.Node{n}); err!= nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(n) // full payload incl. detail / evidence, plus explicit finding IDs
		})
}

// --- planner write tools ---

// intentItem A direction of exploration for add_intent batch/single.
type intentItem struct {
	Summary string `json:"summary"`
	AssetIDs []json.RawMessage `json:"asset_ids"`
	ParentIDs []json.RawMessage `json:"parent_ids"`
	Priority int `json:"priority"`
}

// addOneIntent Create an intent node and connect upstream ancestry, return id.
// Constraint: Intent can only anchor on confirmed knowledge——Each parent_id must be an existing fact/finding
// Node (cannot be attached to other intents/targets/prompts). Top-level new directions leave parent_ids empty, fallback to origin fact.
// like this"Each intent connects to a fact node and is driven by discoveries rather than empty planning"Forced from the creation path.
func (t *ToolSet) addOneIntent(it intentItem) (int64, error) {
	if strings.TrimSpace(it.Summary) == "" {
		return 0, fmt.Errorf("summary cannot be empty")
	}
	// First validate anchor point (to avoid orphan intents left by bad anchors before node creation).
	parents:= pidList(it.ParentIDs)
	for _, pidv:= range parents {
		n, err:= t.ts.GetNodeWithSources(pidv)
		if err!= nil || n == nil {
			return 0, fmt.Errorf("parent_id %d Does not exist in this task or directly related tasks: parent_ids must be existing[facts(fact)/Remove(finding)]Node id; for new top-level direction leave parent_ids empty", pidv)
		}
		if n.Kind!= db.KindFact && n.Kind!= db.KindFinding {
			return 0, fmt.Errorf("parent_id %d is %q Node, can't be an intent anchor: Intent can only anchor on confirmed [facts](fact)/Remove(finding)]It should not be hung on intent/target/hint; leave parent_ids empty for a top-level new direction when writing back record_fact / report_finding.", pidv, n.Kind)
		}
	}
	priority:= it.Priority
	if priority == 0 {
		priority = 5
	}
	anchors:= pidList(it.AssetIDs)
	// Asset interception: If the intended asset hits the system's asset interception rules, it prohibits issuing that intent.
	if t.as!= nil && len(anchors) > 0 {
		hits, err:= t.as.CheckAssetsIntercept(t.taskID, anchors)
		if err!= nil {
			return 0, fmt.Errorf("Asset interception validation failed:%w", err)
		}
		if len(hits) > 0 {
			var b strings.Builder
			fmt.Fprintf(&b, "Intent '%s'The assets bound to this task have not passed the test range validation, please stop testing related assets:", it.Summary)
			for _, h:= range hits {
				fmt.Fprintf(&b, "\n - %s", h.Describe())
			}
			return 0, fmt.Errorf("%s", b.String())
		}
	}
	payload:= map[string]any{"summary": it.Summary}
	if len(anchors) > 0 {
		payload["asset_ids"] = anchors
	}
	id, err:= t.ts.AddIntent(payload, priority, anchors, "planner")
	if err!= nil {
		return 0, err
	}
	// upstream lineage: link each (validated) fact/finding parent → this intent, so
	// "multiple facts combine into one new intent" is expressible.
	for _, parent:= range parents {
		_ = t.ts.Link(parent, db.RelDerivedFrom, id)
	}
	// a top-level intent (no explicit parent) connects to the origin fact, so every
	// intent still traces back to a fact node — at task start the only fact is the
	// origin, and the first intents derive from it.
	if len(parents) == 0 {
		if origin, _:= t.ts.OriginFactID(); origin > 0 {
			_ = t.ts.Link(origin, db.RelDerivedFrom, id)
		}
	}
	return id, nil
}

func (t *ToolSet) addIntent() actool.CoreTool {
	return t.writeExpTool("add_intent", "Generate [exploration direction] write into frontier and link into exploration chain. The intent is an open exploration direction, not a fixed type——Use summary to freely describe what to explore/verify/utilize.\n"+
		"★Prioritize batching: submit multiple new directions in one go (savesround trip). Return ids array, same length as intents (failed item id=0, Details in errors). For a single item, omit intents and directly give the top-level summary.",
		obj(map[string]any{
			"intents": map[string]any{"type": "array", "description": "[Prioritize using this array of new exploration directions to process them in order. Each element field is the same as the top-level fields (summary/asset_ids/parent_ids/priority). The returned ids should be of equal length and in the same order as this array.", "items": map[string]any{"type": "object"}},
			"summary": str("[single] In one sentence, describe this exploration direction: What to do?+Why? Direction already clear, no dependency on asset ID."),
			"asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "The target asset id to test/attack in this direction (**pass along when possible**, 0/1/Multiple; these are asset IDs returned by list_assets, not exploration node IDs): This exploration direction targets which assets (sites/interfaces/parameters/hosts, etc.). As long as the direction revolves around certain specific assets, always pass them up.——It is a structured marker for 'which targets this exploration will attack,' used to cover de-duplication and connect intents into asset chains. Only leave empty when performing pure global reconnaissance and there are no specific target assets."},
			"parent_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Upstream anchor ID (optional, 0/1/multiple): Which confirmed facts in this direction?(fact)/Remove(finding)]Comprehensive result.**Can only fill in existing fact/finding node IDs.,Cannot fill intent/target/hint**——The intent must be anchored on confirmed knowledge,Discovering drivers rather than planning out of thin air. Multiple facts together produce a new intent, so pass multiple.;Leave the top-level new reconnaissance direction empty (it will automatically attach to the task's origin fact)."},
			"priority": intp("Priority 0-10, default is 5"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Intents []intentItem `json:"intents"`
				intentItem // Single mode: top-level summary/asset_ids/parent_ids/priority
			}
			_ = json.Unmarshal(in, &a)
			batch:= len(a.Intents) > 0
			items:= a.Intents
			if!batch {
				items = []intentItem{a.intentItem}
			}

			ids:= make([]int64, len(items))
			errs:= map[string]string{}
			createdAny:= false
			for i, it:= range items {
				id, err:= t.addOneIntent(it)
				if err!= nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
				createdAny = true
			}

			// Manually dispatching an intent to the main agent. → If the task is done (no open goalless branch), move it
			// Pull back running, worker can only claim this intent for execution. resumeTask is only accessible via the main agent's Chat
			// (SetResumeTask); planner have their ToolSet as nil, so planner calls add_intent during this segment
			// no-op, Without affecting its normal intent. Intent nodes are already built above(open), Will not be misjudged as drained upon revival.
			if createdAny && t.resumeTask!= nil {
				t.resumeTask()
			}

			if!batch { // Single item: keep original return
				if e, bad:= errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("intent created: %d", ids[0])), nil
			}
			out:= map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

func (t *ToolSet) listGoals() actool.CoreTool {
	return t.readExpTool("list_goals", "Lists the target nodes and their status (open/met) for this task, used to determine if the objective has been achieved.",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			g, _:= t.ts.ListByKind(db.KindGoal, 100)
			return jsonResult(g)
		})
}

func (t *ToolSet) proveGoal() actool.CoreTool {
	return t.writeExpTool("prove_goal", "Call when you determine that a discovery/fact proves a target has been achieved: connect evidence nodes to target nodes and mark target as met.",
		obj(map[string]any{
			"goal_id": idp("Target Node ID"),
			"evidence_id": idp("Prove its discovery/fact node id"),
			"reason": str("Why does this evidence meet the goal"),
		}, "goal_id", "evidence_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				GoalID json.RawMessage `json:"goal_id"`
				EvidenceID json.RawMessage `json:"evidence_id"`
				Reason string `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			goal, ev:= pid(a.GoalID), pid(a.EvidenceID)
			if goal == 0 || ev == 0 {
				return actool.Errorf("goal_id And evidence_id must be filled in"), nil
			}
			goalNode, err:= t.ts.GetNode(goal)
			if err!= nil || goalNode == nil || goalNode.Kind!= db.KindGoal {
				return actool.Errorf("goal_id Must be the target node of this task (associated task targets are read-only)"), nil
			}
			evidenceNode, err:= t.ts.GetNodeWithSources(ev)
			if err!= nil || evidenceNode == nil || (evidenceNode.Kind!= db.KindFact && evidenceNode.Kind!= db.KindFinding) {
				return actool.Errorf("evidence_id Must be facts/vulnerability nodes directly related to this task or its associated tasks"), nil
			}
			_ = t.ts.Link(ev, db.RelProves, goal)
			_ = t.ts.SetNodeState(goal, "met")
			// After marking each target met, check if all targets for this task have been met; if so, automatically determine
			// Task completed (set GoalMet), no longer need to depend on the model to explicitly call goal_met.
			if goals, err:= t.ts.ListByKind(db.KindGoal, 1000); err == nil && len(goals) > 0 {
				allMet:= true
				for _, g:= range goals {
					if g.State!= "met" {
						allMet = false
						break
					}
				}
				if allMet {
					t.GoalMet = true
					t.Reason = fmt.Sprintf("All %d All goals have been met (last by goal) %d Trigger)", len(goals), goal)
					return actool.Text(fmt.Sprintf("goal %d marked met; All targets for this task have been achieved, the task is automatically determined complete", goal)), nil
				}
			}
			return actool.Text(fmt.Sprintf("goal %d marked met", goal)), nil
		})
}

func (t *ToolSet) goalMet() actool.CoreTool {
	return writeTool("goal_met", "[Immediately end the entire task——Only call it when you confirm that all targets have truly been achieved and the overall mission has concluded (note that it is the mission as a whole that is completed; merely achieving one target/flag/vulnerability [not counting] does not trigger this).——For that situation, use prove_goal to mark the target⚠️It is not used for“End current round planning”If there is no new intent to dispatch this round or waiting for worker output, directly end this round (no need to call this tool) (0 intents is completely normal). Normally determine priority using prove_goal one by one; goal_met only bypasses individual proof and directly concludes from global closure means.",
		obj(map[string]any{"reason": str("Reason for achievement (must be evidence of the target truly being achieved, not just evidence of the vulnerability)“No new direction in this round”Reasons for ending this round of this class)")}, "reason"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct{ Reason string }
			_ = json.Unmarshal(in, &a)
			t.GoalMet = true
			t.Reason = a.Reason
			return actool.Text("acknowledged: goal marked met"), nil
		})
}

// --- worker write tools ---

func (t *ToolSet) addFinding() actool.CoreTool {
	return writeTool("report_finding", "Record confirmed vulnerabilities using evidence to provide verifiable command outputs, logs, etc. Pass the current intent_id through task context. The returned finding_id is an independent vulnerability record ID, and finding_node_id is the exploration node ID (retain this node number on the first line).", obj(map[string]any{
		"vulnclass": str("Vulnerability Category"), "name": str("Vulnerability name"), "severity": str("critical|high|medium|low"), "summary": str("Discovery summary"),
		"intent_id": idp("The intent ID of the current task"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Affected asset ID"},
		"evidence": str("Evidence/PoC text"),
		"evidence_hint_id": idp("Optional: This task's corresponding hint node ID for this vulnerability, automatically carrying its structured traffic_refs; cannot reference inherited hints or other vulnerabilities' hints"),
		"traffic_refs": map[string]any{"type": "array", "description": "Optional; for HTTP/HTTPS vulnerabilities, first retrieve and verify each request/response indeed supports the vulnerability conclusion before filling in the real ID in the order of reproduction. For non-HTTP vulnerabilities, TCP, or when no exact record is found, omit or pass. [], Do not block reporting; provide reasons in evidence and other verifiable evidence. Do not guess IDs, infer associations based on domain/time, or merely re-probe for patching. Purpose baseline normalbaseline / proof / verification / supporting.",
			"items": obj(map[string]any{"traffic_id": str("traffic_search The real traffic ID returned."), "role": map[string]any{"type": "string", "enum": []string{"baseline", "proof", "verification", "supporting"}}, "note": str("How does this traffic support the vulnerability conclusion")}, "traffic_id")},
	}, "vulnclass", "severity", "summary"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
		var a struct {
			VulnClass, Name, Severity, Summary, Evidence string
			IntentID json.RawMessage `json:"intent_id"`
			AssetIDs []json.RawMessage `json:"asset_ids"`
			TrafficRefs []db.TrafficRef `json:"traffic_refs"`
			EvidenceHintID json.RawMessage `json:"evidence_hint_id"`
		}
		if err:= json.Unmarshal(in, &a); err!= nil {
			return actool.Errorf(err.Error()), nil
		}
		if t.ts == nil {
			return actool.Errorf("report_finding Requires task context; platform dialogue should pass confirmed vulnerabilities to the corresponding task through add_task_hint and include existing traffic_refs in the prompt. Registered vulnerabilities can be bound using bind_finding_traffic."), nil
		}
		// Auto-binding off: ignore the evidence params instead of rejecting the call.
		// stripTrafficParameters already removes them from the advertised schema, but
		// models routinely emit fields anyway — failing here would discard a confirmed
		// finding over a stray parameter. The success path below reports evidence_status
		// "not_bound" with the "Already closed, can be manually associated on the page" note, which is what the caller needs.
		if!findingTrafficBindingEnabled() {
			a.TrafficRefs, a.EvidenceHintID = nil, nil
		}
		if len(a.EvidenceHintID) > 0 && pid(a.EvidenceHintID) <= 0 {
			return actool.Errorf("evidence_hint_id Must be a valid prompt node ID; omit if no handover prompt."), nil
		}
		refs, err:= t.findingRefsFromHint(pid(a.EvidenceHintID), a.TrafficRefs)
		if err!= nil {
			return actool.Errorf(err.Error()), nil
		}
		input:= db.RecordFindingInput{TaskID: t.taskID, ExplorationID: t.ts.ID(), IntentID: pid(a.IntentID), VulnClass: a.VulnClass, Name: a.Name, Severity: a.Severity, Summary: a.Summary, Evidence: a.Evidence, Worker: t.worker, AssetIDs: pidList(a.AssetIDs)}
		var recorded *db.RecordedFinding
		if t.findingRecorder!= nil {
			recorded, err = t.findingRecorder.Record(ctx, input, refs)
		} else if len(refs) > 0 {
			return actool.Errorf("Traffic evidence storage unavailable; no vulnerability registered."), nil
		} else {
			recorded, err = t.ts.RecordFinding(ctx, input)
		}
		if err!= nil {
			return actool.Errorf(err.Error()), nil
		}
		if t.notifyFinding!= nil {
			iid:= input.IntentID
			if iid <= 0 {
				iid = t.ownerNode
			}
			t.notifyFinding(iid, a.Summary)
		} else if t.notify!= nil {
			t.notify()
		}
		t.writes.Findings++
		// Keep the first line's node-ID contract for existing reporter triggers.
		for i:= range recorded.Traffic.Bindings {
			recorded.Traffic.Bindings[i].Snapshot.ReqHead = ""
			recorded.Traffic.Bindings[i].Snapshot.RespHead = ""
		}
		result:= struct {
			*db.RecordedFinding
			EvidenceStatus string `json:"evidence_status"`
			EvidenceNote string `json:"evidence_note,omitempty"`
		}{RecordedFinding: recorded, EvidenceStatus: "bound"}
		if len(recorded.Traffic.Bindings) == 0 {
			result.EvidenceStatus = "not_bound"
			result.EvidenceNote = "Vulnerability saved but not bound to traffic. TCP/No-packet scenarios can continue normally; if there is verified HTTP traffic, use available bind_finding_traffic or vulnerability page bindings to complete evidence transfer. Do not create duplicate vulnerabilities."
			if!findingTrafficBindingEnabled() {
				result.EvidenceNote = "Vulnerability saved. Agent automatically binds traffic, which can be manually associated on the page."
			}
		}
		raw, _:= json.Marshal(result)
		return actool.Text(fmt.Sprintf("finding recorded: %d\n%s", recorded.NodeID, raw)), nil
	})
}

// recordFact writes a general exploration RESULT/conclusion (not a vuln, not a
// new asset) into the EXPLORATION graph, chained to the intent that produced it.
// This is the home for observations and — importantly — negative results
// ("port closed", "param not injectable", "no login found"). Such conclusions
// must NOT be stuffed into the asset graph via upsert_asset.
// factItem This is one fact from record_fact batch/single submission.
type factItem struct {
	Summary string `json:"summary"`
	Detail string `json:"detail"`
	Evidence string `json:"evidence"` // One line of key evidence (command+Key output lines to support conclusions and facilitate post-event verification.
	Confidence string `json:"confidence"` // observed (Directly see)| inferred (Infer from phenomenon)
	IntentID json.RawMessage `json:"intent_id"`
	AssetIDs []json.RawMessage `json:"asset_ids"`
}

// recordOneFact Write a fact node and connect it to the intent (intent→yields→fact). defaultIntent For
// Default intent for batch operations (used when no intent_id is given).
func (t *ToolSet) recordOneFact(it factItem, defaultIntent int64) (int64, error) {
	if strings.TrimSpace(it.Summary) == "" {
		return 0, fmt.Errorf("summary cannot be empty")
	}
	payload:= map[string]any{"summary": it.Summary}
	if it.Detail!= "" {
		payload["detail"] = it.Detail
	}
	if e:= strings.TrimSpace(it.Evidence); e!= "" {
		payload["evidence"] = e
	}
	if c:= strings.TrimSpace(it.Confidence); c!= "" {
		payload["confidence"] = c
	}
	intent:= pid(it.IntentID)
	if intent <= 0 {
		intent = defaultIntent
	}
	if intent > 0 {
		node, err:= t.ts.GetNode(intent)
		if err!= nil || node == nil || node.Kind!= db.KindIntent {
			return 0, fmt.Errorf("intent_id Must be this task's intent (associated task intents are read-only).")
		}
	}
	// a fact is its OWN node kind (distinct from a vuln finding).
	id, err:= t.ts.AddNode(db.KindFact, payload, 5, "confirmed", t.worker, pidList(it.AssetIDs))
	if err!= nil {
		return 0, err
	}
	if intent > 0 {
		_ = t.ts.Link(intent, db.RelYields, id) // chain: intent -> fact
	}
	t.writes.Facts++
	return id, nil
}

func (t *ToolSet) recordFact() actool.CoreTool {
	return t.writeExpTool("record_fact", "Write exploration [facts/conclusions] into the exploration graph, linking back to the producing intent (intent_id). Used to record exploration results.——Includes fingerprints/enumeration etc. [Positive Conclusions], and'port closed'/'Parameters Cannot Be Injected'/'No login entry found.'Wait for [negative conclusion].\n"+
		"⚠️Multiple observations during one exploration should be[summarized into one fact]; do not split them into multiple facts. Try to use one fact if they can be merged: summary=A summary sentence of the conclusion for this session, detail=Relevant details (can include multiple specific items). Example: Fingerprint intent.→One Fact {summary:'Identified the technology stack and response characteristics of site X', detail:'nginx 1.25 / Vue3 / 200 / title=.. / body_len=..'}, Instead of status code, fingerprint, title each on a line. One intent usually produces one fact; breaking it too much will cause the graph to expand infinitely.\n"+
		"★facts Array for multiple conclusions in one write (each can omit intent_id, default using top-level intent_id). Return ids array, same length as facts.\n"+
		"⚠️Only write the conclusion you actually see in the tool output; don't speculate. Evidence and confidence prevent inaccurate conclusions from contaminating the graph:\n"+
		" · evidence=The key evidence supporting this conclusion is one line (command+The most convincing output lines),**Must be concise**——Details are in detail; no more large text outputs here.\n"+
		" · confidence=observed (Directly visible in the output| inferred (Infer from phenomena).\n"+
		" · **Negative conclusion** (Injection not allowed/ports closed/no entry points found) should only write\"Observe + Probing read method\"——State what you actually saw; whether to give up is determined by the planner based on the overall situation; always provide evidence, mark as inferred if means haven't been exhausted or evidence is weak (including probing once and looking like it), only mark as observed if means have been exhausted and seen directly.",
		obj(map[string]any{
			"facts": map[string]any{"type": "array", "description": "[When there are multiple different conclusions, use a fact array with element fields the same as the top-level fields (summary/detail/evidence/confidence/intent_id/asset_ids); omit intent_id and use the top-level intent_id. Return ids of equal length and in the same order as this array.", "items": map[string]any{"type": "object"}},
			"summary": str("A [conclusionary sentence] for this exploration (summarizing the details)."),
			"intent_id": idp("The intent id that produced this fact (the intent you received; when batch processing, as the default for each item)"),
			"detail": str("Relevant details of this fact: write multiple observed facts from this exploration here"),
			"evidence": str("[Key evidence: Command + The most convincing two lines of output proving the conclusion. Must be concise; do not paste large segments of output (details in detail)."),
			"confidence": str("observed (Directly visible in the output| inferred (Based on observed phenomena. Negative conclusions must be accurately annotated."),
			"asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Related asset IDs (optional, 0/1/multiple): Which assets are involved in this fact."},
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Facts []factItem `json:"facts"`
				factItem // Single mode + Batch default intent_id.
			}
			_ = json.Unmarshal(in, &a)
			batch:= len(a.Facts) > 0
			items:= a.Facts
			if!batch {
				items = []factItem{a.factItem}
			}
			defaultIntent:= pid(a.factItem.IntentID) // Top-level intent_id = Batch default

			ids:= make([]int64, len(items))
			errs:= map[string]string{}
			for i, it:= range items {
				id, err:= t.recordOneFact(it, defaultIntent)
				if err!= nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
			}

			if!batch { // Single item: keep original return
				if e, bad:= errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("fact recorded: %d", ids[0])), nil
			}
			out:= map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

type hintItem struct {
	Text string `json:"text"`
	AssetIDs []json.RawMessage `json:"asset_ids"`
	TrafficRefs []db.TrafficRef `json:"traffic_refs"`
}

// addOneHint Add a hint node.(active/human)to exploration graph,Anchorable asset,Return id.
func (t *ToolSet) addOneHint(it hintItem) (int64, error) {
	if len(it.TrafficRefs) > 0 &&!findingTrafficBindingEnabled() {
		return 0, fmt.Errorf("Agent Automatic binding of traffic is disabled, no prompt carried with traffic_refs; can be enabled in system settings or transferred as plain text")
	}
	if strings.TrimSpace(it.Text) == "" {
		return 0, fmt.Errorf("text cannot be empty")
	}
	var anchors []int64
	for _, raw:= range it.AssetIDs {
		if tid:= pid(raw); tid > 0 {
			anchors = append(anchors, tid)
		}
	}
	refs, err:= db.NormalizeTrafficRefs(it.TrafficRefs)
	if err!= nil {
		return 0, err
	}
	payload:= map[string]any{"text": it.Text}
	if len(refs) > 0 {
		payload["traffic_refs"] = refs
	}
	// Wake up planner not done one by one here——Triggered by addHint once after all entries are written (with a hint text).
	// Avoid multiple hints from one add_hint flooding planner's trigger line.
	return t.ts.AddNode(db.KindHint, payload, 0, "active", "human", anchors)
}

type goalItem struct {
	Text string `json:"text"`
	VulnClass string `json:"vulnclass"`
}

// addOneGoal Add a goal node(open)to exploration graph:Connect to task root(origin fact,rel spawns).
// origin Get t.worker(Default system):goals Decoder written logs "goals", Main agent records during operation
// "human". Wake up planner by setGoals after writing the entire batch.(see below),This part is only responsible for storing data.
func (t *ToolSet) addOneGoal(it goalItem) (int64, error) {
	text:= strings.TrimSpace(it.Text)
	if text == "" {
		return 0, fmt.Errorf("text cannot be empty")
	}
	payload:= map[string]any{"text": text}
	if vc:= strings.TrimSpace(it.VulnClass); vc!= "" {
		payload["vulnclass"] = vc
	}
	origin:= t.worker
	if origin == "" {
		origin = "system"
	}
	id, err:= t.ts.AddNode(db.KindGoal, payload, 0, "open", origin, nil)
	if err!= nil {
		return 0, err
	}
	if of, _:= t.ts.OriginFactID(); of > 0 && id > 0 {
		_ = t.ts.Link(of, db.RelSpawns, id) // goals descend from the task root (origin fact)
	}
	return id, nil
}

// setGoals Add new exploration targets to [this task](goal node). It is both the submission tool for the target decomposition tool,Is also the main
// agent Tool to supplement targets at runtime——Same Managed Tool,Can be modified in web end description/schema, bound by agent.
func (t *ToolSet) setGoals() actool.CoreTool {
	return writeTool("set_goals",
		"Add new exploration targets to [this task](goal). Target=Final deliverable/verifiable result,Not an attack step or reconnaissance action.\n"+
			"★Prioritize Batch Processing:Multiple targets into goals array for one submission,Return ids with the same length and order.(Failed item id=0,Details see errors). Single item omits goals directly gives top-level text.\n"+
			"vulnclass Optional:Corresponding vulnerability class(Like SQLi/IDOR),Business logic class target left blank. Whether the goal is achieved is determined by the system marking met,This tool is only responsible for adding new items",
		obj(map[string]any{
			"goals": map[string]any{"type": "array", "description": "[Prefer this one if you need to add a new target array.,Process in order. Each element:text(Required,An independent verifiable final goal)+ vulnclass(Optional). Return ids that match the length and order of this array.", "items": map[string]any{"type": "object"}},
			"text": str("[single] An independent verifiable final goal"),
			"vulnclass": str("[single] Corresponding vulnerability class(If Explicitly),Like SQLi/IDOR;Business logic target can be left empty."),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("set_goals Not enabled: ExplorationStore Not initialized"), nil
			}
			var a struct {
				Goals []goalItem `json:"goals"`
				goalItem // Single mode:Top-level text/vulnclass
			}
			_ = json.Unmarshal(in, &a)
			batch:= len(a.Goals) > 0
			items:= a.Goals
			if!batch {
				items = []goalItem{a.goalItem}
			}

			ids:= make([]int64, len(items))
			errs:= map[string]string{}
			var addedTexts []string
			for i, it:= range items {
				id, err:= t.addOneGoal(it)
				if err!= nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
				addedTexts = append(addedTexts, strings.TrimSpace(it.Text))
			}
			if len(addedTexts) > 0 {
				// Wake up the planner.(Process entire batch at once). Prioritize notifyGoal:Each set_goals records one 'person added'
				// N Targets:...'Trigger,Do not flood the screen one by one;The decomposer/worker has no such callback → Revert to pure notify(Decomposer
				// round-0 Even notify didn't connect,no operation,Because the planner has not yet started at this time).
				switch {
				case t.notifyGoal!= nil:
					t.notifyGoal(addedTexts)
				case t.notify!= nil:
					t.notify()
				}
				// New targets added during main agent runtime → Pull completed/paused tasks back to running and continue running.(Terminal Gate Condition
				// Swallow ordinary notify,Must explicitly revive). Only mainagent accepts this callback.;The parser/worker is nil.
				if t.resumeTask!= nil {
					t.resumeTask()
				}
			}

			if!batch { // single:Keep original return
				if e, bad:= errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("goal added: %d", ids[0])), nil
			}
			out:= map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

type constraintItem struct {
	Text string `json:"text"`
	Type string `json:"type"` // allow | deny
}

// addOneConstraint Apply an operation constraint to task_constraints. origin takes t.worker(Default system):
// Decoder write "goals", Main agent writes "human".
func (t *ToolSet) addOneConstraint(it constraintItem) (int64, error) {
	text:= strings.TrimSpace(it.Text)
	if text == "" {
		return 0, fmt.Errorf("text cannot be empty")
	}
	kind:= strings.TrimSpace(strings.ToLower(it.Type))
	if kind == "" {
		kind = "deny" // Default handled as prohibited.:More conservative when type is not annotated
	}
	if kind!= "allow" && kind!= "deny" {
		return 0, fmt.Errorf("type Must be allow or deny")
	}
	return t.ts.AddConstraint(kind, text, t.worker)
}

// setConstraints Add operation constraints to this task(allow=Allow what / deny=Prohibited actions). Is also a target
// The round-0 constraint extraction tool for the disassembler,Also a tool to supplement constraints during main agent runtime.——Same Managed Tool,Can be returned in web
// Description/schema for the endpoint, bound by agent. Constraints are injected into planner/worker system prompts to constrain exploration boundaries.
func (t *ToolSet) setConstraints() actool.CoreTool {
	return writeTool("set_constraints",
		"Add operation constraints to this task,Used to define exploration boundaries:type=allow(Allowed actions)Or deny(Prohibited Operations). \n"+
			"Lateral Movement=Rules for what actions can/cannot be performed(Like 'Only test current port,Do not scan other ports; no write operations on production databases; only passive reconnaissance allowed.),Not a target or an attack step.\n"+
			"★Prioritize Batch Processing:Multiple entries into constraints array per submission,Return ids with the same length and order.(Failed item id=0,Details see errors). For a single item, omit constraints and directly provide the top-level text/type.\n"+
			"Only register constraints explicitly written in the task target/description,do not fabricate;Use deny when unsure of type(More Conservative). ",
		obj(map[string]any{
			"constraints": map[string]any{"type": "array", "description": "[Prefer this] to add new constraint array,,Process in order. Each element:text(Required,One Constraint)+ type(allow|deny). Return ids that match the length and order of this array.", "items": map[string]any{"type": "object"}},
			"text": str("[single] Content of a single operation constraint"),
			"type": str("[single] allow(Allow)Or deny(Prohibit);Default handled as deny."),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("set_constraints Not enabled: ExplorationStore Not initialized"), nil
			}
			var a struct {
				Constraints []constraintItem `json:"constraints"`
				constraintItem // Single mode:Top-level text/type
			}
			_ = json.Unmarshal(in, &a)
			batch:= len(a.Constraints) > 0
			items:= a.Constraints
			if!batch {
				items = []constraintItem{a.constraintItem}
			}
			ids:= make([]int64, len(items))
			errs:= map[string]string{}
			for i, it:= range items {
				id, err:= t.addOneConstraint(it)
				if err!= nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
			}
			if!batch { // single:Keep simple return
				if e, bad:= errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("constraint added: %d", ids[0])), nil
			}
			out:= map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

func (t *ToolSet) addHint() actool.CoreTool {
	return t.writeExpTool("add_hint", "Hang the human/main agent's strategic hints on the exploration graph, so planners will read it when generating intentions next time.\n"+
		"★Prioritize batch processing: submit multiple hints in the hints array at once (more efficient than calling individually). Return an ids array, same length and order as hints (failed items id=0, Details see errors). For a single item, omit hints and directly give the top-level text.",
		obj(map[string]any{
			"hints": map[string]any{"type": "array", "description": "[Prioritize using this]to add new prompts array, process each element in order. Each element field is the same as the top-level fields below (text/asset_ids/traffic_refs). Return ids array with the same length and order as this array.", "items": obj(map[string]any{"text": str("Prompt content"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "traffic_refs": HintTrafficSchema()})},
			"text": str("[single] Hint content, such as'Focus on authenticated interface digging'"),
			"traffic_refs": HintTrafficSchema(),
			"asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Anchored asset id (optional, 0/1/multiple)"},
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Hints []hintItem `json:"hints"`
				hintItem // Single mode: Top-level text/asset_ids
			}
			_ = json.Unmarshal(in, &a)
			batch:= len(a.Hints) > 0
			items:= a.Hints
			if!batch {
				items = []hintItem{a.hintItem}
			}

			ids:= make([]int64, len(items))
			errs:= map[string]string{}
			var addedTexts []string
			for i, it:= range items {
				id, err:= t.addOneHint(it)
				if err!= nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
				addedTexts = append(addedTexts, strings.TrimSpace(it.Text))
			}
			if len(addedTexts) > 0 {
				// Wake up planner (in one batch). Priority notifyHint: one add_hint records one 'person added
				// N Strategic Tip:...'Trigger to make the planner clearly understand"Triggered by New Hint in This Round"And see the prompt content;
				// When not receiving the callback, revert to a pure notify (bare wake, hint still folded in the graph for it to read).
				switch {
				case t.notifyHint!= nil:
					t.notifyHint(addedTexts)
				case t.notify!= nil:
					t.notify()
				}
			}

			if!batch { // Single item: keep original return
				if e, bad:= errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("hint added: %d", ids[0])), nil
			}
			out:= map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

// killWorkTool lets the planner terminate a single running work (by intent id).
func (t *ToolSet) killWorkTool() actool.CoreTool {
	return t.writeExpTool("kill_work", "Terminate an ongoing intent(work). Used to stop off-topic or meaningless exploration; the stopped intent is marked as 'stopped' and will no longer be automatically re-claimed. First use get_worker_output to see what it's doing before deciding.",
		obj(map[string]any{"intent_id": idp("Intent ID to terminate ()= work Handle)")}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.killWork == nil {
				return actool.Errorf("kill_work Currently unavailable"), nil
			}
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
			}
			_ = json.Unmarshal(in, &a)
			id:= pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id Required"), nil
			}
			node, err:= t.ts.GetNode(id)
			if err!= nil || node == nil || node.Kind!= db.KindIntent {
				return actool.Errorf("intent_id Must be this task's intent (associated task intents are read-only)."), nil
			}
			if err:= t.killWork(id); err!= nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(fmt.Sprintf("Already aligned with intent %d Send termination signal to the work", id)), nil
		})
}

// steerWorkTool lets the planner inject a mid-run course-correction into a running
// work WITHOUT killing it: the message reaches the worker before its next tool call,
// which re-plans its next step (already-gathered context is kept). For in-intent
// nudges ("Stop X, focus on Y."); if the whole direction is wrong use kill_work + a new intent.
func (t *ToolSet) steerWorkTool() actool.CoreTool {
	return t.writeExpTool("steer_work", "Give one running intent(work)Real-time injection of correction instructions without interrupting it or discarding existing progress: The worker will receive your instruction before the next action and adjust accordingly. Used to'Don't walk X anymore, focus on Y.'Corrective action within the scope of intent; if the direction is entirely wrong, use kill_work and issue a new intent. It's recommended to first use get_worker_output to see what it's doing.",
		obj(map[string]any{
			"intent_id": idp("Intent ID to correct ((= work Handle)"),
			"message": str("Corrective instructions given to worker, clearly telling it what to stop and what to turn to"),
		}, "intent_id", "message"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.steerWork == nil {
				return actool.Errorf("steer_work Currently unavailable"), nil
			}
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
				Message string `json:"message"`
			}
			_ = json.Unmarshal(in, &a)
			id:= pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id Required"), nil
			}
			node, err:= t.ts.GetNode(id)
			if err!= nil || node == nil || node.Kind!= db.KindIntent {
				return actool.Errorf("intent_id Must be this task's intent (associated task intents are read-only)."), nil
			}
			if strings.TrimSpace(a.Message) == "" {
				return actool.Errorf("message Required"), nil
			}
			if err:= t.steerWork(id, a.Message); err!= nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(fmt.Sprintf("Already aligned with intent %d The work injection correction instruction (effective next step)", id)), nil
		})
}

// getWorkerOutput returns a work's final (orUntil termination) conclusion text by intent id.
func (t *ToolSet) getWorkerOutput() actool.CoreTool {
	return t.readExpTool("get_worker_output", "Take the current task or directly associate with a certain intent of an associated task.(work)The final output conclusion. Associated task results include source_task_id/inherited=true And is read-only. Normally ends with its summary; terminated(stopped)/An exception work returns its last output up to when it was terminated.",
		obj(map[string]any{"intent_id": idp("Intent ID (= work Handle)")}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
			}
			_ = json.Unmarshal(in, &a)
			id:= pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id Required"), nil
			}
			intentNode, err:= t.ts.GetNodeWithSources(id)
			if err!= nil {
				return actool.Errorf(err.Error()), nil
			}
			if intentNode == nil || intentNode.Kind!= db.KindIntent {
				return actool.Errorf("intent_id Does not belong to this task or directly associated tasks"), nil
			}
			acts, _, err:= t.ts.ActivityListWithSources(id, 0, 1000)
			if err!= nil {
				return actool.Errorf(err.Error()), nil
			}
			var chosen, fallback *db.Activity
			for i:= range acts {
				switch acts[i].Kind {
				case "result":
					chosen = &acts[i]
					fallback = &acts[i]
				case "text":
					fallback = &acts[i]
				}
			}
			pick:= chosen
			if pick == nil {
				pick = fallback
			}
			if pick == nil {
				if intentNode.Inherited {
					return jsonResult(inheritedMap(map[string]any{
						"intent_id": id, "final_text": " (This work has no output yet.",
					}, intentNode.SourceTaskID))
				}
				return actool.Text(" (This work has no output yet."), nil
			}
			detail, _:= t.ts.ActivityDetailWithSources(pick.ID)
			if detail == "" {
				detail = pick.Summary
			}
			result:= map[string]any{
				"intent_id": id, "final_text": detail,
				"summary": pick.Summary, "is_error": pick.IsError,
			}
			if intentNode.Inherited {
				inheritedMap(result, intentNode.SourceTaskID)
			}
			return jsonResult(result)
		})
}

// traceSteps renders summary-only trace rows, re-truncating each summary to 100
// chars — the stored summary is capped at 200 for the UI transcript; the trace
// tools want it tighter since a whole work's step list is many rows.
func traceSteps(acts []db.Activity) []map[string]any {
	steps:= make([]map[string]any, 0, len(acts))
	for i:= range acts {
		step:= map[string]any{
			"step_id": acts[i].ID, "kind": acts[i].Kind, "tool": acts[i].Tool,
			"is_error": acts[i].IsError, "summary": firstLine(acts[i].Summary, 100),
		}
		if acts[i].Inherited {
			inheritedMap(step, acts[i].SourceTaskID)
		}
		steps = append(steps, step)
	}
	return steps
}

// getWorkerTrace exposes a work's execution PROCESS (not just its final output):
// list step summaries, keyword-search within one work, or pull full detail of a
// few specific steps. Thinking steps are excluded everywhere.
func (t *ToolSet) getWorkerTrace() actool.CoreTool {
	return t.readExpTool("get_worker_trace",
		"View a specific intent(work)The 'execution process' (distinguished from get_worker_output which only gives the final conclusion). Three uses:\n"+
			"① Only pass intent_id → Return summary stream for each step of this work (summary≤100Characters, containing step_id; just action outlines, no complete output); \n"+
			"② intent_id + q → Only return summary of steps that hit keywords (search in both summary and full output; still only give summary, look at content to use③); \n"+
			"③ intent_id + step_ids → Returns the full content of these steps(detail); Take at most 5 items; if more than 5, return only the first 5 and inform in notice/omitted_step_ids about those not taken.\n"+
			"Typical flow: first①/②Locate the suspicious step's step_id, then use③Take its full output. No thinking included(thinking)Step. Supports directly associating task history traces; their results include source_task_id/inherited.=true and read-only.",
		obj(map[string]any{
			"intent_id": idp("Intent ID (= work Handle)"),
			"q": str("Keywords: Only return summary/comprehensive output that hits its step (optional; mutually exclusive with step_ids)."),
			"step_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "The step_id to retrieve full content (from①/②Return; up to 5 at a time, extra in omitted_step_ids."},
			"limit": intp("Summary stream/retrieval return limit (optional)."),
		}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
				Q string `json:"q"`
				StepIDs []json.RawMessage `json:"step_ids"`
				Limit int `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			id:= pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id Required"), nil
			}
			intentNode, nodeErr:= t.ts.GetNodeWithSources(id)
			if nodeErr!= nil {
				return actool.Errorf(nodeErr.Error()), nil
			}
			if intentNode == nil || intentNode.Kind!= db.KindIntent {
				return actool.Errorf("intent_id Does not belong to this task or directly associated tasks"), nil
			}
			// ③ detail drill-down by step ids, thinking excluded by the store.
			if len(a.StepIDs) > 0 {
				// Dedup + drop invalid ids first so garbage/duplicates don't eat into
				// the per-call cap. detail is returned in full (untruncated), so the
				// cap bounds one tool result; over the cap we serve the first N and
				// tell the model exactly which ids were deferred, instead of erroring
				// and forcing it to re-plan the call.
				const maxStepIDs = 5
				var ids []int64
				seen:= make(map[int64]bool)
				for _, raw:= range a.StepIDs {
					if v:= pid(raw); v > 0 &&!seen[v] {
						seen[v] = true
						ids = append(ids, v)
					}
				}
				var omitted []int64
				if len(ids) > maxStepIDs {
					omitted = append(omitted, ids[maxStepIDs:]...)
					ids = ids[:maxStepIDs]
				}
				acts, err:= t.ts.ActivityByIDsWithSources(ids)
				if err!= nil {
					return actool.Errorf(err.Error()), nil
				}
				steps:= make([]map[string]any, 0, len(acts))
				for i:= range acts {
					if acts[i].NodeID == nil || *acts[i].NodeID!= id || acts[i].Inherited!= intentNode.Inherited ||
						(acts[i].Inherited && acts[i].SourceTaskID!= intentNode.SourceTaskID) {
						continue
					}
					step:= map[string]any{
						"step_id": acts[i].ID, "kind": acts[i].Kind, "tool": acts[i].Tool,
						"is_error": acts[i].IsError, "detail": acts[i].Detail,
					}
					if acts[i].Inherited {
						inheritedMap(step, acts[i].SourceTaskID)
					}
					steps = append(steps, step)
				}
				result:= map[string]any{"intent_id": id, "steps": steps, "returned_step_ids": ids}
				if len(omitted) > 0 {
					// returned_step_ids/omitted_step_ids let the model decide programmatically
					// whether another call is worth it; the notice states the same in prose.
					result["omitted_step_ids"] = omitted
					result["notice"] = fmt.Sprintf(
						"Take at most %d The complete content of each step, only the first part returned this time %d a%v), Unacquired %d each for %v. "+
							"If these contents are sufficient to locate it, there's no need to take the remaining steps; if you need to continue, use these step_id again.",
						maxStepIDs, len(ids), ids, len(omitted), omitted)
				}
				if intentNode.Inherited {
					inheritedMap(result, intentNode.SourceTaskID)
				}
				return jsonResult(result)
			}
			// ①/② summary stream, optionally keyword-filtered; 100-char summaries.
			var acts []db.Activity
			var err error
			if strings.TrimSpace(a.Q)!= "" {
				acts, err = t.ts.ActivityTraceSearchWithSources(id, a.Q, a.Limit)
			} else {
				acts, err = t.ts.ActivityTraceWithSources(id, a.Limit)
			}
			if err!= nil {
				return actool.Errorf(err.Error()), nil
			}
			result:= map[string]any{"intent_id": id, "steps": traceSteps(acts)}
			if intentNode.Inherited {
				inheritedMap(result, intentNode.SourceTaskID)
			}
			return jsonResult(result)
		})
}

// searchAllWorkerTraces keyword-searches EVERY work's process in this task — for
// finding what a worker saw but never wrote back as a fact. Returns only matching
// summaries (≤100 chars), each tagged with its intent_id for follow-up drill-down.
func (t *ToolSet) searchAllWorkerTraces() actool.CoreTool {
	return t.readExpTool("search_all_worker_traces",
		"[Usually not recommended because the system already provides most information]In[This task's other work execution process], by keyword(q)retrieve——Used to retrieve something that a worker has seen but didn't write into fact (e.g., path/token/error)."+
			"Automatically excluded your own step in this intent (those already in your context)."+
			"Only return summaries of hit steps(summary≤100word), Each with intent_id; use get_worker_trace based on this(intent_id, step_ids=[…]) Get full content.",
		obj(map[string]any{
			"q": str("Keywords (in all work step summaries)+Search for in complete output)"),
			"limit": intp("Return limit, default 100 (optional)"),
		}, "q"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Q string `json:"q"`
				Limit int `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Q) == "" {
				return actool.Errorf("q Required"), nil
			}
			// Excludes this step in the intent chain called by the caller (the worker's own trace is already in its context).
			acts, err:= t.ts.ActivityTraceSearchAllWithSources(t.ownerNode, a.Q, a.Limit)
			if err!= nil {
				return actool.Errorf(err.Error()), nil
			}
			hits:= make([]map[string]any, 0, len(acts))
			for i:= range acts {
				var intent int64
				if acts[i].NodeID!= nil {
					intent = *acts[i].NodeID
				}
				hit:= map[string]any{
					"intent_id": intent, "step_id": acts[i].ID, "worker": acts[i].Worker,
					"kind": acts[i].Kind, "tool": acts[i].Tool, "is_error": acts[i].IsError,
					"summary": firstLine(acts[i].Summary, 100),
				}
				if acts[i].Inherited {
					inheritedMap(hit, acts[i].SourceTaskID)
				}
				hits = append(hits, hit)
			}
			return jsonResult(map[string]any{"query": a.Q, "hits": hits})
		})
}

// listWorkerTraces gives a worker (which has no graph_overview and can't see the
// intent graph) a lightweight index of the works in this task — intent_id +
// one-line summary + state — so it can DISCOVER which works to inspect via
// get_worker_trace. Without this a worker only knows intent_ids that come back
// from search_all_worker_traces hits. Excludes still-open intents (not yet run →
// no process to inspect).
func (t *ToolSet) listWorkerTraces() actool.CoreTool {
	return t.readExpTool("list_worker_traces",
		"[Usually not recommended because the system already provides most of the information. List indices of tasks run in this task [intents]: intent_id + One-way direction(summary) + Status."+
			"you(worker)You can't see the exploration graph. Use it to discover which work items are worth reviewing.——Use get_worker_trace again(intent_id) Check its steps, get_worker_trace.(intent_id, step_ids=[…]) Get Details."+
			"Only list executed(running/done/exhausted/blocked/stopped), Does not include unfinished open tasks. Note: Your task boundaries are still the intent you received; looking at other work is for reuse/avoiding repeated labor.",
		obj(map[string]any{
			"q": str("Filter by summary keyword (optional)"),
			"limit": intp("Return limit, default 50 (optional)."),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Q string `json:"q"`
				Limit int `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			limit:= a.Limit
			if limit <= 0 {
				limit = 50
			}
			all, err:= t.ts.ListByKindWithSources(db.KindIntent, 500)
			if err!= nil {
				return actool.Errorf(err.Error()), nil
			}
			q:= strings.ToLower(strings.TrimSpace(a.Q))
			out:= make([]map[string]any, 0, limit)
			for _, n:= range all {
				if n.Inherited && n.State == "running" {
					continue
				}
				switch n.State {
				case "running", "done", "exhausted", "blocked", "stopped": // has run → has a process
				default:
					continue
				}
				var p map[string]any
				_ = json.Unmarshal(n.Payload, &p)
				summary, _:= p["summary"].(string)
				if q!= "" &&!strings.Contains(strings.ToLower(summary), q) {
					continue
				}
				item:= map[string]any{"intent_id": n.ID, "summary": summary, "state": n.State}
				if n.Inherited {
					inheritedMap(item, n.SourceTaskID)
				}
				out = append(out, item)
				if len(out) >= limit {
					break
				}
			}
			return jsonResult(map[string]any{"works": out})
		})
}

// PlannerTools is the read + intent-generation + goal-judgement tool set.
func (t *ToolSet) PlannerTools() []actool.CoreTool {
	return []actool.CoreTool{
		t.graphOverview(), t.listFindings(), t.listFacts(), t.nodeDetail(),
		// cold-digest §6.1: restore folded cold nodes (digest body → members → detail).
		t.expandDigest(),
		t.getWorkerOutput(), t.getWorkerTrace(), t.searchAllWorkerTraces(), t.listGoals(), t.addIntent(), t.proveGoal(), t.goalMet(),
		t.killWorkTool(), t.steerWorkTool(),
		// report_finding: When planning situational analysis, if self-confirmed vulnerability, directly register (same tool as worker).
		t.addFinding(),
		// list_companies: View Enterprise List + scope + Asset count (using company_id / understanding the scope of ownership).
		t.listCompanies(),
		// list_assets: Retrieve the full asset library when planning (in conjunction with list_untested_assets)"Not tested within scope"Perspective,
		// Supplement"Search in the entire library by domain/fingerprint/port/status code etc.") ability.
		t.listAssets(),
		// add_company_scope: When planning, include domain/IP/CIDR/ICP/keywords in a company's asset range (automatically claim assets that match).
		t.addCompanyScope(),
		// add_task_scope: Proactively include the entire domain, company, subdomain, or IP in this task's test scope.(Coverage denominator).
		t.addTaskScope(),
		// list_untested_assets: Query undetected assets within this task scope as needed.(type+pagination), Decide to retest.
		t.listUntestedAssets(),
	}
}
