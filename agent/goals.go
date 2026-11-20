package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Zeerg/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	acperm "github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// goalsDefaultTmpl is the built-in EDITABLE body (Segment [A]) of the goals-decomposer
// prompt, seeded into agent_prompts. No template vars are used today.
const goalsDefaultTmpl = `You are a penetration testing target decomposer. Your responsibility is to identify**Final result to be achieved**, Rather than planning attack steps.

**The first step (before splitting the target): extract operation constraints**
Identify operator's specific regulations on what can and cannot be done from 'Task Target / Task Description', call set_constraints to log each one (if the description or goal does not involve operation constraints, do not extract operation constraints):
- type=deny: Prohibited operations (such as 'do not scan ports', 'no write/delete operations on production environment', 'prohibit brute force', 'do not touch a subdomain').
- type=allow: Explicitly allow/limit the scope of operations (e.g., 'only passive reconnaissance', 'targeting a specific domain').
- Lateral Movement ≠ Target, also ≠ Attack steps: It defines the boundaries of operational behavior.
- **Constraints must be [self-contained, hardcoded specific targets]**: For targets like 'current target/current port/current IP/current domain/this site', etc.,**Target references**Replace with the task's target/description**Specific Value**. Constraints will be injected separately into the execution phase prompt; after detaching from context, pronouns can't determine who they refer to.
 Example: target is https://abc.example.net → Write 'Only allow testing abc'.example.net'Not 'only allow testing the current target'; 'only test port 443 of the target, no scanning of other ports' rather than 'only test the current port'. If the original text says 'current target' but the target address is clear, fill in the address.
- **Only log constraints explicitly written or emphasized in target/description, no speculation allowed**; Use 'deny' (more conservative) when unsure of type.
- If there are no operation constraints in the target/description, then**don't**Call set_constraints.
After registering constraints (if any), proceed to split the targets.

**Target = Final deliverable/verifiable result**

**Not content related to the target (prohibited as a sub-target).**:
- Information collection, reconnaissance, endpoint scanning
- Vulnerability analysis and validation process
- Attack steps, exploitation methods
- Result verification steps

**Split principle**:
- The final goal described by the user is only one → Output one
- Multiple exist**independent of each other**Final deliverables → List separately
- Able to annotate with clear vulnerability classes vulnclass; information collection/business logic targets left blank
- Strictly prohibit fabricating user-unmentioned targets

Call set_goals to submit results.`

// goalsScopeTail is the code-owned tail appended after the editable goals body
// WHEN an asset store + task context are available. It teaches the decomposer to
// also lift the explicit asset scope out of the goal/description and register it
// via add_task_scope. Kept in code (not the DB-editable body) so it always applies
// on released DBs and can't be edited away — same pattern as the trafficTool tail.
const goalsScopeTail = `

**Additional responsibility: register the test asset scope.**
Apart from splitting the target, you also need to identify 'task targets / task description' for**Clearly defined test asset range.**, Call add_task_scope to register (this task's authorization boundary, also the denominator for asset test coverage).**Minimum scope principle: Only log the target that the user explicitly points to, never expand arbitrarily.**
- Target is a URL or address with a hostname (e.g., https://xxx.example.com/path, app.example.com)→ take its**Full hostname**, kind=subdomain, value=Full hostname.
 Example: Target https://a1b2c3.lab.example.net/path → kind=subdomain, value=a1b2c3.lab.example.net (**Is not** example.net).
 **Strictly Prohibited**Shortening hostnames with subdomains to root domains.——See xxx.example.com Just register the entire example.com expands the scope beyond user targets, violating the principle of minimum range.
- Only when the user provides it.**Top-level domain, without any subdomains** (As if directly writing example.com), Or Explicitly Say“Entire site / all subdomains / full domain names” → Only use kind if applicable=root_domain, value=example.com.
- Pure IP or subnet → kind=ip / cidr, value=IP Or CIDR.
- **don't**Register company scope (company).——The task is just established, and the asset system usually doesn't have this company yet. It can't be registered, so it's handled by the subsequent plan phase at a corporate level.
Other rules:
- Just register**Clearly state in the target/description.**The scope;Do not fabricate or infer unmentioned domains/IPs.
- reason Briefly state where the summary comes from, for auditing.
- If there is no explicit asset range in the target/description,**don't**Call add_task_scope.
First register scope with add_task_scope (if any), then call set_goals to submit goals.`

// GoalSpec is one decomposed objective.
type GoalSpec struct {
	Text string `json:"text"`
	VulnClass string `json:"vulnclass,omitempty"`
}

