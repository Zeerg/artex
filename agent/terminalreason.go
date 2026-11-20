package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Autumn-27/norma/harness"
)

// runTrace retains the latest tool call so an interrupted run can identify the
// operation that was still in flight.
type runTrace struct {
	startedAt time.Time
	id string
	name string
	input string
	at time.Time
	pending bool
}

func (t *runTrace) start(id, name, input string) {
	t.id, t.name, t.input, t.at, t.pending = id, name, input, time.Now(), true
}

func (t *runTrace) done(id string) {
	if id == t.id {
		t.pending = false
	}
}

var reasonHint = map[harness.TerminalReason]string{
	harness.ReasonCompleted: "The model normally ended this round, but didn't leave a text summary; facts and assets are based on the tool call records for this round.",
	harness.ReasonMaxTurns: "Reach step limit(MaxTurns): SDK The cleanup and writeback of facts and assets have been completed, with the intent marked as exhausted for the planner to change direction and continue, rather than treating it as a failure.",
	harness.ReasonTimeout: "Reach the wall-clock budget for a single run(MaxDuration): At the right time, interrupt running tools and wrap them up on-site, writing out identified facts and assets. The intent will be marked as exhausted.",
	harness.ReasonModelError: "Model or API call failure (network, authentication, rate limiting, supplier 5xx, etc.), after retries are exhausted, mark the intent as blocked——Transport layer failure means this intent didn't really probe; check its execution process (get_worker_trace) before deciding to reassign or change method.",
	harness.ReasonBlockingLimit: "When the context length reaches the hard limit, the request is intercepted before it's sent; narrow down intent granularity or compress tool returns",
	harness.ReasonPromptTooLong: "Prompt too long and context compression retries exhausted, cannot continue execution",
	harness.ReasonImageError: "The current model does not support multimodal content this round; please switch to a model that supports vision or avoid tools returning images.",
	harness.ReasonStopHookPrevented: "Stop You do not need to conclude the entire planning here. Please implement the conclusions you have already figured out in this round, don't let this round run in vain.",
	harness.ReasonHookStopped: "Tools or hooks actively stop further execution, such as out-of-bound targets or disabled commands; please check the interception instructions of the last tool_result.",
	harness.ReasonAbortedStreaming: "Cancelled during the model output stream generation phase.",
	harness.ReasonAbortedTools: "Cancelled during tool execution phase",
}

// terminalText renders a terminal event with no final text into a compact summary
// and a Markdown detail block.
func terminalText(ctx context.Context, term *harness.Terminal, tr *runTrace) (string, string) {
	reason:= term.Reason
	aborted:= reason == harness.ReasonAbortedStreaming || reason == harness.ReasonAbortedTools
	// Prompt may return ctx.Err directly without a terminal event. Preserve the
	// cancellation cause instead of falling back to an empty/unknown terminal reason.
	if reason == "" && ctx.Err()!= nil {
		aborted = true
	}

	var sum string
	if aborted {
		_, short, _, ok:= AbortReason(ctx)
		if!ok {
			short = "Failed to cancel, reason unknown"
		}
		stage:= "During execution"
		switch reason {
		case harness.ReasonAbortedStreaming:
			stage = "Model output stage"
		case harness.ReasonAbortedTools:
			stage = "Tool execution stage"
		}
		sum = " (Execution Interrupted:" + short + "; Stop at" + stage + progressSuffix(term, tr) + ", Not completed)"
	} else if reason == harness.ReasonMaxTurns || reason == harness.ReasonTimeout {
		sum = " (It has reached the upper limit of the running budget.(" + string(reason) + "), Facts have been written back at the beginning and end" + progressSuffix(term, tr) + "; No text summary this time)"
	} else {
		hint:= terminalReasonHint(reason)
		sum = " (No text summary, final state " + terminalReasonLabel(reason) + ": " + firstLine(hint, 80) + ")"
	}

	var b strings.Builder
	b.WriteString(sum)
	b.WriteString("\n\n")
	displayReason:= terminalReasonLabel(reason)
	fmt.Fprintf(&b, "- **Export**: `%s` - %s\n", displayReason, terminalReasonHint(reason))
	if aborted {
		code, _, why, ok:= AbortReason(ctx)
		if ok {
			fmt.Fprintf(&b, "- **interruption reason** (`%s`): %s\n", code, why)
		} else {
			b.WriteString("- **interruption reason**: Unable to retrieve; canceler may not have passed through context.WithCancelCause Append named reason\n")
		}
	}
	if term.Err!= nil {
		fmt.Fprintf(&b, "- **Underlying Error**: `%v`\n", term.Err)
	}
	if aborted && strings.TrimSpace(term.Text)!= "" {
		b.WriteString("- **Cancel before generating partial output.**:\n\n")
		b.WriteString(term.Text)
		b.WriteString("\n\n")
	}
	if term.Turns > 0 {
		fmt.Fprintf(&b, "- **Already executed**: %d Round model round\n", term.Turns)
	}
	if!tr.startedAt.IsZero() {
		fmt.Fprintf(&b, "- **Time taken for this run**: %s\n", roundDur(time.Since(tr.startedAt)))
	}
	if u:= term.Usage; u.InputTokens+u.OutputTokens+u.CacheReadTokens+u.CacheWriteTokens > 0 {
		fmt.Fprintf(&b, "- **Cumulative tokens**: Input %d / Output %d / Cache Read %d / Cache write %d\n",
			u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens)
	}
	if tr.name == "" {
		b.WriteString("- **Tool call**: This run ended without making any tool calls\n")
	} else if tr.pending {
		fmt.Fprintf(&b, "- **Tools being executed when interrupted**: `%s` (Already run %s, **No result returned.**)\n\n ```json\n %s\n ```\n",
			tr.name, roundDur(time.Since(tr.at)), firstLine(tr.input, 300))
	} else {
		fmt.Fprintf(&b, "- **The last tool before interruption**: `%s` (Normal return)\n", tr.name)
	}
	return sum, b.String()
}

func terminalReasonLabel(reason harness.TerminalReason) string {
	if reason == "" {
		return "context_canceled"
	}
	return string(reason)
}

func terminalReasonHint(reason harness.TerminalReason) string {
	if hint:= reasonHint[reason]; hint!= "" {
		return hint
	}
	if reason == "" {
		return "The running context has been cancelled, but the underlying did not produce a Terminal event"
	}
	return "Unknown final state; harness may have added TerminalReason, please add reasonHint."
}

func progressSuffix(term *harness.Terminal, tr *runTrace) string {
	var parts []string
	if term.Turns > 0 {
		parts = append(parts, fmt.Sprintf("%d Loop", term.Turns))
	}
	if!tr.startedAt.IsZero() {
		parts = append(parts, roundDur(time.Since(tr.startedAt)))
	}
	if len(parts) == 0 {
		return ""
	}
	return ", Already run " + strings.Join(parts, " / ")
}

func roundDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return d.Round(100 * time.Millisecond).String()
	case d < time.Hour:
		return d.Round(time.Second).String()
	default:
		return d.Round(time.Minute).String()
	}
}
