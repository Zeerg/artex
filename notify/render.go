package notify

import (
	"strings"
	"unicode/utf8"
)

const ellipsis = "…"

// TruncateBytes Truncate s to no more than max bytes, ensuring the result is valid UTF-8 and does not split characters.
//
// Why must it be cut by character boundary? Qwen's WeChat group robot markdown has a limit of 4096 characters. **bytes**Hard limit (not
// Character count), and Chinese characters are 3 bytes. Directly slicing by byte will cut a character in half, producing an illegal
// UTF-8——The platform either rejects the entire message or displays it as a garbled block. Here, the approach is to first deduct from the budget position
// Roll back to the nearest rune starting byte (utf8.RuneStart Determining continuation bytes 0b10xxxxxx).
//
// max<=0 means not limiting it. Append an ellipsis after truncation, unless max is too small to fit the ellipsis.
func TruncateBytes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	budget:= max - len(ellipsis)
	suffix:= ellipsis
	if budget < 0 {
		// max Shorter than an ellipsis: Give up on ellipsis, pure truncation, to avoid the result exceeding max.
		budget = max
		suffix = ""
	}
	cut:= budget
	for cut > 0 &&!utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + suffix
}

// OneLine Compress multi-line text into one line: Collapse all whitespace, then truncate by character count.
// Title line for IM messages.——Summaries often have line breaks, which can mess up formatting if directly inserted into tables/headers.
// max<=0 Indicates no length restriction.
func OneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	return TruncateRunes(s, max)
}

// TruncateRunes Truncate s to no more than max characters (not bytes), appending an ellipsis if exceeded.
// max<=0 Indicates no limit.
//
// The difference from TruncateBytes is in the platform's criteria: WeChat limits by byte count, Telegram by character count.
// Using the wrong scope does not throw an error; it only makes the message shorter than expected (Chinese 1 character = 3 Byte,
// Slice by byte 4096 leaves only about 1365 bytes), so both functions must be retained and selected by channel.
func TruncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	runes:= []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 1 {
		return string(runes[:max])
	}
	return string(runes[:max-1]) + ellipsis
}

// TruncateHTML Truncate HTML snippet by character count and ensure no incomplete tags are produced.
//
// Directly truncating HTML characters would cut out `<a href="htt` This incomplete tag, either
// Error rejects entire message, either swallowing subsequent text as attribute values. This approach is to: first truncate by character,
// Check if there are any unclosed `<`, If present, go back to it before.
//
// Do not perform label alignment (complete). </b>): Telegram's HTML parser will automatically close unclosed tags,
// while implementing balancing requires handling quotes, comments, self-closing tags in attributes, which is not worth the complexity and return on investment.
func TruncateHTML(s string, max int) string {
	if max <= 0 || len([]rune(s)) <= max {
		return s
	}
	cut:= TruncateRunes(s, max)
	// If at the End, `<` Leading fragment (last to appear `<` No More After, `>`), Usage `<` Before.
	if lt:= strings.LastIndex(cut, "<"); lt >= 0 &&!strings.Contains(cut[lt:], ">") {
		cut = cut[:lt]
	}
	// If the tail is a truncated HTML entity (like `&amp;` Cut into `&amp`), Must go back.
	// An entity fragment might cause issues in a parser that only recognizes entities.**Entire message**Rejected——one exceeds
	// Summary messages with length limits are common enough; it's not worth losing the entire notification for this.
	if amp:= strings.LastIndex(cut, "&"); amp >= 0 &&!strings.Contains(cut[amp:], ";") {
		cut = cut[:amp]
	}
	return cut
}

// packItemCount Can calculate within the budget**Complete**How many to release, for summary messages to be bundled as whole items.
//
// Why render the entire paragraph before truncating? Truncation would make the second half of the item disappear out of thin air,
// And their delivery records are still marked as delivered——The message doesn't show it, nor does the delivery history.
// The vulnerability is gone. Pack it up and entries that don't fit stay in the library for the next batch,
// The kept value the caller gets is the actual number of messages this one was delivered to.
//
// Parameter: maxSize<=0 Indicates no restriction; reserve is for reserving space in the message header/tail;
// size Responsible for counting (different platforms have different metrics: WeChat/DingTalk by bytes, Telegram by character count——
// Using the wrongmeasure will not cause an error; it will just make Chinese messages compressed to far below the limit).
// render Render the idx-th item as its actual text.——Length varies by content, cannot be estimated.
//
// Must return at least 1 (as long as there are entries). Even a single extremely long one should be sent by this one, to be handled by the caller's
// Final truncation fallback, otherwise a very long vulnerability would permanently block the entire batch at the original location.
func packItemCount(items []Item, maxSize, reserve int, footer string, size func(string) int, render func(Item, int) string) int {
	if maxSize <= 0 {
		return len(items)
	}
	budget:= maxSize - reserve - size(footer)
	if budget < 0 {
		budget = 0
	}
	used:= 0
	for i, it:= range items {
		used += size(render(it, i))
		if used > budget && i > 0 {
			return i
		}
	}
	return len(items)
}

// byteSize / runeSize These are two different ways of measuring packItemCount, named to avoid confusion at call sites
// Naked func encountered(s string) int Closure, otherwise it's hard to immediately see what caliber is being used.
func byteSize(s string) int { return len(s) }
func runeSize(s string) int { return utf8.RuneCountInString(s) }

// assetLine Render the asset list as a single line of display text, omitting the rest when there are more than limit items and annotating the total count.
// A vulnerability may anchor dozens of assets; listing them all will overload the message.
func assetLine(assets []string, limit int) string {
	if len(assets) == 0 {
		return ""
	}
	if limit <= 0 || len(assets) <= limit {
		return strings.Join(assets, ", ")
	}
	return strings.Join(assets[:limit], ", ") + " … " + itoa(len(assets)) + " in total"
}

// itoa strconv.Itoa short alias for use in text concatenation, avoiding unnecessary import of strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg:= n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i:= len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
