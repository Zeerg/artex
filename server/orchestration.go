package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/Zeerg/artex/agent"
	"github.com/Zeerg/artex/db"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// jsonResult marshals v to a JSON tool result.
func jsonResult(v any) (actool.Result, error) {
	b, err:= json.Marshal(v)
	if err!= nil {
		return actool.Errorf(err.Error()), nil
	}
	return actool.Text(string(b)), nil
}

// This file implements P2'cross-task orchestration toolset'(docs/Scoring arrangement §2 P2). These are host tools.——Requires
// access Manager(Any task's Store), Engine(Pause), And task creation process.,So it resides at the server layer.
// Reading tools redirect 'existing per-task tools' to the target task's store for running(Create a temporary ToolSet
// And call its corresponding tool.),Thus reusing exactly the same logic;Control class(spawn/pause)Directly call Manager/Engine.
// They seed into tools table like traffic tools, bound by agent.(Visible only to the orchestration agent.).

// hostTools is the runtime host-tool provider fed to ToolAugment: traffic tools
// (gated by capture) + cross-task orchestration tools + user-defined custom tools.
// The second return is the names of custom tools flagged `deferred` (schema
// withheld, routed via SearchExtraTools/ExecuteExtraTool). Per-agent binding still
// decides who actually sees any of them.
//
//nolint:unused // used as the hostTools provider in wireAgentAugment
func (s *Server) hostTools() ([]actool.CoreTool, map[string][]string) {
	tools:= append(s.m.HostTools(), s.orchestrationTools()...)
	tools = append(tools, s.findingRetestTools()...)
	tools = append(tools, s.platformTools()...) // Platform operation tool(Modify skill/tool/MCP for Auto to use)
	custom, err:= s.customTools()
	if err!= nil {
		log.Printf("[custom-tool] load failed: %v", err)
		return tools, nil
	}
	tools = append(tools, custom...)
	// deferred custom tools → name -> its bound agent keys. ToolAugment turns a
	// name into a deferred entry only for agents it's actually bound to (so we don't
	// advertise a tool the per-agent binding will drop from the callable set).
	deferred:= map[string][]string{}
	rows, _:= s.m.pg.ListCustomTools()
	for _, t:= range rows {
		if t.Deferred && t.Enabled {
			deferred[t.Key] = t.Agents
		}
	}
	return tools, deferred
}

// orchestrationTools returns the cross-task tool set. Bound per-agent via the
// tools table (default: no binding — opt-in for orchestration agents).
func (s *Server) orchestrationTools() []actool.CoreTool {
	return []actool.CoreTool{
		s.toolListTasks(),
		s.toolListLLMProfiles(),
		s.toolSpawnTask(),
		s.toolPauseTask(),
		s.toolGetTaskGraph(),
		s.toolListTaskFindings(),
		s.toolAddHint(),
		s.toolGetWorkerTrace(),
		s.toolListWorkerTraces(),
		s.toolSearchWorkerTraces(),
		s.toolGetTaskNodeDetail(),
		s.toolUpdateFindingReport(),
		s.toolGetFindingTraffic(),
		s.toolBindFindingTraffic(),
	}
}

// --- schema helpers ---

func strParam(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// parseProfileID reads an LLM profile id from a tool arg that may arrive as a JSON
// number (5) or a numeric string ("5"); returns 0 when absent/unparseable.
func parseProfileID(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		v, _:= strconv.ParseInt(strings.TrimSpace(str), 10, 64)
		return v
	}
	return 0
}

func objSchema(props map[string]any, required ...string) map[string]any {
	m:= map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		req:= make([]any, len(required))
		for i, r:= range required {
			req[i] = r
		}
		m["required"] = req
	}
	return m
}

func roTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		ReadOnly: func(json.RawMessage) bool { return true },
		Concurrent: func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

func wrTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

