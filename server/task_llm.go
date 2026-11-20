package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Zeerg/artex/agent"
	"github.com/Zeerg/artex/db"
	"github.com/Zeerg/artex/llmrec"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
)

type taskAgentBundle struct {
	runtime *taskLLMRuntime // goal decomposition runtime
	plannerRuntime *taskLLMRuntime
	workerRuntime *taskLLMRuntime
	mainRuntime *taskLLMRuntime
	pl *agent.Planner
	wk *agent.Worker
	main *agent.MainAgent
}

type llmAuditProfile struct {
	ID int64 `json:"id"`
	Name string `json:"name"`
	Format string `json:"format"`
	Model string `json:"model"`
}

type llmTransitionAudit struct {
	Mode string `json:"mode"` // automatic | manual | exhausted
	Reason string `json:"reason"`
	Previous *llmAuditProfile `json:"previous,omitempty"`
	Next *llmAuditProfile `json:"next,omitempty"`
}

type llmActivityMetadata struct {
	LLMTransition llmTransitionAudit `json:"llm_transition"`
}

type taskLLMRuntime struct {
	s *Server
	taskID string
	agentKey string
}

type taskLLMError struct {
	taskID string
	chainExhausted bool
	cause error
}

func (e *taskLLMError) Error() string {
	if e.chainExhausted {
		return fmt.Sprintf("task %s LLM profile chain exhausted: %v", e.taskID, e.cause)
	}
	return e.cause.Error()
}

func (e *taskLLMError) Unwrap() error { return e.cause }

func isTaskLLMRuntimeError(err error) bool {
	var target *taskLLMError
	return errors.As(err, &target)
}

func isTaskLLMChainExhausted(err error) bool {
	var target *taskLLMError
	return errors.As(err, &target) && target.chainExhausted
}

// isQuotaExhaustedError is intentionally strict. A generic 429, auth error,
// network failure, or 5xx does not rotate providers; the response must explicitly
// identify quota, credits, billing balance, or payment exhaustion.
func isQuotaExhaustedError(err error) bool {
	return err!= nil && agent.IsQuotaExhaustedMessage(err.Error())
}

type taskLLMSelection struct {
	task *Task
	profileID int64
	revision int64
	provider llm.Provider
	// retry These are retry parameters parsed from the selected configuration this time(profile Coverage → Global policy → built-in default).
	// Retry according to the security window of the same provider.,So changing profiles changes the retry rhythm.
	retry agent.RetryConfig
}

type taskLLMStreamHooks struct {
	current func() (taskLLMSelection, error)
	exhaust func(taskLLMSelection, error) (db.TaskLLMTransition, error)
	transition func(taskLLMSelection, db.TaskLLMTransition, error)
}

// current resolves the provider this role runs on, by precedence:
// Agent Bind → Task LLM Configuration Chain. → Global/environment configuration.
// Binding takes precedence over task chains: if a role is explicitly specified a model, it runs on that model continuously; binding does not exist or
// Downgrade to task chain only when build fails, further downgrade to global config if task chain is empty.
// The returned profile ID is only non-zero when following the task chain. —— streamTaskLLM To determine if the quota error is due to
// Should advance the task's fault transfer state (binding/global path unchanged, retain existing semantics).
func (r *taskLLMRuntime) current() (taskLLMSelection, error) {
	taskNum, err:= parseTaskID(r.taskID)
	if err!= nil {
		return taskLLMSelection{}, err
	}
	pt, err:= r.s.m.pg.GetTask(taskNum)
	if err!= nil {
		return taskLLMSelection{}, err
	}
	if pt == nil {
		return taskLLMSelection{}, fmt.Errorf("task %s not found", r.taskID)
	}
	r.s.syncTaskLLMState(pt)
	t, _:= r.s.m.Task(r.taskID)
	sel:= taskLLMSelection{task: t, revision: pt.LLMChainRevision}
	if prov, cfg, ok:= r.s.agentBindingProvider(r.agentKey); ok {
		sel.provider, sel.retry = prov, cfg.Retry
		return sel, nil
	}
	if len(pt.LLMProfileIDs) > 0 {
		if pt.ActiveLLMProfileID == nil {
			return sel, &taskLLMError{taskID: r.taskID, chainExhausted: true, cause: errors.New("all selected profiles are quota exhausted")}
		}
		sel.profileID = *pt.ActiveLLMProfileID
		prov, cfg, ok:= r.s.providerForProfile(sel.profileID)
		if!ok {
			return sel, fmt.Errorf("LLM profile #%d is missing or invalid", sel.profileID)
		}
		sel.provider, sel.retry = prov, cfg.Retry
		return sel, nil
	}
	prov, cfg, ok:= r.s.globalProvider()
	if!ok {
		return sel, fmt.Errorf("task %s has no available fallback LLM provider", r.taskID)
	}
	sel.provider, sel.retry = prov, cfg.Retry
	return sel, nil
}

