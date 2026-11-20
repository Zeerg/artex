// Package agent wires real LLM-driven planner and work agents (on top of the
// agent-core SDK) to the dual SQLite graph. See docs/ARTEX-Architecture Design.md
// §4.3 (planner) and §4.4 (work agent).
//
// Provider configuration is read from the environment so the system runs with
// any Anthropic- or OpenAI-format endpoint. If no key is configured, FromEnv
// returns ok=false and the exploration engine stays idle (an LLM is required).
package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Zeerg/artex/llmrec"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/compaction"
	"github.com/Autumn-27/norma/llm"
	acperm "github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/transcript"
)

// Config describes the LLM backend resolved from the environment.
type Config struct {
	Format llm.Format
	BaseURL string
	APIKey string
	Model string
	// Proxy routes all LLM requests through the given proxy URL (http/https/socks5,
	// optionally with user:pass@ credentials). Empty means direct — it does NOT
	// fall back to the standard *_PROXY environment variables.
	Proxy string
	// RatePerSecond / RatePerMinute cap the shared request rate across ALL agents
	// using the provider (0 = that window unlimited).
	RatePerSecond float64
	RatePerMinute float64
	// ContextWindowK is the model's context window in K tokens (user-configured),
	// used to size compaction thresholds. 0 = default; see CompactionWindow.
	ContextWindowK int
	// ThinkingType Independently control the thinking 'switch' field(thinking.type):
	// "" = Do not send(Replacement,Compatibility with models not supporting this field); "disabled" = Explicitly Close,;
	// "enabled" = enable. Decoupled from ReasoningEffort——Some interfaces do not have thinking fields,
	// Can be activated by strength parameters alone.,Therefore, they can each be set independently.
	ThinkingType string
	// ReasoningEffort Independently control the 'strength' field of thinking:
	// "" = Do not send(Replacement); "low"/"medium"/"high"/"xhigh"/"max" = Corresponding strength.
	// OpenAI Map to top-level reasoning_effort.;Anthropic Map to output_config.effort.
	ReasoningEffort string
	// Stream Control whether this profile uses streaming.(SSE)Interface. true(Replacement)= stream;false = True·
	// Non-streaming(Send stream:false,Get the complete JSON once.,Through Provider.Complete). Non-streaming can bypass
	// Some gateways have poor SSE implementation(Empty frame, skip thinking field),The cost is losing real-time progress/real-time updates
	// token Count. Map to agentcore..Options.NonStreaming =!Stream.
	Stream bool
	// MaxTokens This is the upper limit of a single response's output(token). 0 = Do not send this field,Determined by the server's default value
	// (Historical behavior). Different from ContextWindowK:The latter is the model's total capacity,Used locally to calculate compression threshold,
	// Does not appear in requests;This value changes with each request. Mapped to agentcore..Options.MaxTokens.
	MaxTokens int
	// MaxTokensField Choose MaxTokens which request field name to use,Only for format=openai Take effect:
	// "" = max_tokens(Replacement); "max_completion_tokens" = new field.
	// OpenAI Inference Model(o Series/GPT-5)Only recognize the latter,Directly report on max_tokens received
	// unsupported_parameter;While most compatible gateways only recognize the former,Do Not Automatically Infer,Let users select by endpoint
	MaxTokensField string
	// SessionHeaderKey,When Non-Empty,Add a custom HTTP header to each LLM request.,Header Named This Value,
	// Header value as [the current session's session id](chat Session=conv-<id>,worker=exp<x>-worker-i<intent>
	// Equal,See WorkerSessionID.). Gateway used for some session-id header-based prompt caching/sticky routing.
	// empty = Do not send. The value is determined by transcript.WithSessionID Attached to the request context.,Using RoundTripper
	// Read and fill in,Therefore, the same shared provider can issue different header values per session.
	SessionHeaderKey string
	// Retry These are retry parameters after configuration parsing(profile Coverage → Global policy → built-in default,by
	// server Side parsing). The meaning of three layers can be found in RetryConfig;zero value = Fully use built-in defaults.
	Retry RetryConfig
}

