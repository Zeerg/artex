package agent

import (
	"context"
	"encoding/json"

	actool "github.com/Autumn-27/norma/tool"
)

// This file turns 'built-in tools' from plain code into an enumerable, DB-overridable directory:
// - BuiltinToolSeeds(): Expand the built-in toolset of three execution agents into seed records (key +
// Describe + Parameter schema + Default bound agent), for server startup idempotent seeding into tools table.
// - ToolResolve Hooks: At runtime, filter already assembled tools by DB's tools row for 'By agent'. +
// Overwrite description/schema + 'Inject parameter default values.' The key/handler remains at the code layer, while the DB only changes the 'essays and default values.'
// handler (Call Behavior) always comes from code——DB Cannot change it; can only modify the model's visible description and default parameters.

// ToolSeed It's a built-in tool's seed snapshot: key is CoreTool.Name() (Bound to the handler,
// UI Read-only), Desc/Schema from tool definition in code, Agents are those given by default to agents in the code.
type ToolSeed struct {
	Key string // = CoreTool.Name(), Primary Key,immutable
	Desc string // Top-level description (can be overridden in UI).
	Schema map[string]any // Parameter JSON-Schema (structure is read-only, description/default can be modified in UI)
	Agents []string // Default bound agent key (worker/planner/mainagent)
}

// builtinToolsByAgent Constructs each execution agent using a 'read-only shell' ToolSet (nil stores).
// Domain toolset. Tool constructor only passes closures into Spec; no dereferencing store during construction, so nil-safe.——
// These tools are only used to read Name here()/Description()/InputSchema(), Absolutely not to Call.
//
// Intentionally [not] SDK generic tools actool.DefaultTools() (Read/Write/Edit/MultiEdit/LS/Glob/
// Grep/Bash): Each agent has fixed ownership; there is no choice between 'bound to who' and 'not bound'; most explanations are in the Prompt()
// (This table only covers Description(), May cause partial coverage misdirection). Do not seed → No DB row → ToolResolve
// Allow passing through as-is, without overwriting. Behavior remains consistent with before. Only artex's own domain tools can be managed.
func builtinToolsByAgent() map[string][]actool.CoreTool {
	ts:= NewToolSet(nil, "")
	return map[string][]actool.CoreTool{
		"mainagent": ts.MainAgentTools(),
		"planner": ts.PlannerTools(),
		"worker": ts.WorkerTools(),
		// goals (Target resolver) defaults to binding set_goals + set_constraints: Rely on them to extract targets,
		// Extracted operation constraints written to the database. Shares managed tools with mainagent, web end can modify description/schema, select by agent.
		"goals": {ts.setGoals(), ts.setConstraints()},
		// auto Default binds vulnerability reporting + Asset management tool, other domain tools can be selected on the UI as needed.
		// new library written by this seed; old libraries are migrated by seedAutoDefaultBindings.
		"auto": {ts.addFinding(), ts.insertAssets(), ts.addCompanyScope(), ts.listAssets(), ts.listCompanies()},
		// pentest (Independent penetration agent)default binding: scan assets / insert assets / report vulnerabilities / scan vulnerabilities / scan enterprise.
		// New library writes by seed; Old library migrates using seedPentestDefaultBindings.
		"pentest": {ts.listAssets(), ts.insertAssets(), ts.addFinding(), ts.listFindings(), ts.listCompanies()},
	}
}

// defaultUnbound: These system tools will normally enter the directory (visible on the web end, manually selectable by agent), but
// Default [not bind any agent],——ToolResolve Tools with empty bindings discard all agents,Must explicitly opt-in.
// That's why it still stays in some agent's base toolset.(If goal_met in PlannerTools):Firstly, let seed be able to
// Construct it to get desc/schema,Secondly, when manually bound back and run, it stays in base if ToolResolve allows it.
//
// goal_met: Bypass each prove_goal individually, directly declaring from a global perspective that 'the entire task is completed.',High-risk and with misjudgment risk,And with
// 'prove_goal Mark the last target → Automatic Conclusion Repeated,Default is no agent,Bind manually when needed.
var defaultUnbound = map[string]bool{"goal_met": true}