// activeCfg resolves the task's currently-active LLM config, mirroring current()'s
// source precedence (agent binding → active chain profile → global). Read-only and
// best-effort: ok=false when nothing resolves, leaving the per-setting fallback to
// the caller. If failover switches profiles, the change takes effect on the next
// agent run (a fresh Session is built per run in captureRun).
func (r *taskLLMRuntime) activeCfg() (agent.Config, bool) {
	taskNum, err:= parseTaskID(r.taskID)
	if err!= nil {
		return agent.Config{}, false
	}
	pt, err:= r.s.m.pg.GetTask(taskNum)
	if err!= nil || pt == nil {
		return agent.Config{}, false
	}
	if _, cfg, ok:= r.s.agentBindingProvider(r.agentKey); ok {
		return cfg, true
	}
	if len(pt.LLMProfileIDs) > 0 && pt.ActiveLLMProfileID!= nil {
		if _, cfg, ok:= r.s.providerForProfile(*pt.ActiveLLMProfileID); ok {
			return cfg, true
		}
	}
	if _, cfg, ok:= r.s.globalProvider(); ok {
		return cfg, true
	}
	return agent.Config{}, false
}

// nonStreaming reports whether the task's currently-active LLM source is set to
// non-streaming. Unresolvable → streaming (false), the safe default.
func (r *taskLLMRuntime) nonStreaming() bool {
	cfg, ok:= r.activeCfg()
	return ok &&!cfg.Stream
}

// maxTokens returns the currently-active source's per-reply output cap.
// Unresolvable → 0, i.e. send no cap, matching the pre-setting behaviour.
func (r *taskLLMRuntime) maxTokens() int {
	cfg, _:= r.activeCfg() // Zero value 0 when configuration is not parsed
	return cfg.MaxTokens
}

func parseTaskID(id string) (int64, error) {
	n, err:= strconv.ParseInt(id, 10, 64)
	if err!= nil || n <= 0 {
		return 0, fmt.Errorf("invalid task id %q", id)
	}
	return n, nil
}

// streamHooks builds the failover/exhaustion callbacks shared by Stream and
// Complete: how to read the current profile selection, how to mark it quota
// exhausted, and how to emit a failover transition.
func (r *taskLLMRuntime) streamHooks() taskLLMStreamHooks {
	return taskLLMStreamHooks{
		current: r.current,
		exhaust: func(selection taskLLMSelection, cause error) (db.TaskLLMTransition, error) {
			taskNum, _:= parseTaskID(r.taskID)
			transition, err:= r.s.m.pg.MarkTaskLLMProfileQuotaExhaustedAtRevision(taskNum, selection.profileID, selection.revision, cause.Error())
			if err!= nil {
				return transition, err
			}
			if pt, getErr:= r.s.m.pg.GetTask(taskNum); getErr == nil && pt!= nil {
				r.s.syncTaskLLMState(pt)
			}
			return transition, nil
		},
		transition: func(selection taskLLMSelection, transition db.TaskLLMTransition, cause error) {
			r.s.emitTaskLLMTransition(selection.task, transition, cause)
		},
	}
}

func (r *taskLLMRuntime) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	ctx = llmrec.WithTaskID(ctx, r.taskID)
	return streamTaskLLM(ctx, r.taskID, req, r.streamHooks())
}