// delegateToTask resolves the `task_id` in the input, builds a ToolSet bound to
// that task's store, strips task_id, and calls the chosen per-task tool — so the
// cross-task read reuses the exact in-task logic against another task.
func (s *Server) delegateToTask(ctx context.Context, in json.RawMessage, pick func(*agent.ToolSet) actool.CoreTool) (actool.Result, error) {
	var head struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(in, &head)
	if strings.TrimSpace(head.TaskID) == "" {
		return actool.Errorf("task_id Must be filled in"), nil
	}
	t, ok:= s.m.Task(head.TaskID)
	if!ok {
		return actool.Errorf("task Does not exist: " + head.TaskID), nil
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(in, &m)
	delete(m, "task_id")
	inner, _:= json.Marshal(m)
	tsx:= agent.NewToolSet(t.Store, "orchestrator")
	if s.m.Assets()!= nil {
		tsx.SetAssetStore(s.m.Assets(), s.m.Assets().Companies())
	}
	tsx.SetNotify(t.Notify) // General wake-up (for write operations without dedicated callbacks; read tools are no-op)
	tsx.SetNotifyHint(t.NotifyHint) // add_hint → Log a note 'A user added N strategic hints:...'Trigger and wake up planner.
	return pick(tsx).Call(ctx, inner, nil)
}

// --- tools ---

func (s *Server) toolListTasks() actool.CoreTool {
	return roTool("list_tasks",
		"List all tasks(id/Description/Target/Status/Run Time/Parent Task/LLM Configuration), Orchestration agent uses it to grasp the global picture, see which tasks are stuck too long, and use which LLM. Runtime duration: running=Create→Now, final state=Create→Last activity(Second). llm_profile: Configuration name used by planner/worker,(Activate configuration)=Follow global activation.",
		objSchema(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			lastAct, _:= s.m.PG().LastActivityAll()
			// id -> name to resolve each task's pinned LLM profile.
			profName:= map[int64]string{}
			if profs, err:= s.m.pg.ListProfiles(); err == nil {
				for _, p:= range profs {
					profName[p.ID] = p.Name
				}
			}
			out:= make([]map[string]any, 0)
			for _, t:= range s.m.List() {
				status:= s.deriveTaskStatus(t)
				end:= lastAct[t.ExpID]
				if live:= s.engine.LastActivity(t.ID); live > end {
					end = live
				}
				dur:= int64(0)
				if status == "running" {
					dur = time.Now().Unix() - t.CreatedAt
				} else if end > t.CreatedAt {
					dur = end - t.CreatedAt
				}
				row:= map[string]any{"id": t.ID, "description": t.Description, "goal": t.Goal, "status": status, "run_seconds": dur}
				if t.ParentRef!= "" {
					row["parent_ref"] = t.ParentRef
				}
				llmState:= t.llmStateSnapshot()
				if llmState.ProfileID == nil {
					row["llm_profile"] = "(Activate configuration)"
				} else if n, ok:= profName[*llmState.ProfileID]; ok {
					row["llm_profile"] = n
				} else {
					row["llm_profile"] = fmt.Sprintf("#%d(Deleted)", *llmState.ProfileID)
				}
				out = append(out, row)
			}
			return jsonResult(out)
		})
}

// toolListLLMProfiles lists the available LLM profiles (name/model/active) so an
// orchestration agent can pick one for spawn_task's llm_profile. Never leaks keys.
func (s *Server) toolListLLMProfiles() actool.CoreTool {
	return roTool("list_llm_profiles",
		"List available LLM configurations(profile): id, Name, model, format, whether it is the current active configuration. Use id to specify a sub-task-specific LLM for spawn_task's llm_profile_id parameter (e.g., cheap models for reconnaissance, strong models for exploitation). Does not include API Key.",
		objSchema(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			profs, err:= s.m.pg.ListProfiles()
			if err!= nil {
				return actool.Errorf(err.Error()), nil
			}
			out:= make([]map[string]any, 0, len(profs))
			for _, p:= range profs {
				out = append(out, map[string]any{
					"id": p.ID, "name": p.Name, "model": p.Model, "format": p.Format, "is_active": p.IsDefault,
				})
			}
			return jsonResult(map[string]any{"profiles": out})
		})
}

func (s *Server) toolSpawnTask() actool.CoreTool {
	return wrTool("spawn_task",
		"Creates a new sub-task and starts the exploration engine, returning task_id. Used to parse out target host/IP without creating a site (please manually configure scope).(Like a question/one target)Assign as an independent task. parent_ref optional: fill the current orchestration's associated parent task ID for parent-child association.",
		objSchema(map[string]any{
			"description": strParam("Task description(Short title)"),
			"goal": strParam("Task target(What to achieve)"),
			"parent_ref": strParam("Optional: Parent task ID(Establish parent-child relationship)"),
			"source_task_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": fmt.Sprintf("Optional: Only read-inherited source task ID list.(Maximum %d Pages). Subtasks can only read-only reference these tasks' discovered assets/conclusions as starting points; unlike parent_ref's pure parent-child pointers, this is content inheritance.", db.MaxTaskSourceCount)},
			"llm_profile_id": map[string]any{"type": "integer", "description": "Optional: Specify the LLM configuration id used by this sub-task planner/worker.(See list_llm_profiles); Leaving it blank inherits the parent task, then reverts to global active configuration."},
			"timeout_seconds": map[string]any{"type": "integer", "description": "Optional: Task-level timeout(Second). Trigger graceful end and enter timeout terminal state upon reaching the time; leave empty or 0 = Unlimited"},
			"plan_heartbeat_seconds": map[string]any{"type": "integer", "description": "Optional: planner heartbeat trigger interval(Second). After the last planning round ends/task starts by this value and no triggers during that period. → Trigger a round of planning(fallback deadlock + Wake up to monitor flying workers.). Leave empty or 0 = Default 600(10min); "},
			"seed_first_intent": map[string]any{"type": "boolean", "description": "Optional: For simple tasks, you can enable it and directly issue a seed intent at creation time.(Content=Describe+Target)Change strategy: Read tool_logs, change harvest, check reference.(Follow the standard first planning then execution). "},
		}, "description", "goal"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Description string `json:"description"`
				Goal string `json:"goal"`
				ParentRef string `json:"parent_ref"`
				SourceTaskIDs []string `json:"source_task_ids"`
				LLMProfileID json.RawMessage `json:"llm_profile_id"`
				TimeoutSeconds int `json:"timeout_seconds"`
				PlanHeartbeatSeconds int `json:"plan_heartbeat_seconds"`
				SeedFirstIntent bool `json:"seed_first_intent"`
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Description) == "" {
				a.Description = "Unnamed task"
			}
			if strings.TrimSpace(a.Goal) == "" {
				return actool.Errorf("goal Must be filled in"), nil
			}
			if a.TimeoutSeconds < 0 {
				a.TimeoutSeconds = 0
			}
			// Read-only inherited task count limit + Each id must be valid/unique/exist, with validation rules consistent with HTTP task creation.
			if len(a.SourceTaskIDs) > db.MaxTaskSourceCount {
				return actool.Errorf(fmt.Sprintf("The most selected associated tasks %d Pages", db.MaxTaskSourceCount)), nil
			}
			sourceIDs:= make([]int64, 0, len(a.SourceTaskIDs))
			seenSources:= map[int64]bool{}
			for _, raw:= range a.SourceTaskIDs {
				id, err:= strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
				if err!= nil || id <= 0 || seenSources[id] {
					return actool.Errorf("Invalid or duplicate associated task id."), nil
				}
				if _, ok:= s.m.Task(strconv.FormatInt(id, 10));!ok {
					return actool.Errorf(fmt.Sprintf("Associated task #%d Does not exist", id)), nil
				}
				seenSources[id] = true
				sourceIDs = append(sourceIDs, id)
			}
			// LLM profile resolution: explicit id > inherit parent's pin > active(nil).
			var pin *int64
			if id:= parseProfileID(a.LLMProfileID); id > 0 {
				if _, ok:= s.loadProfileConfig(id);!ok {
					return actool.Errorf(fmt.Sprintf("LLM Version #%d API Key does not exist or is not set.", id)), nil
				}
				pin = &id
			} else if a.ParentRef!= "" {
				if pt, ok:= s.m.Task(a.ParentRef); ok {
					pin = pt.LLMProfileID
				}
			}
			var llmIDs []int64
			if pin!= nil {
				llmIDs = []int64{*pin}
			}
			t, err:= s.m.CreateTaskWithOptions(a.Description, a.Goal, db.TaskCreateOptions{
				SourceTaskIDs: sourceIDs,
				LLMProfileIDs: llmIDs,
				TimeoutSeconds: a.TimeoutSeconds,
				PlanHeartbeatSeconds: a.PlanHeartbeatSeconds,
			})
			if err!= nil {
				return actool.Errorf(err.Error()), nil
			}
			if a.ParentRef!= "" {
				t.ParentRef = a.ParentRef
				if id, e:= strconv.ParseInt(t.ID, 10, 64); e == nil {
					_ = s.m.PG().SetParentRef(id, a.ParentRef)
				}
			}
			// Shared post-build process.,create task via HTTP(server.go createTask)Reuse the same launchTask:
			// seed + Perform target decomposition in the background.(First round/LLM step/goal) + engine.Run.
			// seed_first_intent Default false(Standard first plan then execute);Simple tasks can enable direct issuance of one work for testing.
			s.launchTask(t, a.Description+" "+a.Goal, a.SeedFirstIntent)
			return actool.Text(fmt.Sprintf("task created: %s", t.ID)), nil
		})
}

