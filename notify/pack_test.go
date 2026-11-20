package notify

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// This file covers the 'bundle by whole' fix: when the batch message exceeds channel length limits,**By line**
// Truncate and report the number of entries not installed truthfully, allowing the caller to only mark those actually delivered.
//
// The previous approach was to render the entire document and then truncate it, marking everything as delivered in batches: the second half of the message would vanish out of thin air,
// And delivery history shows all successful——The vulnerability is gone and there's nowhere to find it anymore.

func TestMarkdownBodyPacksWholeItemsWithinByteLimit(t *testing.T) {
	// 200 A summary in Chinese must exceed WeChat Work's 4096-byte limit.
	m:= batchMsg(200)
	body, kept:= markdownBody(m, weComMarkdownLimit)

	if len(body) > weComMarkdownLimit {
		t.Fatalf("Content %d Byte exceeds limit %d", len(body), weComMarkdownLimit)
	}
	if!utf8.ValidString(body) {
		t.Fatal("The body is not valid UTF-8")
	}
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("Should only install a part (0 < kept < %d), Got %d", len(m.Items), kept)
	}
	// The header must truthfully state how many items it contains and how many more there are——Otherwise, readers will treat the header as
	// Treat that number as all.
	if!strings.Contains(body, "the rest") ||!strings.Contains(body, "continue in the next message") {
		t.Fatalf("The header should indicate how many are not included in this one.:\n%s", body[:minInt(400, len(body))])
	}
	// Should only contain the first kept items.
	for i:= 0; i < kept; i++ {
		if!strings.Contains(body, "Vulnerability"+itoa(i+1)) {
			t.Fatalf("Intercept %d The item should be in this message:\n%s", i+1, body)
		}
	}
	if strings.Contains(body, "Vulnerability"+itoa(kept+1)) {
		t.Fatalf("Intercept %d Should not appear (it belongs to the next batch)", kept+1)
	}
}

func TestMarkdownBodyKeepsEverythingWhenUnderLimit(t *testing.T) {
	m:= batchMsg(3)
	body, kept:= markdownBody(m, 0) // 0 = No limit
	if kept!= len(m.Items) {
		t.Fatalf("When no length restriction is applied, all should be retained. Result: kept.=%d", kept)
	}
	if strings.Contains(body, "The rest") {
		t.Fatalf("No truncation prompt should appear when not truncated.:\n%s", body)
	}
}

func TestMarkdownBodyAlwaysKeepsAtLeastOneItem(t *testing.T) {
	// 'Even when the budget is too small to fit a single item, one should still be sent (final truncation as fallback).',
	// Otherwise, a long-running vulnerability would permanently stall the batch: each time it's picked up, it won't fit, and nothing will be sent.
	m:= batchMsg(5)
	_, kept:= markdownBody(m, 50)
	if kept!= 1 {
		t.Fatalf("At least one should be retained, resulting in %d", kept)
	}
}

func TestMarkdownBodySingleReturnsOne(t *testing.T) {
	_, kept:= markdownBody(singleMsg(), 4096)
	if kept!= 1 {
		t.Fatalf("A single message should report delivery of 1, resulting in %d", kept)
	}
	// An empty message has no deliverable entries.
	if _, k:= markdownBody(Message{}, 4096); k!= 0 {
		t.Fatalf("An empty message should report 0 items, got %d", k)
	}
}

func TestTelegramPackingUsesRuneBudget(t *testing.T) {
	m:= batchMsg(200)
	text, kept:= telegramHTML(m)
	// Telegram Sort**Character count**Limited length; using byte units will compress Chinese messages to one-third.
	if n:= utf8.RuneCountInString(text); n > telegramTextLimit {
		t.Fatalf("Content %d Character exceeds limit %d", n, telegramTextLimit)
	}
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("Should only install a part, got %d", kept)
	}
	if!strings.Contains(text, "continue in the next message") {
		t.Fatalf("It should be noted that there is still unused capacity:\n%.300s", text)
	}
}

func TestFeishuPackingReportsKept(t *testing.T) {
	m:= batchMsg(2000)
	_, kept:= feishuCard(m)
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("The card should only fit part of it, got %d", kept)
	}
}