// Complete is the non-streaming counterpart of Stream. A non-streaming call is
// atomic — it never delivers partial output — so every failure is safe to retry
// on the same provider or fail over to the next profile without risking
// duplicated model output or tool execution (the "committed" bookkeeping the
// streaming path needs is unnecessary here).
func (r *taskLLMRuntime) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	ctx = llmrec.WithTaskID(ctx, r.taskID)
	return completeTaskLLM(ctx, r.taskID, req, r.streamHooks())
}

func completeTaskLLM(ctx context.Context, taskID string, req llm.CompletionRequest, hooks taskLLMStreamHooks) (llm.Message, string, llm.Usage, error) {
	for {
		selection, err:= hooks.current()
		if err!= nil {
			return llm.Message{}, "", llm.Usage{}, err
		}
		var (
			msg llm.Message
			sr string
			usage llm.Usage
			callErr error
		)
		// Retry with the same provider security window:Non-streaming calls must either succeed entirely or fail entirely.,None
		// Issues encountered during mid-delivery output,So any transient failure can be retried verbatim.
		retries, backoffOf:= sameProviderRetryPolicy(selection.retry)
		for attempt:= 0;; attempt++ {
			msg, sr, usage, callErr = selection.provider.Complete(ctx, req)
			if callErr!= nil && ctx.Err() == nil &&
				attempt < retries && isRetryableStreamError(callErr) {
				backoff:= backoffOf(attempt)
				log.Printf("[task-llm] task %s Non-streaming call failed,%v Retry with the provider later. (%d/%d): %v",
					taskID, backoff, attempt+1, retries, callErr)
				if sleepCtx(ctx, backoff) {
					break // Ctx canceled during backoff → Stop Retrying,
				}
				continue
			}
			break
		}
		if callErr == nil {
			return msg, sr, usage, nil
		}
		// profileID=0: Explicit chain cleared.;Non-quota error:Transparently pass through. Neither changes the task failover status.
		if selection.profileID == 0 ||!isQuotaExhaustedError(callErr) {
			return llm.Message{}, "", llm.Usage{}, callErr
		}
		transition, markErr:= hooks.exhaust(selection, callErr)
		if markErr!= nil {
			return llm.Message{}, "", llm.Usage{}, fmt.Errorf("mark profile quota exhausted after %v: %w", callErr, markErr)
		}
		if transition.Advanced &&!transition.Stale && hooks.transition!= nil {
			hooks.transition(selection, transition, callErr)
		}
		if!transition.Stale && transition.NextProfileID == nil {
			return llm.Message{}, "", llm.Usage{}, &taskLLMError{taskID: taskID, chainExhausted: transition.ChainExhausted, cause: callErr}
		}
		// No output delivered to the caller.,Switching to another profile to replay the same logic request is safe.
	}
}

