package agent

import (
	"strings"

	"github.com/Autumn-27/norma/harness"
)

// End prompt(wrap-up / settlement prompt):When the agent runs out of steps due to(MaxTurns)]Or
// [timeout(run_seconds/MaxDuration)]When terminated,SDK The settlement phase will inject this hint,
// Let the agent first store content it has identified but not written back to the database before outputting a summary sentence,Avoid a bad end.
//
// Each agent's start and end prompts can be overridden in the background as needed.(Store agents.wrapup_prompt),Use this if left empty
// Built-in default. Only [prompt body] can be edited;Disable which tools, budget for a few rounds is fixed code strategy.

// WrapupOverride, if set, returns the stored wrap-up prompt for an agent key and
// whether a non-empty one exists. Wired by the server to the agents table (like
// PromptOverride for system prompts). nil / empty → the built-in default is used.
var WrapupOverride func(agentKey string) (string, bool)

// WrapupMaxTurnsOverride, if set, returns the admin-configured turn budget for the
// wrap-up phase of an agent and whether a positive one exists. Wired to the agents
// table. nil / ≤0 → the built-in per-agent default (wrapupTurnDefaults) is used.
var WrapupMaxTurnsOverride func(agentKey string) (int, bool)

// Built-in default start and end prompts,Indexed by agent key. Worker reuses historically hard-coded settleWrapUpPrompt.
// (Defined in worker.go),planner/mainagent each has its own version;Not Matched(Custom agent.)Fall back to general.
var wrapupDefaults = map[string]string{
	"worker": settleWrapUpPrompt,
	"planner": plannerWrapUpDefault,
	"mainagent": mainAgentWrapUpDefault,
}

// wrapupTurnDefaults: Each agent's budget for the final stage [itself] is built-in default(Can be accessed by the backend >0 Coverage).
// Given 10 rounds,Ensure there are enough steps at the beginning and end for database storage. If not hit, use genericWrapupTurns.
var wrapupTurnDefaults = map[string]int{
	"worker": 10,
	"planner": 10,
	"mainagent": 10,
}

const genericWrapupTurns = 10

const plannerWrapUpDefault = "You are about to run out of steps for this round.——Note that this is just for the current round.,The system will still wake you up again as conditions change to continue planning.,Not task termination,You do not need to wrap up the entire planning here. Please implement the conclusions you have already figured out in this round, don't let this round run in vain.,But do not force intent for completeness.(0 intents in this round are still a completely normal result.): (1) If the current exploration direction to be dispatched is already determined,Use once add_intent to batch submit(Don't hold back what you've thought of);(2) For targets that have already been achieved/factually proven,,Call prove_goal to mark met(Don't miss);(3) If identifying need for multi-step serial exploitation chain,,Use TodoWrite to record,Convenient for next wake-up to continue dispatching. Done directly ends the current round,No summary text output required."

const mainAgentWrapUpDefault = "Your steps are about to run out,This interaction should end. Do not initiate new explorations or operations. Please**A single sentence of plain text**Summarize current progress and key conclusions to the user.,And the suggested next step."

const genericWrapUpDefault = "You are about to be terminated due to budget depletion. Please write back any completed but uncommitted results first.,again**A single sentence of plain text**Summarize what you did and the key conclusions you reached(This sentence will be displayed as the result of this run.). "

// WrapupDefault returns the built-in default wrap-up prompt for an agent key —
// used by the admin UI as the "restore default" value and empty-field placeholder.
func WrapupDefault(agentKey string) string {
	if d, ok:= wrapupDefaults[agentKey]; ok {
		return d
	}
	return genericWrapUpDefault
}

// WrapupTurnsDefault returns the built-in wrap-up turn budget for an agent key —
// used by the admin UI as the "0 = default N" hint.
func WrapupTurnsDefault(agentKey string) int {
	if n, ok:= wrapupTurnDefaults[agentKey]; ok {
		return n
	}
	return genericWrapupTurns
}

// resolveWrapup returns the effective wrap-up prompt: the DB override (if set and
// non-empty) over the built-in default.
func resolveWrapup(agentKey string) string {
	if WrapupOverride!= nil {
		if t, ok:= WrapupOverride(agentKey); ok && strings.TrimSpace(t)!= "" {
			return t
		}
	}
	return WrapupDefault(agentKey)
}