// RetryConfig It is a retry parameter that travels with an LLM configuration. The 'number' at each level has a unified semantic meaning.:
// 0 = Use built-in default count.;negative = Close retry at this layer;>0 = Use this value. The 'interval' at each level:
// 0 = Use the original index for backoff at this layer.;>0 = Switch to this fixed interval.
type RetryConfig struct {
	// ConnectAttempts/ConnectInterval:SDK Reconnect Retry(Connection reset/timeout/429/5xx,Before traffic starts),
	// Directly map to llm.Config.MaxRetries / RetryInterval. Default 3 times, 0.5s Start index(Cap at 8s).
	ConnectAttempts int
	ConnectInterval time.Duration
	// EmptyAttempts/EmptyInterval:SDK Retry on empty response(Completed but no content block,Only openai
	// Format),Map to llm.Config.EmptyResponseRetries / EmptyResponseInterval.
	// Default 2 times, same index gradient.
	EmptyAttempts int
	EmptyInterval time.Duration
	// StreamAttempts/StreamInterval:Retry with the same provider security window——This project supplements the SDK.
	// One layer,Only replay interrupted/overloaded/stream 429 when 'no output has been delivered to the caller' yet. SDK cannot see it,
	// From server/task_llm.go Consume. Default 2 times, 0.5s Start index(Cap at 4s).
	StreamAttempts int
	StreamInterval time.Duration
}

// compaction window resolution bounds (in K tokens). Below the floor the
// threshold math (window − summary reserve − buffer) would go non-positive and
// compaction would fire every turn; above the cap it would never fire.
const (
	defaultWindowK = 200 // unset → assume a 200K window (Claude default)
	minWindowK = 32 // floor so effectiveWindow stays comfortably positive
	maxWindowK = 1000 // cap at 1M tokens (user request)
)

// CompactionWindow returns the model context window in TOKENS for compaction
// thresholds, resolved from the user-configured size (ContextWindowK). 0/unset →
// a 200K default; otherwise clamped to [32K, 1M] so compaction stays effective.
func (c Config) CompactionWindow() int {
	k:= c.ContextWindowK
	if k <= 0 {
		k = defaultWindowK
	}
	if k < minWindowK {
		k = minWindowK
	}
	if k > maxWindowK {
		k = maxWindowK
	}
	return k * 1000
}

// compactionConfig builds the agent-core compaction config for a context window
// in tokens. agentcore.NewSession wires the summarizer (same provider) when this
// is set on Options.Compaction.
func compactionConfig(windowTokens int) *compaction.Config {
	if windowTokens <= 0 {
		windowTokens = defaultWindowK * 1000
	}
	return &compaction.Config{ContextWindow: windowTokens}
}

// FromEnv resolves the LLM provider config:
//
//	ARTEX_LLM_PROVIDER = anthropic|openai (default: inferred from keys)
//	ARTEX_LLM_MODEL = model id (default: per provider)
//	ARTEX_LLM_BASE_URL = endpoint (optional)
//	ARTEX_LLM_PROXY = proxy URL (optional; http/https/socks5)
//	ANTHROPIC_API_KEY / OPENAI_API_KEY = credentials
func FromEnv() (Config, bool) {
	prov:= os.Getenv("ARTEX_LLM_PROVIDER")
	anthKey:= os.Getenv("ANTHROPIC_API_KEY")
	oaiKey:= os.Getenv("OPENAI_API_KEY")

	if prov == "" {
		switch {
		case anthKey!= "":
			prov = "anthropic"
		case oaiKey!= "":
			prov = "openai"
		default:
			return Config{}, false
		}
	}

	c:= Config{
		BaseURL: os.Getenv("ARTEX_LLM_BASE_URL"),
		Model: os.Getenv("ARTEX_LLM_MODEL"),
		Proxy: strings.TrimSpace(os.Getenv("ARTEX_LLM_PROXY")),
		// Default stream;ARTEX_LLM_STREAM=false/0/off Explicitly close non-streaming tools
		Stream:!isFalsy(os.Getenv("ARTEX_LLM_STREAM")),
	}
	switch prov {
	case "openai":
		c.Format = llm.FormatOpenAI
		c.APIKey = oaiKey
		if c.Model == "" {
			c.Model = "gpt-4o"
		}
	case "openai-responses":
		c.Format = llm.FormatOpenAIResponses
		c.APIKey = oaiKey
		if c.Model == "" {
			c.Model = "gpt-5"
		}
	default:
		c.Format = llm.FormatAnthropic
		c.APIKey = anthKey
		if c.Model == "" {
			c.Model = "claude-opus-4-8"
		}
	}
	if c.APIKey == "" {
		return Config{}, false
	}
	return c, true
}

