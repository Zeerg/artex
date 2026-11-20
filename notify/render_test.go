package notify

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateBytesKeepsValidUTF8(t *testing.T) {
	// This is the most important constant in this package. Enterprise WeChat allows**bytes**Limited length, 3 bytes per character for Chinese.
	// Any byte-hard-cut implementation would cut Chinese characters in half, producing invalid UTF-8 that would be rejected by the platform.
	// Use a mix of English and Chinese inputs with coprime lengths to collide every possible cut point.
	inputs:= []string{
		"\u4e2d\u6587\u6d4b\u8bd5\u5185\u5bb9",
		"\u6df7\u5408 mixed \u5185\u5bb9 content",
		"a\u4e2db\u6587c\u6d4bd\u8bd5e",
		"🔴🟠🟡🔵", // 4 Byte emoji, easier to spot mistakes
		strings.Repeat("\u6f0f\u6d1e", 100),
	}
	for _, in:= range inputs {
		for max:= 1; max <= len(in)+2; max++ {
			got:= TruncateBytes(in, max)
			if!utf8.ValidString(got) {
				t.Fatalf("Input %q max=%d: Produces illegal UTF-8 %q", in, max, got)
			}
			if len(got) > max {
				t.Fatalf("Input %q max=%d: Result %d Byte exceeds limit", in, max, len(got))
			}
			// Content cannot be modified when not truncated.
			if len(in) <= max && got!= in {
				t.Fatalf("Input %q max=%d: Modified content without exceeding limits -> %q", in, max, got)
			}
		}
	}
}

func TestTruncateBytesZeroMeansUnlimited(t *testing.T) {
	long:= strings.Repeat("x", 10000)
	if got:= TruncateBytes(long, 0); got!= long {
		t.Fatal("max=0 Should indicate no limit.")
	}
	if got:= TruncateBytes(long, -5); got!= long {
		t.Fatal("max<0 Should indicate no limit.")
	}
}

func TestTruncateBytesEllipsisBudget(t *testing.T) {
	// max When the ellipsis itself is shorter, it should not exceed the limit because appending an ellipsis would go in reverse.
	got:= TruncateBytes("abcdefgh", 1)
	if len(got) > 1 {
		t.Fatalf("max=1 Result %q Length %d Exceed", got, len(got))
	}
	// Ellipsis should be included in normal cases.
	if got:= TruncateBytes("abcdefgh", 5);!strings.HasSuffix(got, ellipsis) {
		t.Fatalf("Expected to have an ellipsis, but got %q", got)
	}
}

func TestTruncateRunesCountsCharactersNotBytes(t *testing.T) {
	// The difference in scope between TruncateBytes and Telegram's character limit must be retained:
	// Byte-wise handling will cut the Chinese message down to only one-third.
	s:= "One two three four five six seven eight nine ten"
	got:= TruncateRunes(s, 5)
	if n:= utf8.RuneCountInString(got); n!= 5 {
		t.Fatalf("Expected 5 characters, got %d Pages (%q)", n, got)
	}
	// The same string should be noticeably shorter by byte count.
	if utf8.RuneCountInString(TruncateBytes(s, 5)) >= 5 {
		t.Fatal("Bytethe measure shouldn't yield the same character count as the character measure")
	}
}

func TestOneLineCollapsesWhitespace(t *testing.T) {
	got:= OneLine("First line\n\nSecond line\tUse tabs and multiple spaces.", 0)
	if strings.ContainsAny(got, "\n\t") {
		t.Fatalf("Should collapse all blanks to get %q", got)
	}
	if strings.Contains(got, "  ") {
		t.Fatalf("Should not retain consecutive spaces, resulting in %q", got)
	}
	// Truncated must still be readable and legal.
	got = OneLine("One two three four five six seven eight nine ten", 4)
	if n:= utf8.RuneCountInString(got); n!= 4 {
		t.Fatalf("Expecting 4 characters, received %d (%q)", n, got)
	}
}

func TestTruncateHTMLNeverCutsTagInHalf(t *testing.T) {
	// Directly truncating HTML will cut out `<a href="htt` This fragment will cause the platform to reject the entire message.
	s:= `<b>Title</b>Main text main text main text<a href="https://example.com/very/long/path">view details</a>`
	for max:= 1; max <= utf8.RuneCountInString(s)+2; max++ {
		got:= TruncateHTML(s, max)
		if n:= utf8.RuneCountInString(got); max > 0 && n > max {
			t.Fatalf("max=%d: Result %d Character limit exceeded", max, n)
		}
		// Trailing characters cannot be unclosed `<` (As the last segment appears `<` Append `>`).
		if lt:= strings.LastIndex(got, "<"); lt >= 0 &&!strings.Contains(got[lt:], ">") {
			t.Fatalf("max=%d: Truncated trailing label -> %q", max, got)
		}
	}
}

func TestAssetLineOmitsExcess(t *testing.T) {
	if got:= assetLine(nil, 3); got!= "" {
		t.Fatalf("No assets should return an empty string, received. %q", got)
	}
	if got:= assetLine([]string{"a", "b"}, 3); got!= "a, b" {
		t.Fatalf("If not exceeded, list all, resulting in %q", got)
	}
	// Must annotate total count when exceeding limit; otherwise, readers don't know how many assets are still missing.
	got:= assetLine([]string{"a", "b", "c", "d", "e"}, 2)
	if!strings.Contains(got, "5 in total") {
		t.Fatalf("Should annotate total count as 5, got %q", got)
	}
}

func TestSeverityAndStatusLabels(t *testing.T) {
	if AtLeast("", "low") {
		t.Fatal("Empty level sequence number is 0, should be blocked by any threshold")
	}
	if!AtLeast("critical", "") {
		t.Fatal("Empty threshold should be allowed")
	}
	if got:= StatusLabel("fixed"); got!= "Fixed" {
		t.Fatalf("Unknown status mapping, got %q", got)
	}
	// Unknown status echoed as is, no fabricated labels.
	if got:= StatusLabel("weird_status"); got!= "weird_status" {
		t.Fatalf("Unknown state should be echoed back as is, got %q", got)
	}
}

// TestTruncateHTMLNeverCutsEntity Audit coverage points out a missing item: truncation must avoid
// Half-tagged, also avoiding broken HTML entities.
//
// `&amp;` Cut into `&amp` After that, a parser that only recognizes entities may reject it**Entire**Message——
// And long summary messages are common, the cost is too high.
func TestTruncateHTMLNeverCutsEntity(t *testing.T) {
	s:= "aaaa&amp;bbbb&lt;cccc&quot;dddd"
	for max:= 1; max <= utf8.RuneCountInString(s)+2; max++ {
		got:= TruncateHTML(s, max)
		// No 'have' at the end & But there is no corresponding;'Entity fragment.
		if amp:= strings.LastIndex(got, "&"); amp >= 0 &&!strings.Contains(got[amp:], ";") {
			t.Fatalf("max=%d: Left behind entity fragments at the end %q", max, got[amp:])
		}
		if strings.Contains(got, "&amp\x00") {
			t.Fatalf("max=%d: Abnormal entity encountered.", max)
		}
	}
}