func (s *Server) toolPauseTask() actool.CoreTool {
	return wrTool("pause_task", "Pause specified task(Stop its planner/worker loop.). ",
		objSchema(map[string]any{"task_id": strParam("ID of the task to pause.")}, "task_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				TaskID string `json:"task_id"`
			}
			_ = json.Unmarshal(in, &a)
			t, ok:= s.m.Task(a.TaskID)
			if!ok {
				return actool.Errorf("task Does not exist: " + a.TaskID), nil
			}
			if _, err:= s.applyTaskControlWithCause(t, "pause", agent.AbortPausedByOrchestrator); err!= nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text("task paused: " + a.TaskID), nil
		})
}

func (s *Server) toolGetTaskGraph() actool.CoreTool {
	return roTool("get_task_graph", "Read the overview of exploration graphs for a specified task(Similar to graph_overview: asset count/frontier/discovery/coverage etc.), Use task_id to specify the task.",
		objSchema(map[string]any{"task_id": strParam("Task ID")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).GraphOverviewTool)
		})
}

func (s *Server) toolListTaskFindings() actool.CoreTool {
	return roTool("list_task_findings", "Confirm vulnerabilities for the specified task(Contains flag/PoC; each with id/task_id/intent_id/vulnclass/severity/summary/status), Use task_id to specify the task.",
		objSchema(map[string]any{"task_id": strParam("Task ID")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).ListFindingsTool)
		})
}

func (s *Server) toolAddHint() actool.CoreTool {
	return wrTool("add_task_hint", "Inject strategic hints into a specified task.(The planner for this task will read it when generating intents next round). \n"+
		"★Prioritize batch processing: multiple prompts into the hints array in one submission (return ids array, same length as hints, failed item id=0); For individual items, omit hints and directly provide top-level text.",
		objSchema(map[string]any{
			"task_id": strParam("Task ID"),
			"hints": map[string]any{"type": "array", "description": "[Prioritize using this]prompt array, with each element's fields matching those at the top level (text/asset_ids/traffic_refs).", "items": objSchema(map[string]any{"text": strParam("Prompt content"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "traffic_refs": agent.HintTrafficSchema()})},
			"text": strParam("[single] Prompt content"),
			"traffic_refs": agent.HintTrafficSchema(),
			"asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Anchored asset id (optional, 0/1/multiple; the asset id within this task)"},
		}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).AddHintTool)
		})
}

func (s *Server) toolGetWorkerTrace() actool.CoreTool {
	return roTool("get_task_worker_trace",
		"Check a specific task's work(Intent)Execution process: get_task_worker_trace(task_id, intent_id) Review the step summary; include step_ids.=[…] Take the full content of those steps(Maximum of 5 at a time,only returns the first 5). ",
		objSchema(map[string]any{
			"task_id": strParam("Task ID"),
			"intent_id": map[string]any{"type": "integer", "description": "Intent ID(work within this task)"},
			"step_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Optional: Step ID to fetch the full content(Maximum of 5 at a time,only returns the first 5,Others listed in omitted_step_ids)"},
		}, "task_id", "intent_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).GetWorkerTraceTool)
		})
}