// BuiltinToolSeeds Deduplicates and merges each agent's built-in toolset into a seed list: same-named tools (like list_assets
// Multiple agents combine into one; defaultUnbound tools are forced to bind as empty.
func BuiltinToolSeeds() []ToolSeed {
	byAgent:= builtinToolsByAgent()
	order:= []string{"mainagent", "goals", "planner", "worker", "auto", "pentest"}

	type acc struct {
		tool actool.CoreTool
		agents []string
	}
	m:= map[string]*acc{}
	var keys []string
	for _, ak:= range order {
		for _, t:= range byAgent[ak] {
			a, ok:= m[t.Name()]
			if!ok {
				a = &acc{tool: t}
				m[t.Name()] = a
				keys = append(keys, t.Name())
			}
			a.agents = append(a.agents, ak)
		}
	}

	out:= make([]ToolSeed, 0, len(keys))
	for _, k:= range keys {
		a:= m[k]
		agents:= a.agents
		if defaultUnbound[k] {
			agents = []string{} // Enter directory, can be manually bound, but defaults don't give any agent (store [] It is not null, consistent with other tools)
		}
		out = append(out, ToolSeed{
			Key: k,
			Desc: a.tool.Description(),
			Schema: a.tool.InputSchema(),
			Agents: agents,
		})
	}
	return out
}

// ToolResolve, if set, post-processes an agent's fully-assembled tool list against
// the DB tools table: it drops tools not bound to this agent (or globally disabled)
// and wraps the rest so the model sees the DB-overridden description/schema and
// Default input parameter injected.. Tools with no matching DB row (MCP/skill/host tools like
// traffic) pass through untouched. nil = tools unchanged. Wired in server/assembly.go.
var ToolResolve func(ctx context.Context, agentKey string, tools []actool.CoreTool) []actool.CoreTool

// DecorateTool wraps t so Description()/InputSchema() report the DB overrides and
// Call() injects scalar parameter defaults (from schema's "default" props) whenever
// the model omitted them. Name/Prompt/permission/scheduler flags delegate to t, so
// the tool's identity and handler are unchanged. Empty desc/schema fall back to t's.
func DecorateTool(t actool.CoreTool, desc string, schema map[string]any) actool.CoreTool {
	if desc == "" {
		desc = t.Description()
	}
	if len(schema) == 0 {
		schema = t.InputSchema()
	}
	return &overriddenTool{CoreTool: t, desc: desc, schema: schema}
}

// overriddenTool is a CoreTool decorator: it embeds the original (so all behavioral
// methods — Prompt/IsReadOnly/IsConcurrencySafe/CheckPermissions/Name — delegate)
// and overrides only the model-facing description/schema plus default injection.
type overriddenTool struct {
	actool.CoreTool
	desc string
	schema map[string]any
}

func (o *overriddenTool) Description() string { return o.desc }
func (o *overriddenTool) InputSchema() map[string]any { return o.schema }

func (o *overriddenTool) Call(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	return o.CoreTool.Call(ctx, injectDefaults(in, o.schema), tc)
}

// injectDefaults fills scalar parameter defaults declared in the (possibly edited)
// schema into the input JSON whenever the model omitted the field or left it empty/
// null. Structure (names/types/required) is untouched — onlyDefault values are merged in..
func injectDefaults(in json.RawMessage, schema map[string]any) json.RawMessage {
	defs:= scalarDefaults(schema)
	if len(defs) == 0 {
		return in
	}
	m:= map[string]json.RawMessage{}
	if len(in) > 0 {
		if err:= json.Unmarshal(in, &m); err!= nil {
			return in // non-object input: don't touch it
		}
	}
	changed:= false
	for k, dv:= range defs {
		if cur, ok:= m[k];!ok || isEmptyJSON(cur) {
			m[k] = dv
			changed = true
		}
	}
	if!changed {
		return in
	}
	b, err:= json.Marshal(m)
	if err!= nil {
		return in
	}
	return b
}

// scalarDefaults extracts properties[k]["default"] for scalar params (string/
// integer/number/boolean). Array/object defaults are skipped: merging them is
// ambiguous and not worth the surprise.
func scalarDefaults(schema map[string]any) map[string]json.RawMessage {
	props, _:= schema["properties"].(map[string]any)
	if len(props) == 0 {
		return nil
	}
	out:= map[string]json.RawMessage{}
	for name, raw:= range props {
		p, ok:= raw.(map[string]any)
		if!ok {
			continue
		}
		dv, ok:= p["default"]
		if!ok || dv == nil {
			continue
		}
		switch p["type"] {
		case "string", "integer", "number", "boolean":
			if b, err:= json.Marshal(dv); err == nil {
				out[name] = b
			}
		}
	}
	return out
}

func isEmptyJSON(raw json.RawMessage) bool {
	s:= string(raw)
	return s == "null" || s == `""`
}