// ConfigFrom builds a Config from UI-provided strings (provider defaults to
// anthropic; model defaults per provider). Inputs are trimmed and the base URL
// is normalized to the API base the provider expects (the provider appends the
// endpoint path itself), so a full endpoint URL is tolerated.
func ConfigFrom(provider, model, baseURL, apiKey, proxy string) Config {
	c:= Config{
		Model: strings.TrimSpace(model),
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIKey: strings.TrimSpace(apiKey),
		Proxy: strings.TrimSpace(proxy),
		Stream: true, // Default stream;The caller overrides according to the profile
	}
	switch strings.TrimSpace(provider) {
	case "openai":
		c.Format = llm.FormatOpenAI
		// provider appends "/chat/completions"; tolerate a full endpoint URL.
		c.BaseURL = strings.TrimRight(strings.TrimSuffix(c.BaseURL, "/chat/completions"), "/")
		if c.Model == "" {
			c.Model = "gpt-4o"
		}
	case "openai-responses":
		c.Format = llm.FormatOpenAIResponses
		// provider appends "/responses"; tolerate a full endpoint URL.
		c.BaseURL = strings.TrimRight(strings.TrimSuffix(c.BaseURL, "/responses"), "/")
		if c.Model == "" {
			c.Model = "gpt-5"
		}
	default:
		c.Format = llm.FormatAnthropic
		// provider appends "/v1/messages".
		c.BaseURL = strings.TrimRight(strings.TrimSuffix(c.BaseURL, "/v1/messages"), "/")
		if c.Model == "" {
			c.Model = "claude-opus-4-8"
		}
	}
	return c
}

// isFalsy reports whether an env-var string explicitly requests "off". Empty or
// unrecognized → false (so an unset var keeps the streaming default).
func isFalsy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "0", "false", "off", "no":
		return true
	}
	return false
}

// Provider returns the short provider name ("anthropic"/"openai").
func (c Config) Provider() string {
	switch c.Format {
	case llm.FormatOpenAI:
		return "openai"
	case llm.FormatOpenAIResponses:
		return "openai-responses"
	}
	return "anthropic"
}

// NewProvider builds an llm.Provider from the config. When a rate is set, the
// limiter lives on the single provider instance — so planner + all workers +
// main agent (which share this provider) are bounded by one shared rate limit.
func (c Config) NewProvider() (llm.Provider, error) {
	client, err:= quotaAwareHTTPClient(c.Proxy, c.SessionHeaderKey)
	if err!= nil {
		return nil, err
	}
	lc:= llm.Config{
		Format: c.Format,
		BaseURL: c.BaseURL,
		APIKey: c.APIKey,
		Model: c.Model,
		HTTPClient: client,
	}
	// The 'Thinking Switch' and 'Intensity' fields should each be transparently passed through.(empty = This field is not sent). decoupled:
	// Can only send thinking.type, Send only effort, all, or none.
	lc.ThinkingType = c.ThinkingType
	lc.ReasoningEffort = c.ReasoningEffort
	// Field name selection for the output limit(empty = Use max_tokens). The 'value' limit is not here:It Retries Every Round
	// agentcore.Options.MaxTokens walk,provider Only decides which key to put it into.
	lc.MaxTokensField = c.MaxTokensField
	// Retry parameters with SDK semantics(Count 0=Default/negative=Close,interval 0=Exponential Backoff/>0=Fixed),Transparently pass through.
	lc.MaxRetries = c.Retry.ConnectAttempts
	lc.RetryInterval = c.Retry.ConnectInterval
	lc.EmptyResponseRetries = c.Retry.EmptyAttempts
	lc.EmptyResponseInterval = c.Retry.EmptyInterval
	if c.RatePerSecond > 0 || c.RatePerMinute > 0 {
		lc.RateLimit = &llm.RateLimit{PerSecond: c.RatePerSecond, PerMinute: c.RatePerMinute}
	}
	return llm.NewProvider(lc)
}

// IsQuotaExhaustedMessage deliberately recognizes only explicit balance,
// billing, credit, or quota-exhaustion signals. Generic 429/rate-limit text,
// authentication failures, network errors, and server failures are excluded.
var nonFailoverHTTPStatus = regexp.MustCompile(`(?:status(?:\s+code)?|http(?:\s+status)?)\s*[=:]?\s*(?:401|403|5\d\d)\b`)
var transientQuotaLimit = regexp.MustCompile(`(?i)(?:\b(?:rpm|tpm|rpd|qps)\b|quota[_\s-]*metric|rate[_\s-]*limit|too many requests|(?:requests?|tokens?)\s+(?:per|/)\s*(?:second|minute)|(?:per|/)\s*(?:second|minute)\s+(?:requests?|tokens?)|generate[_\s-]*requests[_\s-]*per[_\s-]*(?:minute|second)|tokens?[_\s-]*per[_\s-]*(?:minute|second))`)