func (s *Server) toolListWorkerTraces() actool.CoreTool {
	return roTool("list_task_worker_traces", "List which work ran in the specified task.(Intent) + Individual steps, used to discover which work values are worth reviewing.(Then use get_task_worker_trace). ",
		objSchema(map[string]any{"task_id": strParam("Task ID")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).ListWorkerTracesTool)
		})
}

func (s *Server) toolSearchWorkerTraces() actool.CoreTool {
	return roTool("search_task_worker_traces", "In a specified task, search all work execution processes by keyword(Return summary of matched steps + intent_id). ",
		objSchema(map[string]any{"task_id": strParam("Task ID"), "q": strParam("Search keyword")}, "task_id", "q"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).SearchWorkerTracesTool)
		})
}

func (s *Server) toolGetTaskNodeDetail() actool.CoreTool {
	return roTool("get_task_node_detail",
		"Reading the complete content of a specific exploration graph node from a task.(Discovery/Fact/Intent/Target: Summary + Details/Evidence/PoC). id For exploration node id(such as the id returned by report_finding or listed in list_task_findings). Use it to fetch the complete evidence for this vulnerability before writing the vulnerability report.",
		objSchema(map[string]any{
			"task_id": strParam("Task ID"),
			"id": map[string]any{"type": "integer", "description": "Explore graph node id.(Not an asset id)"},
		}, "task_id", "id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).NodeDetailTool)
		})
}

// toolUpdateFindingReport writes/overwrites a finding's detailed Markdown report.
// finding_id is the id report_finding returned ("finding recorded: <id>", the
// finding node id). The write (SetFindingReportByNodeID) is keyed by node_id and
// task-agnostic, so this host tool needs no task_id / exploration store.
func (s *Server) toolUpdateFindingReport() actool.CoreTool {
	return wrTool("update_finding_report",
		"Writing/Updating the detailed report for registered vulnerabilities.(Markdown full text,Entirely overwrite old content). finding_id passed by report_finding.(\"finding recorded: <id>\" The numbers in). Report suggestions should include:Vulnerability overview, impact and risks, reproduction steps, evidence/PoC, repair suggestions.",
		objSchema(map[string]any{
			"finding_id": map[string]any{"type": "integer", "description": "Target vulnerability ID.(report_finding Returned id)"},
			"report": strParam("Full text of the detailed report,Markdown Format"),
			"evidence_version": map[string]any{"type": "integer", "description": "get_finding_traffic The returned evidence version; used to prevent reports from being permanently marked as pending updates. Do not mix two types of numbering."},
		}, "finding_id", "report"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				EvidenceVersion *int64 `json:"evidence_version"`
				FindingID json.RawMessage `json:"finding_id"`
				Report string `json:"report"`
			}
			_ = json.Unmarshal(in, &a)
			nodeID:= parseProfileID(a.FindingID) // Reuse 'number or number string' parsing
			if nodeID <= 0 {
				return actool.Errorf("finding_id Invalid"), nil
			}
			n, err:= s.m.pg.SetFindingReportVersionByNodeID(ctx, nodeID, a.Report, a.EvidenceVersion)
			if err!= nil {
				return actool.Errorf(err.Error()), nil
			}
			if n == 0 {
				return actool.Errorf(fmt.Sprintf("Finding_id not found.=%d Corresponding vulnerability record(First register with report_finding)", nodeID)), nil
			}
			return actool.Text(fmt.Sprintf("finding %d report updated (%d chars)", nodeID, len(a.Report))), nil
		})
}

// deriveTaskStatus mirrors listTasks' status derivation for the list_tasks tool.
func (s *Server) deriveTaskStatus(t *Task) string {
	lifecycle:= t.lifecycleSnapshot()
	switch {
	case isTerminalStatus(lifecycle.Status):
		return lifecycle.Status
	case lifecycle.Paused || s.engine.IsPaused(t.ID):
		return "paused"
	case s.engine.ReadyFor(t) && s.engine.Started(t.ID):
		return "running"
	}
	return "created"
}

// orchestrationToolSeeds seeds the cross-task tools into the tools table so they
// are bindable per-agent (default: bound to nobody — opt-in for orchestration
// agents). First-insert only, like the traffic seeds.
func (s *Server) seedOrchestrationTools() {
	// task-op + platform tools default-bind to the built-in Auto agent (It is naturally used for
	// Operation platform). SeedTool Take effect on first insertion;Rows already seeded by the old library are bound by seedAutoDefaultBindings.
	autoAgents, _:= json.Marshal([]string{"auto"})
	for _, t:= range s.orchestrationTools() {
		schema, _:= json.Marshal(t.InputSchema())
		bindings:= autoAgents
		if t.Name() == "bind_finding_traffic" {
			bindings = json.RawMessage(`["reporter"]`)
		}
		_ = s.m.PG().SeedTool(t.Name(), t.Description(), schema, bindings)
	}
	for _, t:= range s.platformTools() {
		schema, _:= json.Marshal(t.InputSchema())
		_ = s.m.PG().SeedTool(t.Name(), t.Description(), schema, autoAgents)
	}
	s.refreshBuiltinToolSchemas()
	s.seedAutoDefaultBindings()
	s.seedPlannerDefaultBindings()
	s.seedPlannerListAssetsBinding()
	s.seedCompanyScopeRebind()
	s.seedWorkerReadToolsUnbind() // list_facts/list_companies/list_worker_traces Unbind from worker by default(One-time)
	s.seedWorkerReadbackRebind() // Fix old migration errors by reattaching search_all_worker_traces/get_worker_trace/node_detail to worker(One-time)
	s.seedAutoReportFindingBinding()
	s.unbindGoalMetDefault()
	s.reseedGoalsPrompt() // goals Add 'draw operation constraint' step to the prompt → Append a new default version to old library(One-time)
	s.reseedMainAgentPrompt() // mainagent Add a prompt in 'add_intent' after achieving the goal to ask if you want to create a new target.(One-time)
	s.reseedPlannerPrompt() // planner Prompt:Rewrite '0 intent' justifiable reasons + Quantitative acceptance verification.(One-time)
	s.reseedWorkerPrompt() // worker Prompt:Add a negative conclusion evidence threshold(One-time)
	s.seedReporterAgent() // Preset 'report writing' agent + tool binding + finding Trigger(One-time)
	s.upgradeReporterTriggerMessage() // Migrate old library:Let reporter return evidence_version(One-time)
	s.seedFindingTrafficTools() // Add optional evidence parameters and read-only evidence tools, retaining user configuration.
	s.seedFindingWorkflowTools()
	// Note: Default tool bindings for pentest do not need migration.——BuiltinToolSeeds Initialize with
	// list_assets/insert_assets/report_finding/list_findings/list_companies Imported
	// pentest Seed together (project has no old library, no migration).
}