func streamTaskLLM(ctx context.Context, taskID string, req llm.CompletionRequest, hooks taskLLMStreamHooks) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		for {
			selection, err:= hooks.current()
			if err!= nil {
				yield(llm.StreamEvent{}, err)
				return
			}
			committed:= false
			var pending []llm.StreamEvent
			var streamErr error
			retries, backoffOf:= sameProviderRetryPolicy(selection.retry)
			// Retry with the same provider security window:committed Before(No output has been delivered to the caller yet.)
			// Instantaneous failures can be replayed as-is,Will not repeat model outputs or tool executions. After committed,
			// ctx Exit if canceled or a deterministic/error quota is reached,Pass it on to the existing transparent/fallback logic below.
			for attempt:= 0;; attempt++ {
				committed = false
				pending = nil
				streamErr = nil
				for event, err:= range selection.provider.Stream(ctx, req) {
					if err!= nil {
						streamErr = err
						break
					}
					if!committed &&!streamEventCommitsOutput(event) {
						pending = append(pending, event)
						continue
					}
					if!committed {
						for _, buffered:= range pending {
							if!yield(buffered, nil) {
								return
							}
						}
						pending = nil
						committed = true
					}
					if!yield(event, nil) {
						return
					}
				}
				if streamErr!= nil &&!committed && ctx.Err() == nil &&
					attempt < retries && isRetryableStreamError(streamErr) {
					backoff:= backoffOf(attempt)
					log.Printf("[task-llm] task %s Stream failure before submission,%v Retry with the provider later. (%d/%d): %v",
						taskID, backoff, attempt+1, retries, streamErr)
					if sleepCtx(ctx, backoff) {
						break // Ctx canceled during backoff → Stop Retrying,
					}
					continue
				}
				break
			}
			if streamErr == nil {
				for _, buffered:= range pending {
					if!yield(buffered, nil) {
						return
					}
				}
				return
			}
			// profileID=0 means the explicit chain was cleared while this stable task
			// bundle was still in use. Agent/global fallback errors follow the legacy
			// behavior and never mutate task failover state.
			if selection.profileID == 0 ||!isQuotaExhaustedError(streamErr) {
				for _, buffered:= range pending {
					if!yield(buffered, nil) {
						return
					}
				}
				yield(llm.StreamEvent{}, streamErr)
				return
			}
			transition, markErr:= hooks.exhaust(selection, streamErr)
			if markErr!= nil {
				cause:= fmt.Errorf("mark profile quota exhausted after %v: %w", streamErr, markErr)
				if committed {
					// Output may already have driven tool execution. Report the persistence
					// failure, but classify it as router-handled so the worker does not
					// replay the entire intent and duplicate those side effects.
					yield(llm.StreamEvent{}, &taskLLMError{taskID: taskID, cause: cause})
				} else {
					yield(llm.StreamEvent{}, cause)
				}
				return
			}
			if transition.Advanced &&!transition.Stale && hooks.transition!= nil {
				hooks.transition(selection, transition, streamErr)
			}
			if committed || (!transition.Stale && transition.NextProfileID == nil) {
				yield(llm.StreamEvent{}, &taskLLMError{taskID: taskID, chainExhausted: transition.ChainExhausted, cause: streamErr})
				return
			}
			// No event reached the caller, so replaying the same logical request on
			// the next profile cannot duplicate model output or tool execution.
		}
	}
}

func streamEventCommitsOutput(event llm.StreamEvent) bool {
	switch event.Type {
	case llm.SETextDelta, llm.SEThinkingDelta, llm.SEToolInputJSON:
		return event.Text!= ""
	case llm.SEToolUseStart, llm.SEMessageDelta, llm.SEMessageStop:
		return true
	default:
		return false
	}
}

// Submission window before submission for default retries on the same provider. SDK's doStream only retries connection
// Stage(Before getting 200);Once the flow starts,Interruption / overload / in-band 429 etc. transient faults are directly
// Bubble up as model_error.,Zero retries. As long as no token has been handed over to the caller yet(!committed),Replay
// Identical requests will not repeat model output or tool side effects,Therefore, add an extra layer of retry with backoff for the same provider,
// Dampen these fluctuations before the overall intent reruns. Can be overridden by LLM configuration retry policies or global retry strategies.
const sameProviderStreamRetries = 2

// sameProviderRetryBackoff Default backoff before the attempt retry.(0.5s, 1s...,Limit 4s),
// Mimic the SDK's exponential gradient but with a smaller cap.,Avoid holding up worker's end/cancellation response.
// Expose as variables,for testing, set backoff to zero.
var sameProviderRetryBackoff = func(attempt int) time.Duration {
	return min(500*time.Millisecond*(1<<attempt), 4*time.Second)
}

// sameProviderRetryPolicy Parse which retry parameters to use with this provider for this call:If set in the configuration, it's
// use the configured(negative = Turn off this layer of retry),If an interval is set, switch exponential backoff to fixed interval.,Neither is set when both are unset
// Consistent with the previous byte-by-byte.
func sameProviderRetryPolicy(r agent.RetryConfig) (retries int, backoff func(int) time.Duration) {
	retries, backoff = sameProviderStreamRetries, sameProviderRetryBackoff
	if r.StreamAttempts!= 0 {
		retries = max(r.StreamAttempts, 0)
	}
	if r.StreamInterval > 0 {
		d:= r.StreamInterval
		backoff = func(int) time.Duration { return d }
	}
	return retries, backoff
}

