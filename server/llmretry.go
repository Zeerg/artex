package server

import (
	"time"

	"github.com/Zeerg/artex/agent"
	"github.com/Zeerg/artex/db"
)

// Server-side parsing of retry strategy, see docs/LLM Retry Design.md. In the fifth layer:
// - Connection / empty response / same provider security window follows the endpoint; each LLM configuration can override
// Global defaults (if a profile item is left empty, it inherits from the global; if the global isn't set, it uses the built-in default).
// - Circuit breaker / intent retry is process-level, only one global instance.
//
// Read once from the DB a line of settings for global policy, all calls are on low-frequency paths (building provider, work end, etc.).
// Save configuration), not worth adding another layer of caching; circuit breaker parameters are exceptions——It reads on every failed path, so
// Pushed by applyRetryPolicy to be saved in Registry.

// retryPolicy reads the global policy; a nil DB yields the zero policy (all
// layers on their built-in defaults).
func (s *Server) retryPolicy() db.LLMRetryPolicy {
	if s.m == nil || s.m.pg == nil {
		return db.LLMRetryPolicy{}
	}
	return s.m.pg.LLMRetryPolicy()
}

// resolveRetry layers one profile's override on top of the global policy and
// converts the result into the form agent.Config carries. Rules combine field by
// field, so a profile that only pins an interval still inherits the global count.
func resolveRetry(o db.RetryOverride, pol db.LLMRetryPolicy) agent.RetryConfig {
	connect:= o.Connect.Or(pol.Connect)
	empty:= o.Empty.Or(pol.Empty)
	stream:= o.Stream.Or(pol.Stream)
	return agent.RetryConfig{
		// Keep '0' here=Default / Negative=Original meaning of 'close':SDK MaxRetries /
		// EmptyResponseRetries Completely isomorphic with it,Let it parse itself.
		ConnectAttempts: connect.Attempts, ConnectInterval: connect.Interval(),
		EmptyAttempts: empty.Attempts, EmptyInterval: empty.Interval(),
		StreamAttempts: stream.Attempts, StreamInterval: stream.Interval(),
	}
}

// applyProfileRetry fills cfg.Retry for a profile read from the DB.
func (s *Server) applyProfileRetry(cfg *agent.Config, p *db.LLMProfile) {
	if p == nil {
		return
	}
	cfg.Retry = resolveRetry(p.Retry, s.retryPolicy())
}

// Circuit Breaker(polling cooldown)Default value,Consistent with llmpool's built-in version —— This only covers when the user has set a value.
// Default value for rerunning intent is seen in engine.go of modelErrorRetries / modelErrorRetryBackoff.

// applyRetryPolicy pushes the process-wide layers of the policy into the objects
// that consume them on a hot path: the circuit-breaker registry. Called at
// startup and whenever the policy is saved.
func (s *Server) applyRetryPolicy() {
	pol:= s.retryPolicy()
	if s.llmHealth!= nil {
		s.llmHealth.SetPolicy(pol.Breaker.Attempts, pol.Breaker.Interval())
	}
}

// modelErrorRetryPolicy resolves the intent-level replay knobs (layer ⑤): how
// many times a model_error work is re-run and how long to back off between runs.
func (e *Engine) modelErrorRetryPolicy() (retries int, backoff time.Duration) {
	retries, backoff = modelErrorRetries, modelErrorRetryBackoff
	if e == nil || e.m == nil || e.m.pg == nil {
		return retries, backoff
	}
	rule:= e.m.pg.LLMRetryPolicy().Intent
	if rule.Attempts!= 0 {
		retries = max(rule.Attempts, 0)
	}
	if d:= rule.Interval(); d > 0 {
		backoff = d
	}
	return retries, backoff
}

// emptyTurnNudgeLimit resolves how many empty-turn continuations one work may
// inject (see steerHooks.Stop). It deliberately reuses layer ②'s knob —— 'Empty response
// Retry Count':Both are means of the same thing. SDK layer manages 'no content block', method is to wrap
// Resend the same request.;This section handles 'only thinking, no text or tools', using a method to append an instruction that
// The model continues to work with existing thoughts(Resending in this context does not make sense for an empty rotation determined by the shape of the context.). Empty criteria
// Different because SDK determines 'whether it has yielded an event' as the criterion, while thinking increments themselves are events——But user configures
// 'When retrying on an empty response, the intention is to 'try again if the model doesn't produce substantive content'. Both layers share a single attempt count.
// Only makes sense for this mindset.
//
// Read global policy instead of coverage from a specific profile:A profile switch may occur midway through a run due to a failover, and this
// It's a total gate for the intent, which shouldn't change with endpoint changes. Semantics match SDK's emptyRetries() Homogeneous:
// 0 = Default to defaultEmptyTurnNudges;-1(be) = Close idle continuation run;>0 = use this value.
func (e *Engine) emptyTurnNudgeLimit() int {
	if e == nil || e.m == nil || e.m.pg == nil {
		return defaultEmptyTurnNudges
	}
	switch n:= e.m.pg.LLMRetryPolicy().Empty.Attempts; {
	case n == 0:
		return defaultEmptyTurnNudges
	case n < 0:
		return 0
	default:
		return n
	}
}