func TestWebhookAndEmailReportAllItems(t *testing.T) {
	// These two channels do not truncate the body; the entire batch is considered delivered.
	m:= batchMsg(7)
	if n:= len(m.Items); n!= 7 {
		t.Fatal("Prerequisites Do Not Hold")
	}
	// Indirectly confirm through renderer's return value: markdownBody(0) Retain all when not limited.
	if _, k:= markdownBody(m, 0); k!= len(m.Items) {
		t.Fatalf("Applies all when no length restriction, received %d", k)
	}
}

// TestMarkdownEscapesUntrustedContent A regression test for 'untrusted content must not change message structure'.
// Title and summary come from model output (model reads the response of the target being tested), asset name comes from the URL of the target being tested.
func TestMarkdownEscapesUntrustedContent(t *testing.T) {
	cases:= []struct {
		name string
		item Item
		must []string // Must appear in the result (escaped form)
		wrong []string // No unescaped forms should appear in the result
	}{
		{
			name: "Line break in title + External Link",
			item: Item{
				Severity: "high",
				Name: "Login SQL injection\n[Urgent: Click here to verify account](http://attacker.tld)",
			},
			// Line breaks must be collapsed (otherwise new list items/references can be forged);
			// Square brackets and parentheses must be escaped (otherwise, they are clickable external links).
			must: []string{`\[Urgent: Click here to verify account\]`, `\(http://attacker.tld\)`},
			wrong: []string{"\n[Urgent", "\n\n[Urgent"},
		},
		{
			name: "Image beacon in title",
			item: Item{
				Severity: "high",
				Name: "Vulnerability![](http://attacker.tld/beacon)",
			},
			must: []string{`\!`, `\(http://attacker.tld/beacon\)`},
			wrong: []string{"![]("},
		},
		{
			name: "Emphasis and references in asset names.",
			item: Item{
				Severity: "high",
				Name: "Ordinary Title",
				Assets: []string{"a.com/*Completed*>Reference"},
			},
			must: []string{`\*Completed\*`, `\>`},
			wrong: []string{"*Completed*"},
		},
		{
			name: "Backticks and vertical bars in the summary",
			item: Item{
				Severity: "high",
				Name: "Title",
				Summary: "`code` | Table",
			},
			must: []string{"\\`code\\`", `\|`},
			wrong: []string{"`code`"},
		},
	}
	for _, tc:= range cases {
		t.Run(tc.name, func(t *testing.T) {
			m:= Message{Items: []Item{tc.item}}
			// The rendering path for a single-item mode is shared by three markdown channels.
			var b strings.Builder
			writeItem(&b, tc.item, "", true)
			got:= b.String()
			for _, want:= range tc.must {
				if!strings.Contains(got, want) {
					t.Errorf("Missing escape form %q:\n%s", want, got)
				}
			}
			for _, bad:= range tc.wrong {
				if strings.Contains(got, bad) {
					t.Errorf("An unescaped form appeared %q (Can be used to inject structures or external links):\n%s", bad, got)
				}
			}
			_ = m
		})
	}
}

// TestMarkdownEscapeBackslashFirst Lock escape order: backslashes must be processed first,
// Otherwise, an additional backslash will be added when appending, resulting in double backslashes in the output.
func TestMarkdownEscapeBackslashFirst(t *testing.T) {
	if got:= markdownEscape(`a\b*c`); got!= `a\\b\*c` {
		t.Fatalf("The escape order is incorrect, resulting in %q", got)
	}
}

// TestTelegramTitleHasNoMarkdownEscapes Lock a specific regression:
// markdown Escaping should not leak into Telegram's HTML output (once in the shared title function
// Escaped, resulting in `\(1\)` This visible backslash).
func TestTelegramTitleHasNoMarkdownEscapes(t *testing.T) {
	m:= Message{Items: []Item{{Severity: "high", Name: "alert(1) *Key*"}}}
	text, _:= telegramHTML(m)
	if strings.Contains(text, `\(`) || strings.Contains(text, `\*`) {
		t.Fatalf("Telegram Markdown backslash escaping appeared in the body text.:\n%s", text)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