// DecomposeGoals asks the LLM to break a pentest task goal into discrete,
// independently-verifiable objectives (each becomes a goal node). Returns nil if
// no provider is configured or the call yields nothing — the caller then falls
// back to a rule-based split so goal nodes always exist.
//
// prov is supplied by the caller (rather than built here from a Config) so goal
// decomposition rides the SAME provider instance as the rest of the engine — it
// shares the rate limiter, gets recorded by llmrec, and participates in LLM
// failover instead of quietly bypassing all three.
//
// desc is the task's free-text description (Background: target range/flag count/battle description, etc.).
// It is fed alongside the goal so the decomposer no longer splits blind — the
// prompt still forbids inventing anything the two texts don't state.
//
// emit, when non-nil, receives every LLM step (thinking/tool_use/result) with
// Worker="planner" so the round-0 goal-decomposition activity is visible in the UI.
//
// as + taskID, when non-nil/positive, wire the add_task_scope tool so the
// decomposer can register the explicit asset scope it extracts from the goal.
//
// ts is the task's exploration store: set_goals writes the decomposed goal nodes
// straight into it (the same managed tool the main agent uses to add goals at
// runtime). The returned specs are read back from the store so callers can emit
// per-goal activity and detect the "LLM produced nothing" case for their fallback.
func DecomposeGoals(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	return DecomposeGoalsWithProvider(ctx, prov, dataDir, goalText, desc, as, ts, taskID, false, 0, emit)
}

// DecomposeGoalsWithProvider is the task-runtime variant used when a task has an
// ordered provider chain. It preserves the same tools and write behavior while
// letting the caller own provider selection/failover. maxTokens is the profile's
// per-reply output cap (0 = send none).
func DecomposeGoalsWithProvider(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, nonStreaming bool, maxTokens int, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	// Target decomposition is a one-time call: no transcript store, so agentcore does not hang on ctx.
	// session id (It only attaches when there is a writer, see agentcore.Prompt). By session-id header
	// Gateway for prompt caching/sticky routing (opencode zen missing x-opencode-session directly returns 400
	// MissingSessionID)Reads the value from ctx.——Notbackfill is 'dialogue normal, split 400'.
	// Explicitly attach a stable ID: The same exploration's decomposition requests share it (beneficial for cache hits), and naming with
	// planner/worker Non-conflicting, can be handled by llmrec.parseSession Correct Attribution.
	if ts!= nil {
		ctx = transcript.WithSessionID(ctx, fmt.Sprintf("exp%d-goals", ts.ID()))
	}
	// worker="goals" tags the goal nodes' provenance; ts/taskID let set_goals link
	// each goal under the task root. This is the catalog's real set_goals tool, so a
	// web-edited description/schema on it applies here too.
	tsx:= &ToolSet{as: as, ts: ts, taskID: taskID, worker: "goals"}
	// Description rides in the user message (same channel as the goal), NOT via the
	// {{.EngagementDescription}} template var — else a prompt that references the var
	// would inject the description twice. System prompt stays pure static instructions.
	sys:= renderSystem("goals", goalsDefaultTmpl, GoalsVars{DataDir: dataDir, Now: nowStr()})
	// set_constraints Always available(Not dependent on asset store):The main body already includes the step 'Extract operation constraints before breaking down targets'
	// (This can be modified in the agent editing page.),Just connect the tool here.
	tools:= []actool.CoreTool{tsx.setGoals(), tsx.setConstraints()}
	// Wire add_task_scope only when we have a real asset store + task to write to.
	// The scope-extraction tail is appended in lockstep so the prompt never asks for
	// a tool that isn't present.
	if as!= nil && taskID > 0 {
		tools = append(tools, tsx.addTaskScope())
		sys += goalsScopeTail
	}
	userMsg:= "Task objective:\n" + goalText
	if d:= strings.TrimSpace(desc); d!= "" {
		userMsg += "\n\nTask description (background information, may include target scope/flag count/battle instructions; for reference only, do not fabricate unmentioned content):\n" + d
	}
	// Use captureRun so every LLM step is emitted as an activity record (visible in
	// the plan tab under the round-0 marker). Falls back gracefully when emit is nil.
	captureEmit:= func(r db.Activity) {
		if emit!= nil {
			r.Worker = "planner"
			emit(r)
		}
	}
	captureRun(ctx, agentcore.Options{
		Provider: prov,
		SystemPrompt: []string{sys},
		Tools: tools,
		PermissionMode: acperm.ModeBypass,
		DisableBackgroundTasks: true,
		// 3 step(Draw constraint → Registered range → Decomposing targets)Each requires one tool call,Give enough turns to avoid missing set_goals at the end.
		MaxTurns: 8,
		NonStreaming: nonStreaming, // This profile walks the Provider when not using streaming..Complete
		MaxTokens: maxTokens, // 0 = No upper limit,Determined by the server's default value
	}, userMsg, captureEmit)
	// set_goals persisted the goals directly; read them back so the caller sees what
	// was written (empty slice ⇒ the LLM produced nothing ⇒ caller falls back).
	if ts == nil {
		return nil
	}
	nodes, _:= ts.ListByKind(db.KindGoal, 10000)
	var out []GoalSpec
	for _, n:= range nodes {
		var p struct {
			Text string `json:"text"`
			VulnClass string `json:"vulnclass"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		if strings.TrimSpace(p.Text)!= "" {
			out = append(out, GoalSpec{Text: p.Text, VulnClass: p.VulnClass})
		}
	}
	return out
}
