package agent

import (
	"context"
	"errors"
	"fmt"
)

// AbortCause names why an agent run's context was cancelled. Every cancellation
// site should attach one so the activity trace can report the real initiator.
type AbortCause struct {
	Code string
	Short string
	Text string
}

func (c *AbortCause) Error() string { return c.Text }

func cause(code, short, text string) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: text}
}

// Causef builds a cause that includes runtime-specific detail.
func Causef(code, short, format string, args ...any) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: fmt.Sprintf(format, args...)}
}

var (
	// Task-level execution context.
	AbortPausedByUser = cause("paused_by_user", "User Paused Task",
		"Users control tasks via the task control interface (POST /api/tasks/).{id}/control, action=pause)Task paused. This Planner/Worker run was actively canceled; running intents will be returned to the frontier.(open), Resume task after recovery and re-fetch from start,")
	AbortPausedByOrchestrator = cause("paused_by_orchestrator", "Orchestration Agent paused the task",
		"The Agent calls pause_task tool to pause this task. This Planner/Worker run is actively canceled; the running intent will be returned to frontier(open), Re-execute after recovery.")
	AbortTaskDeleted = cause("task_deleted", "Task deleted",
		"Task is being deleted (DELETE /api/tasks/{id}), The deletion barrier has canceled the Planner, Worker, and main Agent running this task; this run's results will not be used again.")
	AbortPausedOnReload = cause("paused_on_reload", "Task pause restored on startup",
		"When the backend starts, it recovers task pauses based on the persisted state in the database. This run was canceled; under normal circumstances, there should be no running Agent during recovery.")
	AbortGoalMet = cause("goal_met", "Planner judged goal achieved",
		"Planner determines task goal is achieved and sets task as done, then cancels still running Worker; these intents will be marked as stopped, not failed")
	AbortSettleDrainTimeout = cause("settle_drain_timeout", "Drain window exhausted",
		"After the task reaches timeout, wait for the running Worker to gracefully wind down. However, a 90-second drain allowance is still insufficient, so we execute a hard cancellation; the intent will be marked as exhausted, and facts and assets written during the wind-down phase will be retained.")

	// Per-work context.
	AbortKilledByPlanner = cause("killed_by_planner", "Planner terminated this intent",
		"The planner called kill_work to actively terminate this intent, usually indicating that the direction has gone off course or no longer has value; the intent will be marked as stopped and will not automatically re-claim.")
	AbortWorkPausedByUser = cause("work_paused_by_user", "The user paused this Worker's intent",
		"The user paused an ongoing Worker. This call is canceled, and the intent is changed to paused; all registered intents, facts, vulnerabilities, and activity records are retained. After recovery, it will start from the beginning again.")
	AbortWorkCancelledByUser = cause("work_cancelled_by_user", "User deleted this Worker intent",
		"User deleted running Worker. This call is canceled; after Worker exits write zone, server handles intent according to user's chosen deletion mode——Soft delete marks as deleted and retains all output. True deletion cascades removes this intent and downstream nodes supported solely by it.")
	AbortWorkFinished = cause("work_finished", "Worker finished normally",
		"Worker It has ended normally. The engine releases its context resources in detachWork. This is not a runtime interruption; if it appears in an interrupt message, it indicates a race condition between cancellation and the end-of-session event.")
	AbortPausedRaceGuard = cause("paused_race_guard", "New runs refused while task paused",
		"When the task is paused, the engine refuses to issue new execution contexts to prevent a race condition between claim and pause causing the Worker to continue starting; already received intents will be returned to frontier")

	// Main Agent and standalone conversation contexts.
	AbortChatStoppedByUser = cause("chat_stopped_by_user", "User stopped this round of conversation.",
		"User clicked stop, actively terminating the current main Agent or session Agent run. Existing activity records will be retained, and you can continue sending the next message.")
	AbortChatPausedWithTask = cause("chat_paused_with_task", "Task pause ended this conversation",
		"When a user pauses a task, the running main Agent conversation is also synchronized to cancel it. Existing activity records are retained; messages from this round will not be automatically replayed when the task is resumed.")
	AbortChatTurnFinished = cause("chat_turn_finished", "Conversation ended normally",
		"The current conversation has ended normally, and the server is releasing the resources for this round's context. This is not an interruption; if it appears in an interruption message, it indicates a race condition between cancellation and the end-of-session event.")

	// Process-level and per-run hard backstop.
	AbortShutdown = cause("shutdown", "Backend process is shutting down.",
		"The backend process received SIGINT or SIGTERM, is restarting, updating, or shutting down. All running Agents will be canceled; after restart, residual running intents will be reset to open and re-executed")
	AbortRunHardTimeout = cause("run_hard_timeout", "Hard timeout for a run triggered",
		"If a single run exceeds the soft wall clock budget and additional grace period, it indicates that the model request or some tool has been stuck for too long, preventing normal round boundaries from being executed. Please focus on checking the last unreturned tool call before interruption.")
)

// AbortReason resolves the named cause attached to a cancelled run context.
func AbortReason(ctx context.Context) (code, short, text string, ok bool) {
	c:= context.Cause(ctx)
	if c == nil {
		return "", "", "", false
	}
	var ac *AbortCause
	if errors.As(c, &ac) {
		return ac.Code, ac.Short, ac.Text, true
	}
	switch {
	case errors.Is(c, context.DeadlineExceeded):
		return "deadline_exceeded", "Upstream context reached deadline",
			"Upstream context reaches deadline, but the setter did not attach named causes using WithTimeoutCause: " + c.Error(), true
	case errors.Is(c, context.Canceled):
		return "canceled_no_cause", "Cancellation reason without named cause",
			"The upstream context was canceled, but the canceler did not pass through the context..WithCancelCause Append named reason; please add agent/cancelcause.go Log the reason for registration and connect to this cancellation point", true
	default:
		return "other", firstLine(c.Error(), 80), c.Error(), true
	}
}