func IsQuotaExhaustedMessage(message string) bool {
	message = strings.ToLower(message)
	// Authentication/authorization and provider-side 5xx failures never rotate,
	// even when a gateway happens to echo a quota-looking phrase in the body.
	if nonFailoverHTTPStatus.MatchString(message) {
		return false
	}
	// Provider APIs frequently describe an ordinary rate limit as "quota
	// exceeded", especially Google-style responses containing a quota metric.
	// These limits recover with time and must stay on the current provider.
	if transientQuotaLimit.MatchString(message) {
		return false
	}
	markers:= []string{
		"insufficient_quota", "quota_exceeded", "quota exceeded", "quota exhausted",
		"exceeded your current quota", "billing_hard_limit_reached",
		"billing hard limit", "billing_not_active", "credit balance", "insufficient credit",
		"insufficient balance", "balance is too low", "payment required", "status 402",
		"\u4f59\u989d\u4e0d\u8db3", "\u989d\u5ea6\u4e0d\u8db3", "\u989d\u5ea6\u5df2\u7528\u5c3d", "\u6b20\u8d39",
	}
	for _, marker:= range markers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	// gRPC RESOURCE_EXHAUSTED is overloaded for both account quota and ordinary
	// request-rate limiting. Preserve it as an explicit exhaustion signal only
	// when the same error does not identify a transient rate limit.
	return strings.Contains(message, "resource_exhausted") &&
		!strings.Contains(message, "rate limit") &&
		!strings.Contains(message, "too many requests")
}

// quotaAwareTransport preserves Norma's normal retry behavior except for a 429
// whose body explicitly says the account quota/balance is exhausted. Norma's
// retry loop treats every 429 as transient; normalizing only that response to
// 402 lets a task router fail over immediately while retaining the original
// response body for provider-specific classification and audit logs.
type quotaAwareTransport struct {
	base http.RoundTripper
	// sessionHeaderKey, when non-empty, is the HTTP header name each request
	// carries; its value is the session id read from the request context. Empty
	// disables it. See Config.SessionHeaderKey.
	sessionHeaderKey string
}

func (t quotaAwareTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Custom session-id header: name is user-configured, value is THIS run's
	// session id (norma stashes it on the context via transcript.WithSessionID).
	// Stable across a session's turns and distinct across sessions — exactly what
	// a session-keyed prompt cache wants. Skipped when no session id is present.
	if t.sessionHeaderKey!= "" {
		if sid:= transcript.SessionIDFrom(req.Context()); sid!= "" {
			req.Header.Set(t.sessionHeaderKey, sid)
		}
	}
	// When LLM recording is on, the Recorder puts a Capture on the context so the
	// raw wire bodies can be persisted. This is the only layer that still sees
	// them: norma builds the request body internally and decodes the SSE response
	// before either reaches the recorder.
	capt:= llmrec.CaptureFrom(req.Context())
	capt.SetRequest(requestBodySnapshot(req))

	resp, err:= t.base.RoundTrip(req)
	if err!= nil || resp == nil {
		return resp, err
	}
	// Tee rather than read: a 200 is an SSE stream that must keep streaming. The
	// 429 branch below reads through this wrapper, so its body lands in the
	// capture before being replaced.
	resp.Body = capt.TeeResponse(resp.StatusCode, resp.Body)

	if resp.StatusCode!= http.StatusTooManyRequests {
		return resp, nil
	}
	body, readErr:= io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	if readErr!= nil {
		return resp, nil
	}
	if IsQuotaExhaustedMessage(string(body)) {
		resp.StatusCode = http.StatusPaymentRequired
		resp.Status = "402 Payment Required"
	}
	return resp, nil
}

// requestBodySnapshot copies an outgoing request body without consuming it.
// norma builds every model request from a *bytes.Reader, so net/http populates
// GetBody and the copy has no effect on what gets sent.
func requestBodySnapshot(req *http.Request) string {
	if req.GetBody == nil {
		return ""
	}
	rc, err:= req.GetBody()
	if err!= nil {
		return ""
	}
	defer rc.Close()
	b, err:= io.ReadAll(rc)
	if err!= nil {
		return ""
	}
	return string(b)
}

