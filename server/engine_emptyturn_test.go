package server

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

// Idle Round(Only thinking, no body, no tools)The recognition and continuation of running can be seen in steerHooks..Stop.

func assistantThinking(text string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
		{Type: llm.BlockThinking, Thinking: text, Signature: "sig"},
	}}
}

func TestIsThinkingOnlyTurn(t *testing.T) {
	toolUse:= llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
		{Type: llm.BlockThinking, Thinking: "first scan port"},
		{Type: llm.BlockToolUse, ID: "t1", Name: "run_nuclei"},
	}}
	cases:= []struct {
		name string
		msgs []llm.Message
		want bool
	}{
		{"Just think", []llm.Message{llm.UserText("Start"), assistantThinking("Think about it")}, true},
		{"Think+Chat", []llm.Message{llm.UserText("Start"), toolUse}, false},
		{"Think+Content", []llm.Message{assistantThinking("Think about it"), {
			Role: llm.RoleAssistant,
			Content: []llm.ContentBlock{{Type: llm.BlockThinking, Thinking: "x"}, llm.TextBlock("Conclusion")},
		}}, false},
		{"The body contains only whitespace characters", []llm.Message{{
			Role: llm.RoleAssistant,
			Content: []llm.ContentBlock{{Type: llm.BlockThinking, Thinking: "x"}, llm.TextBlock("  \n ")},
		}}, true},
		{"Completely empty assistant round", []llm.Message{{Role: llm.RoleAssistant}}, true},
		// The tool result is the user role. Determination must backtrack to the preceding assistant instead of misjudging nearby.
		{"The last one is the tool result", []llm.Message{toolUse, {
			Role: llm.RoleUser,
			Content: []llm.ContentBlock{{Type: llm.BlockToolResult, ToolUseID: "t1"}},
		}}, false},
		{"No assistant message", []llm.Message{llm.UserText("Start")}, false},
		{"Empty history", nil, false},
	}
	for _, c:= range cases {
		t.Run(c.name, func(t *testing.T) {
			if got:= isThinkingOnlyTurn(c.msgs); got!= c.want {
				t.Fatalf("isThinkingOnlyTurn = %v, want %v", got, c.want)
			}
		})
	}
}

// fakeHooks A programmable inner HookRunner to verify respect for steerHooks by inner decisions.
type fakeHooks struct {
	prevent bool
	blocking []string
	msg string
}

func (f fakeHooks) PreToolUse(context.Context, string, []byte) (bool, string, []byte) {
	return false, "", nil
}
func (f fakeHooks) PostToolUse(context.Context, string, []byte, []byte, bool) {}
func (f fakeHooks) Stop(context.Context, []llm.Message) (bool, []string, string) {
	return f.prevent, f.blocking, f.msg
}

func TestSteerHooksStopNudgesEmptyTurn(t *testing.T) {
	empty:= []llm.Message{assistantThinking("I should first enumerate subdomains")}

	t.Run("idle turn injection continue command", func(t *testing.T) {
		h:= steerHooks{nudges: &atomic.Int64{}, limit: defaultEmptyTurnNudges, label: "worker-1 · #1"}
		prevent, blocking, _:= h.Stop(context.Background(), empty)
		if prevent {
			t.Fatal("Idle turns should not be forcibly stopped")
		}
		if len(blocking)!= 1 || blocking[0]!= emptyTurnNudge {
			t.Fatalf("blocking = %v, want [emptyTurnNudge]", blocking)
		}
	})

	t.Run("Do not intervene when there is a body or tool in use.", func(t *testing.T) {
		h:= steerHooks{nudges: &atomic.Int64{}, limit: defaultEmptyTurnNudges}
		normal:= []llm.Message{{
			Role: llm.RoleAssistant,
			Content: []llm.ContentBlock{llm.TextBlock("Scan completed, no open ports found")},
		}}
		if _, blocking, _:= h.Stop(context.Background(), normal); blocking!= nil {
			t.Fatalf("Normal conclusion mistakenly judged as empty: %v", blocking)
		}
		if n:= h.nudges.Load(); n!= 0 {
			t.Fatalf("Should not count if not intervened, got %d", n)
		}
	})

	t.Run("Release after reaching the limit", func(t *testing.T) {
		const limit = 5 // The user set 'Empty response retry count' to 5.
		h:= steerHooks{nudges: &atomic.Int64{}, limit: limit}
		for i:= 1; i <= limit; i++ {
			if _, blocking, _:= h.Stop(context.Background(), empty); len(blocking)!= 1 {
				t.Fatalf("Intercept %d Should still be within quota, blocking = %v", i, blocking)
			}
		}
		if _, blocking, _:= h.Stop(context.Background(), empty); blocking!= nil {
			t.Fatalf("Exceeds limit but still injecting: %v", blocking)
		}
	})

	// 'Set 'Empty response retry count' to -1. = Turn off this layer, emptyTurnNudgeLimit will parse to 0.
	t.Run("Do not intervene when configuration is closed", func(t *testing.T) {
		h:= steerHooks{nudges: &atomic.Int64{}, limit: 0}
		if _, blocking, _:= h.Stop(context.Background(), empty); blocking!= nil {
			t.Fatalf("Already closed but still injecting: %v", blocking)
		}
	})

	t.Run("inner Decide not todouble-inject when forcibly stopping", func(t *testing.T) {
		h:= steerHooks{inner: fakeHooks{prevent: true, msg: "guard Refuse to end"}, nudges: &atomic.Int64{}, limit: defaultEmptyTurnNudges}
		prevent, blocking, msg:= h.Stop(context.Background(), empty)
		if!prevent || msg!= "guard Refuse to end" || blocking!= nil {
			t.Fatalf("inner The hard stop has been rewritten: prevent=%v blocking=%v msg=%q", prevent, blocking, msg)
		}
		if n:= h.nudges.Load(); n!= 0 {
			t.Fatalf("When yielding to inner, do not consume quota, got %d", n)
		}
	})

	t.Run("inner Do notdouble-inject when resuming", func(t *testing.T) {
		h:= steerHooks{inner: fakeHooks{blocking: []string{"guard Reason for resuming"}}, nudges: &atomic.Int64{}, limit: defaultEmptyTurnNudges}
		_, blocking, _:= h.Stop(context.Background(), empty)
		if len(blocking)!= 1 || blocking[0]!= "guard Reason for resuming" {
			t.Fatalf("inner The continuation message has been rewritten: %v", blocking)
		}
	})

	t.Run("behavior remains unchanged without a counter", func(t *testing.T) {
		h:= steerHooks{limit: defaultEmptyTurnNudges} // For example, future call points might forget to pass nudges
		if _, blocking, _:= h.Stop(context.Background(), empty); blocking!= nil {
			t.Fatalf("Should not inject when there is no counter.: %v", blocking)
		}
	})
}