// isRetryableStreamError Determine if 'stream failure before submission' is worth replaying on the same provider.
// Transient transmission interruption / supplier overload / rate limiting will recover automatically,Can be safely retried;The following three categories will not be retried:
// - Quota exhausted:Hand over to the profile for fault transfer handling.,Don't waste retries here
// - Context too long:Replaying the same request is also useless,Hand over to harness's reactive compression fallback
// - 4xx Deterministic rejection(400/401/403/404/422):It will fail with any provider.
func isRetryableStreamError(err error) bool {
	if err == nil {
		return false
	}
	if isQuotaExhaustedError(err) {
		return false
	}
	s:= strings.ToLower(err.Error())
	if strings.Contains(s, "too long") || strings.Contains(s, "context length") ||
		strings.Contains(s, "context_length") || strings.Contains(s, "maximum context") ||
		strings.Contains(s, "status 413") {
		return false
	}
	for _, code:= range []string{"status 400", "status 401", "status 403", "status 404", "status 422"} {
		if strings.Contains(s, code) {
			return false
		}
	}
	// The rest(Transmission reset/EOF/timeout, 408/429/5xx, stream error events like anthropic
	// overloaded_error Equal)All considered instantaneous,Allow retry.
	return true
}

// CompactionWindow mirrors current()'s precedence so the context window always
// matches the provider the role will actually stream on.
func (r *taskLLMRuntime) CompactionWindow() int {
	if _, cfg, ok:= r.s.agentBindingProvider(r.agentKey); ok {
		return cfg.CompactionWindow()
	}
	taskNum, err:= parseTaskID(r.taskID)
	if err!= nil {
		return (agent.Config{}).CompactionWindow()
	}
	chain, err:= r.s.m.pg.TaskLLMProfiles(taskNum)
	if err!= nil {
		return (agent.Config{}).CompactionWindow()
	}
	if len(chain) == 0 {
		if _, cfg, ok:= r.s.globalProvider(); ok {
			return cfg.CompactionWindow()
		}
		return (agent.Config{}).CompactionWindow()
	}
	minimum:= 0
	for _, entry:= range chain {
		if cfg, ok:= r.s.loadProfileConfig(entry.ProfileID); ok {
			window:= cfg.CompactionWindow()
			if minimum == 0 || window < minimum {
				minimum = window
			}
		}
	}
	if minimum == 0 {
		return (agent.Config{}).CompactionWindow()
	}
	return minimum
}

// agentBindingProvider resolves the profile a role is explicitly bound to
// (agents.llm_profile_id) — the highest-precedence level for task agents. ok=false
// when the role has no binding or the bound profile no longer builds, so callers
// fall through to the task chain. A bound profile stays exclusive unless
// llm_pool_bind_fallback is on, which is what poolForBinding encodes.
func (s *Server) agentBindingProvider(agentKey string) (llm.Provider, agent.Config, bool) {
	id:= s.effectiveProfileForAgent(agentKey, nil)
	if id == nil {
		return nil, agent.Config{}, false
	}
	prov, cfg, ok:= s.providerForProfile(*id)
	if!ok {
		return nil, agent.Config{}, false
	}
	return s.poolForBinding(*id, prov, cfg), cfg, true
}

// globalProvider returns the process-wide provider (persisted active profile or
// environment config) — the last resort once a role has neither a binding nor a
// task chain.
func (s *Server) globalProvider() (llm.Provider, agent.Config, bool) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	if!s.llmOn || s.llmProv == nil {
		return nil, agent.Config{}, false
	}
	return s.llmProv, s.llmCfg, true
}

// taskRuntimeAvailable reports whether every listed role can resolve a provider
// under the runtime precedence in current(): the role's own binding first, then
// the task chain, then global. An exhausted chain is a hard stop for unbound
// roles rather than a silent fall through to global — same as current().
func (s *Server) taskRuntimeAvailable(t *Task, agentKeys ...string) bool {
	if t == nil || len(agentKeys) == 0 {
		return false
	}
	state:= t.llmStateSnapshot()
	unboundReady:= false
	if len(state.ProfileIDs) > 0 {
		if state.ActiveID!= nil {
			_, _, unboundReady = s.providerForProfile(*state.ActiveID)
		}
	} else {
		_, _, unboundReady = s.globalProvider()
	}
	for _, key:= range agentKeys {
		if _, _, ok:= s.agentBindingProvider(key); ok {
			continue
		}
		if!unboundReady {
			return false
		}
	}
	return true
}

