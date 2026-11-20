package agent

import (
	"encoding/json"
	"fmt"

	"github.com/Zeerg/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

const findingIDGuidance = "\n\n**Vulnerability Numbering Convention**: finding_id 'finding_id' is the independent vulnerability record ID; 'finding_node_id' is the exploration node ID. The 'id' in 'list_findings', 'list_task_findings', 'node_detail', and 'get_task_node_detail' should retain the exploration node ID, which should be read from the same returned 'finding_id' to get the independent number. Use 'independent finding_id' for 'get_finding_traffic' and 'bind_finding_traffic'. The old 'update_finding_report' parameter 'finding_id' still passes 'finding_node_id'. Do not use the number in the first line of 'report_finding' for evidence tools, nor guess other numbers after encountering a numbering error."

// The server supplies the persisted setting. A missing setting/host is off.
// Consulted at assembly and again on writes so an already-running session
// cannot keep binding after the user switches the feature off.
var FindingTrafficBindingEnabled func() bool

func findingTrafficBindingEnabled() bool {
	return FindingTrafficBindingEnabled!= nil && FindingTrafficBindingEnabled()
}

// Applied after ToolResolve: user descriptions and prompts remain intact, while
// all actual reporters (including Planner and custom chat agents) see the same
// API contract. Disabled/unbound tools are never reintroduced here.
func findingWorkflowTools(agentKey string, tools []actool.CoreTool) ([]actool.CoreTool, string) {
	if!findingTrafficBindingEnabled() {
		out:= make([]actool.CoreTool, 0, len(tools))
		for _, tool:= range tools {
			if tool.Name() == "bind_finding_traffic" {
				continue
			}
			if agentKey == "reporter" && (tool.Name() == "traffic_search" || tool.Name() == "traffic_get" || tool.Name() == "traffic_blob") {
				continue
			}
			switch tool.Name() {
			case "report_finding", "add_hint", "add_task_hint":
				// Work on a copy: toggling back on must restore the original schema.
				raw, _:= json.Marshal(tool.InputSchema())
				var schema map[string]any
				if json.Unmarshal(raw, &schema) == nil {
					stripTrafficParameters(schema)
					tool = DecorateTool(tool, tool.Description(), schema)
				}
			}
			out = append(out, tool)
		}
		return out, ""
	}
	out:= append([]actool.CoreTool(nil), tools...)
	has:= map[string]bool{}
	for i, tool:= range out {
		has[tool.Name()] = true
		note:= ""
		switch tool.Name() {
		case "report_finding":
			note = "\nBy default, the report Agent verifies and binds traffic before writing a report. The reporter retains validation commands, key outputs, existing real traffic IDs, and their purposes in the evidence for the report Agent to verify against execution records; no additional packet capture is needed for binding. Compatibility with explicit immediate binding: 'traffic_refs' or 'evidence_hint_id' can submit verified references, with the latter reading structured references from the specified hint of this task. Any invalid reference causes the entire submission to fail. TCP/without packets do not require these optional parameters. Return 'finding_id' and 'finding_node_id' respectively representing independent records and exploration nodes."
		case "add_hint", "add_task_hint":
			note = "\nWhen transferring confirmed vulnerabilities, retain verified traffic IDs, usage, description, and order in the corresponding prompt's traffic_refs (single items at the top level, batches in corresponding hint elements), and explain in text what specific vulnerability it proves. The caller cannot transfer only text while discarding existing traffic references. Unverified candidates cannot be passed as evidence."
		case "get_finding_traffic", "bind_finding_traffic", "list_findings", "list_task_findings", "node_detail", "get_task_node_detail", "update_finding_report":
			note = findingIDGuidance
		}
		if note!= "" {
			out[i] = DecorateTool(tool, tool.Description()+note, tool.InputSchema())
		}
	}
	guidance:= ""
	if has["report_finding"] || has["add_task_hint"] || has["add_hint"] {
		guidance = "\n\n**Optional traffic evidence handover**: Automatic binding defaults to completion by the report Agent after vulnerability entry and before writing the report. The reporter should retain validation commands, key outputs, existing real traffic ID and its purpose; tasks should include intent_id for the report Agent to trace; there is no need to look up additional packets for binding. When Auto/Planner reports, do not discard references already made by the executor. add_hint / add_task_hint can use traffic_refs to hand over; explicit immediate binding still compatible with report_finding's traffic_refs / evidence_hint_id. TCP or no packet when registering normally, cannot guess ID, nor repeat probing solely for packet supplementation."
		if has["add_task_hint"] &&!has["add_hint"] {
			guidance += "\nWhen there is no task context in platform dialogue, do not directly call report_finding; instead, pass it to the corresponding task through add_task_hint, where the task Agent registers it and uses list_task_findings to verify the results."
		}
		if has["prove_goal"] || has["goal_met"] {
			guidance += "\nBefore determining if the target is complete, first report/transfer any existing evidence. Do not end the task or cancel the Worker just because a text vulnerability has been registered and evidence transfer has not yet been completed. Without a package, do not require waiting or forceful packet capture."
		}
	}
	if has["update_finding_report"] && has["bind_finding_traffic"] && has["get_finding_traffic"] {
		guidance += "\n\n**Automatically associate traffic with report (already enabled)**: You are responsible for verifying and binding traffic for the triggered vulnerability and writing a report. First, obtain the explicit finding_id and finding_node_id from report_finding returning JSON or get_task_node_detail / list_task_findings. Read the vulnerability details, execution records corresponding to the intent, and existing evidence list, prioritizing the true ID handed over by the reporter. If this verification is for HTTP and traffic tools are available, use traffic_search to filter candidates, then use traffic_get to verify each request/response indeed supports the vulnerability; domain names and times are only used for filtering, not proving ownership. Confirm the evidence in the order of reproduction using bind_finding_traffic(finding_id, traffic_refs) Associate with baseline / proof / verification / supporting and explain their purpose. Only operate on this vulnerability, no repeated creation of vulnerabilities or re-probing of targets. After successful binding, re-call get_finding_traffic to get the latest version, read the required text, then pass the actually read version as evidence_version to update_finding_report (its finding_id parameter still uses finding_node_id). Existing bindings do not need to be repeatedly appended. For TCP, uncollected cases, unavailable tools, or no exact matches, skip automatic binding and write reports normally based on textual/command evidence, without guessing for traffic. Binding failures should not claim success; retain existing evidence and explain the reason for no binding in the report."
	}
	if guidance!= "" || has["get_finding_traffic"] || has["update_finding_report"] {
		guidance += findingIDGuidance
	}
	return out, guidance
}

func stripTrafficParameters(schema map[string]any) {
	props, _:= schema["properties"].(map[string]any)
	delete(props, "traffic_refs")
	delete(props, "evidence_hint_id")
	if required, ok:= schema["required"].([]any); ok {
		kept:= required[:0]
		for _, key:= range required {
			if key!= "traffic_refs" && key!= "evidence_hint_id" {
				kept = append(kept, key)
			}
		}
		schema["required"] = kept
	}
	if hints, ok:= props["hints"].(map[string]any); ok {
		if items, ok:= hints["items"].(map[string]any); ok {
			stripTrafficParameters(items)
		}
	}
}

// HintTrafficSchema is shared by the task-local and cross-task hint tools.
func HintTrafficSchema() map[string]any {
	return map[string]any{"type": "array", "description": "Optional: Retain the order of verified and corresponding traffic references for specific vulnerabilities in this prompt; after handover, report_finding can pass evidence_hint_id with these references.", "items": obj(map[string]any{"traffic_id": str("Actual traffic ID."), "role": str("baseline / proof / verification / supporting"), "note": str("What conclusions can be drawn from this traffic?")}, "traffic_id")}
}

func (t *ToolSet) findingRefsFromHint(hintID int64, explicit []db.TrafficRef) ([]db.TrafficRef, error) {
	if hintID <= 0 {
		return db.NormalizeTrafficRefs(explicit)
	}
	n, err:= t.ts.GetNode(hintID) // local store only: inherited hints cannot supply evidence
	if err!= nil {
		return nil, err
	}
	if n == nil || n.Kind!= db.KindHint {
		return nil, fmt.Errorf("evidence_hint_id=%d Must be a prompt node for this task (inherited prompts cannot be directly used to bind)", hintID)
	}
	var payload struct {
		Refs []db.TrafficRef `json:"traffic_refs"`
	}
	if err:= json.Unmarshal(n.Payload, &payload); err!= nil {
		return nil, err
	}
	return db.NormalizeTrafficRefs(append(append([]db.TrafficRef{}, explicit...), payload.Refs...))
}