// resolveWrapupTurns returns the effective wrap-up turn budget: a positive DB
// override over the built-in per-agent default.
func resolveWrapupTurns(agentKey string) int {
	if WrapupMaxTurnsOverride!= nil {
		if v, ok:= WrapupMaxTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return WrapupTurnsDefault(agentKey)
}

// wrapupSettlement builds the settlement config for an agent's run. Prompt and the
// turn budget are admin-editable per agent; disabled tools are code-owned policy so
// a user can't edit away the "stop probing" guardrail. Resolved fresh each run
// (reads DB live), so edits apply on the next run without a restart.
func wrapupSettlement(agentKey string, disabledTools []string) *harness.Settlement {
	return &harness.Settlement{
		Prompt: resolveWrapup(agentKey),
		DisabledTools: disabledTools,
		MaxTurns: resolveWrapupTurns(agentKey),
	}
}

// ---------- Task-level timeout end phrases (see docs/Task-Level Timeout and Conclusion Design)..md)----------
//
// The per-run conclusion is ['two sets']: per-run is"Your budget for this run has been exhausted."; Task timeout is
// "The entire task is at the end, about to finish". Semantic opposites (especially planner: per-run says"Don't stop, continue planning",
// Task timeout message"Stop planning at the scheduled time, make the final judgment"). Only configure for worker/planner.

// WrapupTaskTimeoutOverride / ...TurnsOverride: DB coverage for task timeout end phrases and rounds.
// (wire To agents.task_timeout_wrapup_prompt / _max_turns, Only for worker/planner.
var (
	WrapupTaskTimeoutOverride func(agentKey string) (string, bool)
	WrapupTaskTimeoutTurnsOverride func(agentKey string) (int, bool)
)

var taskTimeoutWrapupDefaults = map[string]string{
	"worker": workerTaskTimeoutDefault,
	"planner": plannerTaskTimeoutDefault,
}

const workerTaskTimeoutDefault = "**The entire task has reached the timeout limit and is about to end.** (It's not your budget for this run; it's the total exploration budget. This is your last chance:(1) Write back all the content you have identified but haven't written back yet to the database——New assets insert_assets, exploration conclusions/facts record_fact, confirm vulnerabilities report_finding;(2) Do not start any new commands/probes;(3) **End with a single sentence in plain text.**Summarize your key conclusions in this intent."

const plannerTaskTimeoutDefault = "**The entire task has reached the timeout limit and is about to end.** (Not part of this round, but the entire task has been terminated). Based on the current[all]facts and discoveries, make your final target determination: mark goals already proven by evidence with prove_goal as met (don't miss any).**Do not generate any new intents** (At this point, the intent will not be executed again). Once determined, it will conclude without needing to output a summary text."

// TaskTimeoutWrapupDefault Return a task timeout built-in default opening and closing phrase for an agent (for backend placeholder/recovery to default).
func TaskTimeoutWrapupDefault(agentKey string) string {
	return taskTimeoutWrapupDefaults[agentKey] // When Unconfigured(mainagent/chat)Return empty string
}

// resolveTaskTimeoutWrapup: DB Coverage(non-empty) > Built-in default. An empty string means this agent has no task timeout words
// (Non-worker/planner), at which point the caller should fall back to per-run words.
func resolveTaskTimeoutWrapup(agentKey string) string {
	if WrapupTaskTimeoutOverride!= nil {
		if t, ok:= WrapupTaskTimeoutOverride(agentKey); ok && strings.TrimSpace(t)!= "" {
			return t
		}
	}
	return TaskTimeoutWrapupDefault(agentKey)
}

func resolveTaskTimeoutTurns(agentKey string) int {
	if WrapupTaskTimeoutTurnsOverride!= nil {
		if v, ok:= WrapupTaskTimeoutTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return resolveWrapupTurns(agentKey) // Default uses per-run rounds
}

// wrapupSettlementForTask builds settlement for a worker/planner run that is aware
// of the task deadline. See §5 of the design doc:
// - clamped=true → This run was constrained by the task deadline: ended due to Timeout=Task completed→Task timeout keyword;
// Due to MaxTurns limit=Steps in the squeezing window exhaust before the task has a few minutes left→Fallback per-run word.
// - clamped=false → The task is still early: both reasons use per-run terms (i.e., degrade to wrapupSettlement).
//
// The PromptByReason given to harness selects based on the **actual** reason at the end. Without build, it may misalign.
func wrapupSettlementForTask(agentKey string, disabledTools []string, clamped bool) *harness.Settlement {
	perRun:= resolveWrapup(agentKey)
	st:= &harness.Settlement{
		Prompt: perRun, // Fallback(Also the two reason values when not clamped)
		DisabledTools: disabledTools,
		MaxTurns: resolveWrapupTurns(agentKey),
	}
	if clamped {
		if tt:= resolveTaskTimeoutWrapup(agentKey); tt!= "" {
			st.PromptByReason = map[harness.TerminalReason]string{
				harness.ReasonTimeout: tt, // Task completed
				harness.ReasonMaxTurns: perRun, // Steps exhausted before task time remaining
			}
			st.MaxTurns = resolveTaskTimeoutTurns(agentKey)
		}
	}
	return st
}