func (s *Server) invalidateTaskAgents() {
	s.taskAgentMu.Lock()
	s.taskAgents = map[string]*taskAgentBundle{}
	s.taskAgentMu.Unlock()
}

func (s *Server) agentsForTask(t *Task) *taskAgentBundle {
	s.taskAgentMu.Lock()
	defer s.taskAgentMu.Unlock()
	if bundle:= s.taskAgents[t.ID]; bundle!= nil {
		return bundle
	}
	goalRuntime:= &taskLLMRuntime{s: s, taskID: t.ID, agentKey: "goals"}
	plannerRuntime:= &taskLLMRuntime{s: s, taskID: t.ID, agentKey: "planner"}
	workerRuntime:= &taskLLMRuntime{s: s, taskID: t.ID, agentKey: "worker"}
	mainRuntime:= &taskLLMRuntime{s: s, taskID: t.ID, agentKey: "mainagent"}
	tx:= transcript.NewStore(filepath.Join(s.m.dir, "transcripts"))
	window:= workerRuntime.CompactionWindow()
	wk:= agent.NewWorker(workerRuntime, "task-router", s.m.dir, tx, window, s.agentMaxTurns("worker"))
	wk.SetFindingRecorder(s.evidenceStore())
	wk.SetCompactionWindowResolver(workerRuntime.CompactionWindow)
	wk.SetNonStreaming(workerRuntime.nonStreaming) // Stream switch based on the current active profile of the task(Read per round)
	wk.SetMaxTokens(workerRuntime.maxTokens) // Same as Above,The output limit also follows the currently active profile.
	wk.SetNoaEnabled(s.m.NoaCompactionEnabled) // Experimental Features:noa Context compression(Platform-level switch,Every run reads)
	wk.SetRunTimeout(time.Duration(s.agentRunSeconds("worker")) * time.Second)
	wk.SetProxy(s.m.ProxyAddr(), s.m.ProxyCACert())
	wk.SetWebSearch(s.webSearchFor("worker"))
	wk.SetConstraintInject(s.constraintInjectWorker) // Inject operation constraints into worker(Configurable,Default On)
	pl:= agent.NewPlanner(plannerRuntime, "task-router", s.m.dir, tx, plannerRuntime.CompactionWindow(), s.agentMaxTurns("planner"))
	pl.SetFindingRecorder(s.evidenceStore())
	pl.SetCompactionWindowResolver(plannerRuntime.CompactionWindow)
	pl.SetNonStreaming(plannerRuntime.nonStreaming)
	pl.SetMaxTokens(plannerRuntime.maxTokens)
	pl.SetNoaEnabled(s.m.NoaCompactionEnabled) // Experimental Features:noa Context compression(Platform-level switch,Every run reads)
	pl.SetKillWork(s.engine.KillWork)
	pl.SetSteerWork(s.engine.SteerWork)
	pl.SetProxy(s.m.ProxyAddr(), s.m.ProxyCACert())
	pl.SetWebSearch(s.webSearchFor("planner"))
	pl.SetConstraintInject(s.constraintInjectPlanner) // Inject operation constraints into planner(Configurable,Default On)
	// cold-digest §7: Cold node background compression. The engine is actually driven by this per-task planner.
	// (agentsForTask),Compactor Must be handled here. Use the planner provider for task routing(§4:With agent
	// Same model,Follow the task LLM chain for parsing),Compress using Complete to generate body once.
	pl.SetCompactor(agent.NewCompactor(plannerRuntime, "task-router"))
	main:= agent.NewMainAgent(mainRuntime, "task-router", s.m.dir, tx, mainRuntime.CompactionWindow(), s.agentMaxTurns("mainagent"))
	main.SetFindingRecorder(s.evidenceStore())
	main.SetCompactionWindowResolver(mainRuntime.CompactionWindow)
	main.SetNonStreaming(mainRuntime.nonStreaming)
	main.SetMaxTokens(mainRuntime.maxTokens)
	main.SetNoaEnabled(s.m.NoaCompactionEnabled) // Experimental Features:noa Context compression(Platform-level switch,Every run reads)
	main.SetProxy(s.m.ProxyAddr(), s.m.ProxyCACert())
	main.SetWebSearch(s.webSearchFor("mainagent"))
	main.SetSteerWork(s.engine.SteerWork) // steer_work: Manually correct running work in real-time
	bundle:= &taskAgentBundle{
		runtime: goalRuntime, plannerRuntime: plannerRuntime, workerRuntime: workerRuntime,
		mainRuntime: mainRuntime, pl: pl, wk: wk, main: main,
	}
	s.taskAgents[t.ID] = bundle
	return bundle
}