func quotaAwareHTTPClient(proxy, sessionHeaderKey string) (*http.Client, error) {
	transport:= http.DefaultTransport.(*http.Transport).Clone()
	proxy = strings.TrimSpace(proxy)
	if proxy == "" {
		transport.Proxy = nil // leave empty=Direct connection,Do not revert HTTP_PROXY/HTTPS_PROXY environment variables.
	} else {
		proxyURL, err:= url.Parse(proxy)
		if err!= nil {
			return nil, fmt.Errorf("llm: invalid proxy %q: %w", proxy, err)
		}
		switch proxyURL.Scheme {
		case "http", "https", "socks5":
		case "":
			return nil, fmt.Errorf("llm: proxy %q missing scheme (use http://, https:// or socks5://)", proxy)
		default:
			return nil, fmt.Errorf("llm: unsupported proxy scheme %q (use http, https or socks5)", proxyURL.Scheme)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return &http.Client{Transport: quotaAwareTransport{base: transport, sessionHeaderKey: strings.TrimSpace(sessionHeaderKey)}}, nil
}

// logTestConnection prints the raw HTTP status code(s) and response body of a
// connection test to the server log, so "Click to test" leaves a diagnosable trail of
// exactly what the gateway returned — 401 bodies, quota text, empty frames — not
// just the collapsed ok/err the UI shows. Bodies are clipped to keep a chatty
// SSE stream from flooding the log.
func logTestConnection(c Config, capt *llmrec.Capture) {
	attempts:= capt.Attempts()
	if len(attempts) == 0 {
		log.Printf("[llm-test] %s / %s @ %s — No HTTP requests made.(Configuration parsing or connection setup fails)",
			c.Provider(), c.Model, c.BaseURL)
		return
	}
	for i, a:= range attempts {
		log.Printf("[llm-test] %s / %s @ %s — Try %d/%d HTTP %d\nResponse body: %s",
			c.Provider(), c.Model, c.BaseURL, i+1, len(attempts), a.Status, clipBody(a.Body))
	}
}

// clipBody trims a wire body for logging. 4K is plenty to show an error JSON or
// the head of an SSE stream while bounding a runaway response.
func clipBody(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(empty)"
	}
	const max = 4096
	if len(s) > max {
		return s[:max] + fmt.Sprintf("…(truncate,Total %d bytes)", len(s))
	}
	return s
}

// TestConnection makes a minimal real completion to verify the provider/model/
// endpoint/key actually work. Returns the round-trip latency and the model's
// reply text.
func TestConnection(ctx context.Context, c Config) (time.Duration, string, error) {
	prov, err:= c.NewProvider()
	if err!= nil {
		return 0, "", err
	}
	ctx, cancel:= context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Capture original wire packets:When conducting a connection test, what the gateway returns is most important to see.(Status Code+Response body),
	// norma decodes the response into a StreamEvent, after which these are gone. quotaAwareTransport will
	// context Find this Capture and fill in the status code and body of each HTTP attempt.
	ctx, capt:= llmrec.NewCapture(ctx)
	defer logTestConnection(c, capt)
	// Connection test is a one-way path,Session loop without going through agentcore,Therefore, no one adds to the context
	// session id. For endpoints configured with SessionHeaderKey.(Like opencode zen enforcing requirements.
	// x-opencode-session Head,Missing directly 400 MissingSessionID),this will cause"Conversation normal,
	// Click test but got 400."The gap. Here we append a one-time random session id,Let the test and reality converse together.
	// A set of header logic;Endpoints not using SessionHeaderKey don't read it,No side effects.
	ctx = transcript.WithSessionID(ctx, "conntest-"+transcript.NewSessionID())
	start:= time.Now()
	// MaxTokens Provide sufficient resources for: inference model(Like deepseek-v4-pro)Before giving an answer, produce a large segment of
	// Think(Tested on a single sentence. "ping" Can Also Burn ~2900 token). If Only Give 32,The model will always get stuck at"Thinking Phase"
	// Just Hit Output Limit(finish=length), Truncated,The connection test is still considered successful.(err=nil)But displayed as
	// "Interrupted/length/resume" A mess. Give it a budget to let it spit out OK cleanly(finish=stop).
	// EscalateMaxTokens Keep false:Do not retry due to truncation,Prevent resume loop from burning CPU cycles.
	reply, err:= agentcore.Run(ctx, agentcore.Options{
		Provider: prov,
		SystemPrompt: []string{"You are performing a connection test. Simply output two characters 'OK' without thinking, explaining, or adding anything else."},
		PermissionMode: acperm.ModeBypass,
		MaxTurns: 1,
		MaxTokens: 8192,
		NonStreaming:!c.Stream, // Use the true send/receive mode of this profile for connection testing
	}, "ping")
	lat:= time.Since(start)
	if err!= nil {
		return lat, "", err
	}
	// err==nil Still not enough: situations where requests go through but the model doesn't output anything exist.(Think about burning the budget out,
	// The main content is swallowed by security policies, and the compatibility layer loses the content.). This configuration is in the session as"No session",
	// Test reports success——precisely addresses this gap. Without visible text, all are judged as failures.
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return lat, "", fmt.Errorf("Model has no response content (request is through but no text returned)")
	}
	return lat, reply, nil
}