// refreshBuiltinToolSchemas propagates code schema/description changes on the
// orchestration + platform tools into already-seeded rows ONCE per version flag —
// SeedTool is first-insert-only, so a new param (e.g. spawn_task The llm_profile) never
// reaches an old DB otherwise. Preserves each tool's agent binding + enabled flag.
// Bump the flag whenever these tools' schemas/descriptions change in code.
func (s *Server) refreshBuiltinToolSchemas() {
	const flag = "tool_schema_refresh_v7_list_facts_paging"
	if v, _, _:= s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	tools:= append(s.orchestrationTools(), s.platformTools()...)
	for _, t:= range tools {
		schema, _:= json.Marshal(t.InputSchema())
		if err:= s.m.pg.RefreshToolDefaults(t.Name(), t.Description(), schema); err!= nil {
			log.Printf("[tools] refresh %s schema failed: %v", t.Name(), err)
		}
	}
	// Simultaneously convert some built-in agent tools into code defaults:
	// - goal_met: Description of old library seed includes“End current round planning”The misdirection would make planner treat it as
	// “End Empty Round,”The method, misjudging the entire task as completed right after it starts.
	// - insert_assets: Add a new related parameter(Mark whether the asset is related to the current task and decide whether it should be included in coverage.),
	// SeedTool First insert only, old library already seeded schema otherwise won't receive this new parameter.
	// - list_facts: Change to pagination, add new limit/before/q parameters; old library has seeded empty schema otherwise
	// Display 'No Parameters' on the tool management page, and if the model doesn't receive these parameters, it's a problem.
	refreshBuiltin:= map[string]bool{"goal_met": true, "insert_assets": true, "list_facts": true}
	for _, sd:= range agent.BuiltinToolSeeds() {
		if!refreshBuiltin[sd.Key] {
			continue
		}
		schema, _:= json.Marshal(sd.Schema)
		if err:= s.m.pg.RefreshToolDefaults(sd.Key, sd.Desc, schema); err!= nil {
			log.Printf("[tools] refresh %s desc failed: %v", sd.Key, err)
		}
	}
	_ = s.m.pg.SetSetting(flag, "true")
	log.Printf("[tools] The orchestration/platform tool schema has been refreshed to code defaults.(One-time)")
}