func (s *Server) syncTaskLLMState(pt *db.Task) {
	if pt == nil {
		return
	}
	id:= fmt.Sprintf("%d", pt.ID)
	s.m.mu.Lock()
	if task:= s.m.tasks[id]; task!= nil {
		task.setLLMState(pt.LLMProfileID, pt.ActiveLLMProfileID, pt.LLMProfileIDs, pt.LLMChainRevision, pt.LLMFailoverState, pt.LLMFailoverReason)
	}
	s.m.mu.Unlock()
}

func (s *Server) emitTaskLLMTransition(t *Task, transition db.TaskLLMTransition, cause error) {
	if t == nil {
		return
	}
	previous:= s.llmAuditProfile(transition.PreviousProfileID)
	var next *llmAuditProfile
	if transition.NextProfileID!= nil {
		next = s.llmAuditProfile(*transition.NextProfileID)
	}
	mode:= "automatic"
	kind:= "llm_switch"
	summary:= fmt.Sprintf("%s Insufficient quota", llmAuditProfileLabel(previous))
	if transition.NextProfileID!= nil {
		summary += fmt.Sprintf(", Switch to subsequent call. %s", llmAuditProfileLabel(next))
	} else {
		mode = "exhausted"
		kind = "llm_failover"
		summary += ", Configuration chain exhausted"
	}
	metadata, _:= json.Marshal(llmActivityMetadata{LLMTransition: llmTransitionAudit{
		Mode: mode, Reason: cause.Error(), Previous: previous, Next: next,
	}})
	s.engine.emitActivity(t, db.Activity{Worker: "system", Kind: kind, IsError: transition.ChainExhausted, Summary: summary, Detail: cause.Error(), Metadata: metadata})
	log.Printf("[llm-failover] task %s: %s", t.ID, summary)
}

func (s *Server) llmAuditProfile(id int64) *llmAuditProfile {
	if id <= 0 || s.m == nil || s.m.pg == nil {
		return nil
	}
	p, err:= s.m.pg.ProfileByID(id)
	if err!= nil || p == nil {
		return &llmAuditProfile{ID: id, Name: fmt.Sprintf("Version #%d", id)}
	}
	return &llmAuditProfile{ID: p.ID, Name: p.Name, Format: p.Format, Model: p.Model}
}

func llmAuditProfileLabel(profile *llmAuditProfile) string {
	if profile == nil {
		return "Default configuration"
	}
	name:= profile.Name
	if name == "" {
		name = fmt.Sprintf("Version #%d", profile.ID)
	}
	detail:= []string{}
	if profile.Format!= "" {
		detail = append(detail, profile.Format)
	}
	if profile.Model!= "" {
		detail = append(detail, profile.Model)
	}
	if len(detail) == 0 {
		return name
	}
	return fmt.Sprintf("%s (%s)", name, strings.Join(detail, " / "))
}

func sameOptionalID(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (s *Server) emitManualTaskLLMSwitch(t *Task, previousID, nextID *int64) db.Activity {
	previous:= (*llmAuditProfile)(nil)
	next:= (*llmAuditProfile)(nil)
	if previousID!= nil {
		previous = s.llmAuditProfile(*previousID)
	}
	if nextID!= nil {
		next = s.llmAuditProfile(*nextID)
	}
	summary:= fmt.Sprintf("User manually changed the task LLM from %s Switch to %s", llmAuditProfileLabel(previous), llmAuditProfileLabel(next))
	metadata, _:= json.Marshal(llmActivityMetadata{LLMTransition: llmTransitionAudit{
		Mode: "manual", Reason: "User manually switches tasks LLM", Previous: previous, Next: next,
	}})
	return s.engine.emitActivity(t, db.Activity{Worker: "system", Kind: "llm_switch", Summary: summary, Detail: summary, Metadata: metadata})
}