// unbindGoalMetDefault removes goal_met's default "planner" binding ONCE (guarded by
// a settings flag), so existing DBs match the new default of NO agent. goal_met bypasses
// per-goal prove_goal to declare the whole task done — powerful/risky and redundant with
// the prove_goal→auto-complete path — so it ships unbound; users can re-bind it per agent
// in the UI. A user's own binding to another agent is untouched (we only strip planner).
func (s *Server) unbindGoalMetDefault() {
	const flag = "goal_met_unbind_default_v1"
	if v, _, _:= s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err:= s.m.pg.RemoveAgentFromTool("planner", "goal_met"); err!= nil {
		log.Printf("[tools] goal_met Failed to unbind planner: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// reseedGoalsPrompt Reformat the goals target parser prompt to ['Current code default']——Because the default body added new
// 'First extract operation constraints(set_constraints)Split the target at this step,SeedPromptIfEmpty inserts only,Old Database
// The existing version 1 does not receive this step. Here use version management [append a new version] and switch to it(ResetPromptToDefault),
// Old versions still retained in history,If user has customized it, can be recovered from version records. settings flag guards → Do once;
// the default will change again in the future by bumping this flag. There is no need to handle(SeedPromptIfEmpty Already seeded with the latest default).
func (s *Server) reseedGoalsPrompt() {
	const flag = "goals_prompt_constraint_step_v1"
	if v, _, _:= s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // Try only once, regardless of success or failure
	a, err:= s.m.pg.GetAgentByKey("goals")
	if err!= nil || a == nil {
		return // A new library has not yet built an agent line.,seedPrompts Will directly seed the latest default,No need for this migration
	}
	tmpl:= agent.BuiltinPromptSeeds()["goals"]
	if tmpl == "" {
		return
	}
	// The new library seedPrompts has been seeded with the latest default. → The current version is already equal to the code default,No need to append duplicate versions.
	if cur, err:= s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err:= s.m.pg.ResetPromptToDefault(a.ID, tmpl); err!= nil {
		log.Printf("[prompts] goals Prompt refreshed to new default failure.: %v", err)
		return
	}
	log.Printf("[prompts] goals Prompt already appended new default version(Add draw operation constraint step,One-time)")
}

// reseedMainAgentPrompt Brush mainagent prompts as [Current code default]——Default body added 'Target all
// After reaching it, add_intent directly shoots the intent when,'Did the interrogator register as a formal target?',While SeedPromptIfEmpty inserts first
// only,Old library versions are not receiving updates. Use version management [Append a new version] and switch to it.(ResetPromptToDefault),Old versions still
// Retain in history,If user has customized it, can be recovered from version records. settings flag guards → Only once. No need to handle new libraries
// (SeedPromptIfEmpty Already seeded with the latest default). Exactly the same structure as reseedGoalsPrompt.
func (s *Server) reseedMainAgentPrompt() {
	const flag = "mainagent_prompt_goalless_intent_v1"
	if v, _, _:= s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // Try only once, regardless of success or failure
	a, err:= s.m.pg.GetAgentByKey("mainagent")
	if err!= nil || a == nil {
		return // A new library has not yet built an agent line.,seedPrompts Will directly seed the latest default,No need for this migration
	}
	tmpl:= agent.BuiltinPromptSeeds()["mainagent"]
	if tmpl == "" {
		return
	}
	// The new library seedPrompts has been seeded with the latest default. → The current version is already equal to the code default,No need to append duplicate versions.
	if cur, err:= s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err:= s.m.pg.ResetPromptToDefault(a.ID, tmpl); err!= nil {
		log.Printf("[prompts] mainagent Prompt refreshed to new default failure.: %v", err)
		return
	}
	log.Printf("[prompts] mainagent Prompt already appended new default version(After achieving the target, ask to create a new one.,One-time)")
}

// reseedPlannerPrompt Brush planner prompts into ['Current code default'].——The main body has been simplified and refactored,And downgrade 'restraint' to
// Only deduplicate, add 'depth prioritizes coverage' and 'hard bottom line':If the target has not been achieved and there is no ongoing intent, produce a negative conclusion with boundaries.
// Each time there's a substantive change, bump the flag below.(Current v2)Let existing old databases refresh again. SeedPromptIfEmpty first inserts only,Old library versions that can't receive,Use version management
// [Append a new version and switch over(ResetPromptToDefault),old versions still remain in history,If the user has customized it can be retrieved from version records.
// To recover. settings flag guard → Only once. No need to handle new libraries(SeedPromptIfEmpty Already seeded with the latest default). And
// reseedGoalsPrompt Fully isomorphic.
func (s *Server) reseedPlannerPrompt() {
	const flag = "planner_prompt_compact_realistic_v2"
	if v, _, _:= s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // Try only once, regardless of success or failure
	a, err:= s.m.pg.GetAgentByKey("planner")
	if err!= nil || a == nil {
		return // A new library has not yet built an agent line.,seedPrompts Will directly seed the latest default,No need for this migration
	}
	tmpl:= agent.BuiltinPromptSeeds()["planner"]
	if tmpl == "" {
		return
	}
	// The new library seedPrompts has been seeded with the latest default. → The current version is already equal to the code default,No need to append duplicate versions.
	if cur, err:= s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err:= s.m.pg.ResetPromptToDefault(a.ID, tmpl); err!= nil {
		log.Printf("[prompts] planner Prompt refreshed to new default failure.: %v", err)
		return
	}
	log.Printf("[prompts] planner Prompt already appended new default version(Simplified refactoring+Suppress downgrade and deduplication+depth-first+Deny recheck upper limit,One-time)")
}

// reseedWorkerPrompt Set worker prompt to 'Current code default'——Default body record_fact segment removed 'negative conclusions'
// Write observation+'Probing read' - full sentence, and set confidence.(observed/inferred)Decoupled from 'whether all means of this intent have been exhausted'(These can mislead planners),
// For users with deployed ScopeSentry instances. Connect to the platform through Cursor (or other MCP clients) without local source code.
// SeedPromptIfEmpty First insert only,Old library versions that can't receive,So use version management [Add a new version] and switch to it,Old versions are still retained in history and can be recovered.
// settings flag Translated → Only once. No need to handle new libraries. Completely analogous to reseedGoalsPrompt.
func (s *Server) reseedWorkerPrompt() {
	const flag = "worker_prompt_compact_v4"
	if v, _, _:= s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // Try only once, regardless of success or failure
	a, err:= s.m.pg.GetAgentByKey("worker")
	if err!= nil || a == nil {
		return // A new library has not yet built an agent line.,seedPrompts Will directly seed the latest default,No need for this migration
	}
	tmpl:= agent.BuiltinPromptSeeds()["worker"]
	if tmpl == "" {
		return
	}
	// The new library seedPrompts has been seeded with the latest default. → The current version is already equal to the code default,No need to append duplicate versions.
	if cur, err:= s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err:= s.m.pg.ResetPromptToDefault(a.ID, tmpl); err!= nil {
		log.Printf("[prompts] worker Prompt refreshed to new default failure.: %v", err)
		return
	}
	log.Printf("[prompts] worker Prompt already appended new default version(Query context segments converge to list_assets/list_findings,Remove list_facts/node_detail/asset_neighbors,One-time)")
}

// reporterToolCallMessage Must unconditionally require reading get_finding_traffic once before writing the report.
// This tool is read-only, 'does not depend on the capture toggle',Automatically bind evidence that can be read whether manually bound or not. If here
// Write as 'Read only if auto-binding enabled',Under default configuration, reporter will not pass evidence_version,
// SetFindingReportVersionByNodeID Write -1 according to legacy semantics.,Vulnerability details and Markdown export
// From now on, always show 'evidence has changed, report needs updating'.,And there is no UI entry to clear it.
const reporterToolCallMessage = "A vulnerability has just been reported. Please read the finding_id (independent vulnerability record ID) and finding_node_id (exploration node ID) from the returned JSON." +
	"First use get_finding_traffic(finding_id) Read current evidence list and its version (an empty list is normal, write report as usual); " +
	"If auto-binding is enabled during runtime guidance, verify and associate the current vulnerability's traffic before reading. Use finding_node_id for node details." +
	"Finally call update_finding_report(finding_id=finding_node_id, report, evidence_version=Actual read version) Save," +
	"evidence_version Must be passed, otherwise the report will be permanently marked as pending an update. Do not mix two types of numbering."

// Old version trigger message(0.3.8 And earlier). Only records that are still word-for-word identical will be migrated and overwritten. User changes are kept unchanged.
const reporterToolCallMessageV1 = "A vulnerability has just been reported. Please extract the finding_id from the triggering context." +
	" (Tool Response, \"finding recorded: <id>\" the numbers) with the task id, write a detailed report on the vulnerability according to your duties," +
	"Finally call update_finding_report(finding_id, report) Save."

// upgradeReporterTriggerMessage Change reporter messages in old library that are still default text to the new version.
// seedReporterAgent Subject to reporter_agent_seed_v1 guard and only writes triggers when creating new agents, so
// Upgraded libraries cannot get new copy text —— Tool schema is completed by seedFindingTrafficTools.
// evidence_version, But nothing tells the reporter to use it. Once, and only for unchanged text.
func (s *Server) upgradeReporterTriggerMessage() {
	const flag = "reporter_trigger_evidence_version_v1"
	if v, _, _:= s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // Try only once
	triggers, err:= s.m.pg.ListTriggersFor("reporter")
	if err!= nil {
		log.Printf("[reporter] Failed to read trigger.: %v", err)
		return
	}
	for _, t:= range triggers {
		if!t.OnToolCall || t.ToolCallMessage!= reporterToolCallMessageV1 {
			continue // User modified or is not a finding trigger, leave unchanged.
		}
		t.ToolCallMessage = reporterToolCallMessage
		if err:= s.m.pg.UpdateTrigger(t); err!= nil {
			log.Printf("[reporter] Upgrade trigger message failed: %v", err)
			return
		}
		log.Printf("[reporter] The trigger message has been upgraded to read and return evidence_version")
	}
}

// seedReporterAgent Pre-configuring a custom agent for 'report writing'.(builtin=false, Can be edited/deleted in the UI):
// Bind update_finding_report + Task query tool, and attach a 'report_finding called triggers' hook.
// Trigger —— Wake up to write detailed reports for each registered vulnerability. Once-only.(settings flag Translated): do not rebuild after user deletes it.
// Dependency: orchestration tool already seeded into this function via SeedTool, hence binding is possible.
func (s *Server) seedReporterAgent() {
	const flag = "reporter_agent_seed_v1"
	if v, _, _:= s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // Try only once, regardless of success or failure

	if exist, _:= s.m.pg.GetAgentByKey("reporter"); exist!= nil {
		return // key Already occupied(User manually created)——Do not overwrite
	}
	a, err:= s.m.pg.CreateAgent("reporter", "Report writing",
		"Write detailed vulnerability report: automatically triggered when a vulnerability is found, fetch evidence and execution process to write Markdown report and rewrite.")
	if err!= nil {
		log.Printf("[reporter] Failed to create agent: %v", err)
		return
	}
	if err:= s.m.pg.SeedPromptIfEmpty(a.ID, agent.ReporterDefaultPrompt); err!= nil {
		log.Printf("[reporter] seed prompt Success: %v", err)
	}
	// Trigger run strategy: parallel + none —— One vulnerability one report, multiple findings write their own.
	// merge Must be none: otherwise.(Default all)A batch of findings will be merged into one run; parallel execution is meaningless.
	// maxParallel=5: At most 5 report sessions simultaneously to avoid too many LLM calls at once.
	if err:= s.m.pg.SetAgentTriggerBehavior("reporter", "parallel", "none", 5); err!= nil {
		log.Printf("[reporter] Failed to set trigger run strategy.: %v", err)
	}
	// Bind it with the tools it needs: write report. + Read evidence/execution process/trend.
	if err:= s.m.pg.AddAgentToToolBinding("reporter", []string{
		"update_finding_report", "get_task_node_detail", "list_task_findings",
		"get_task_worker_trace", "list_task_worker_traces", "search_task_worker_traces",
		"get_task_graph",
	}); err!= nil {
		log.Printf("[reporter] Binding tool failed: %v", err)
	}
	// Trigger: report_finding is called, triggering (tool returns "finding recorded: <id>" Include finding_id,
	// Task ID also in trigger message).
	if _, err:= s.m.pg.CreateTrigger(&db.AgentTrigger{
		AgentKey: "reporter",
		Enabled: true,
		OnToolCall: true,
		ToolNames: []string{"report_finding"},
		ToolCallMessage: reporterToolCallMessage,
	}); err!= nil {
		log.Printf("[reporter] Trigger creation failed: %v", err)
	}
	log.Printf("[reporter] A pre-configured 'report writing' agent has been set up. + finding Trigger")
}

// seedAutoReportFindingBinding adds "auto" to report_finding's binding ONCE so
// conversation-context agents can call it without requiring an intent_id.
func (s *Server) seedAutoReportFindingBinding() {
	const flag = "auto_report_finding_v1"
	if v, _, _:= s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err:= s.m.pg.AddAgentToToolBinding("auto", []string{"report_finding"}); err!= nil {
		log.Printf("[auto] report_finding Default binding failed: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedPlannerDefaultBindings adds "planner" to report_finding's binding ONCE
// (guarded by a settings flag), so existing DBs — whose report_finding row was
// seeded as worker-only — also let the planner record findings. Fresh DBs already
// get it via PlannerTools(); this only backfills without overriding a user unbind.
func (s *Server) seedPlannerDefaultBindings() {
	const flag = "planner_report_finding_v1"
	if v, _, _:= s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err:= s.m.pg.AddAgentToToolBinding("planner", []string{"report_finding"}); err!= nil {
		log.Printf("[planner] report_finding Default binding failed: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedPlannerListAssetsBinding adds "planner" to list_assets's binding ONCE
// (guarded by a settings flag), so existing DBs — whose list_assets row was seeded
// as auto/pentest-only — also let the planner query the asset store by DSL. Fresh
// DBs already get it via PlannerTools(); this only backfills without overriding a
// user unbind.
func (s *Server) seedPlannerListAssetsBinding() {
	const flag = "planner_list_assets_v1"
	if v, _, _:= s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err:= s.m.pg.AddAgentToToolBinding("planner", []string{"list_assets"}); err!= nil {
		log.Printf("[planner] list_assets Default binding failed: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedCompanyScopeRebind changes add_company_scope's default binding ONCE on
// existing DBs (guarded by a settings flag): the tool moves off worker and onto
// planner — defining a company's asset scope is a planning/main/auto concern, not
// something a worker does mid-exploration. Fresh DBs already get planner via
// PlannerTools() and lack worker via WorkerTools(); this only backfills old rows.
// One-shot + flag-guarded so a user who later re-binds worker isn't overridden.
func (s *Server) seedCompanyScopeRebind() {
	const flag = "company_scope_rebind_v1" // worker→planner Default binding switch
	if v, _, _:= s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err:= s.m.pg.AddAgentToToolBinding("planner", []string{"add_company_scope"}); err!= nil {
		log.Printf("[planner] add_company_scope Default binding failed: %v", err)
		return
	}
	if err:= s.m.pg.RemoveAgentFromTool("worker", "add_company_scope"); err!= nil {
		log.Printf("[worker] add_company_scope Unbind failed: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedWorkerReadToolsUnbind strips the read-context tools off worker's default
// binding ONCE on existing DBs (guarded by a settings flag): a worker executes one
// intent and writes back — reading facts/companies and listing all workers' traces is
// a planning/main concern, not the executor's. Fresh DBs already lack these via
// WorkerTools(); this only backfills old rows without overriding a user who
// deliberately re-binds worker. Each RemoveAgentFromTool is per-tool +
// membership-guarded, so planner/mainagent bindings of the same tool are untouched.
//
// NOTE: search_all_worker_traces / get_worker_trace / node_detail are intentionally NOT
// unbound — worker owns them for cross-work look-back + node drill-down (see WorkerTools).
// They used to be in this list back when worker lacked them; seedWorkerReadbackRebind
// repairs DBs whose old run stripped them.
func (s *Server) seedWorkerReadToolsUnbind() {
	const flag = "worker_readtools_unbind_v1"
	if v, _, _:= s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	for _, k:= range []string{
		"list_facts", "list_companies", "list_worker_traces",
	} {
		if err:= s.m.pg.RemoveAgentFromTool("worker", k); err!= nil {
			log.Printf("[worker] %s Unbind from worker failed: %v", k, err)
			return // Do not set flag if there is an error, retry on next startup
		}
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedWorkerReadbackRebind re-binds the cross-work look-back / drill-down tools onto
// worker ONCE (guarded by a settings flag): an earlier seedWorkerReadToolsUnbind wrongly
// stripped search_all_worker_traces / get_worker_trace / node_detail from worker after
// they had been added to WorkerTools(), so any DB that ran that migration lost them.
// Fresh DBs already have them via WorkerTools() and this is a harmless no-op there.
// One-shot + flag-guarded so a user who later deliberately unbinds them isn't overridden.
func (s *Server) seedWorkerReadbackRebind() {
	const flag = "worker_readback_rebind_v2" // v2: Append node_detail
	if v, _, _:= s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err:= s.m.pg.AddAgentToToolBinding("worker", []string{
		"search_all_worker_traces", "get_worker_trace", "node_detail",
	}); err!= nil {
		log.Printf("[worker] Playback/Details tool binding failed: %v", err)
		return // Do not set flag if there is an error, retry on next startup
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedAutoDefaultBindings adds "auto" to the task-op + platform tools' bindings
// ONCE (guarded by a settings flag), so existing DBs whose tool rows were seeded
// before Auto existed still give Auto its default toolset — without re-adding it
// after a user deliberately unbinds.
func (s *Server) seedAutoDefaultBindings() {
	const flag = "auto_default_bindings_v3" // v3: Replace old asset tool names with insert_assets/add_company_scope
	if v, _, _:= s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	keys:= make([]string, 0, len(platformToolKeys)+12)
	for _, t:= range s.orchestrationTools() {
		keys = append(keys, t.Name())
	}
	keys = append(keys, platformToolKeys...)
	// Asset tools: Auto operation platform often needs to view/register assets and manage company scope.
	keys = append(keys, "insert_assets", "add_company_scope", "list_assets")
	if err:= s.m.pg.AddAgentToToolBinding("auto", keys); err!= nil {
		log.Printf("[auto] Default binding failed: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}
