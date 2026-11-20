package notify

import (
	"fmt"
	"strings"
)

// This file is the 'Markdown series' channel (DingTalk, WeChat Enterprise) shared message rendering.
// Feishu uses card JSON, Telegram uses HTML, email uses HTML; each renders in its adapter.

// maxAssetsShown How many assets are listed in a message. A vulnerability may anchor dozens of assets,
// The entire column will be flooded with messages and lack information value.——No one would look at domain names after the fourth in IM.
const maxAssetsShown = 3

// maxSummaryRunes How many characters the summary is compressed to. IM messages are 'prompt to see details',
// Not the body of the report, full content is in the platform',
const maxSummaryRunes = 120

// markdownReservedBytes Reserve space for messages (summary row). + Level distribution + Possible truncated prompt)
// Attach to the tail (platform link). When packaging the entire thing, deduct this part from the budget to ensure that neither the head nor the tail is truncated.——
// Once the head and tail are truncated, readers won't know which batch this is or how many more items are left to display.
const markdownReservedBytes = 320

// markdownEscape Escape markdown metacharacters',
//
// Why must this be done: vulnerability titles, summaries, types, and asset display names all come from**Untrusted source**——
// Title and summary from model output (model reads the target's response), asset URL is scan
// The complete URL (including target-controlled query strings). Without escaping, a title of
//
//	Login SQL injection\n[Urgent: Click here to verify account](http://attacker.tld)
//
// Vulnerabilities rendered as independent Markdown by security engineers on DingTalk/Feishu.**Clickable external link**; and
// `![](http://attacker.tld/beacon)` It will be fetched by the client during rendering, equivalent to informing
// 'This vulnerability has already been seen and leaked the reader's IP. Even if it's non-malicious content, injected bold or
// Code blocks can also push out serious vulnerabilities from the fold line.
//
// Escaping sets cover titles/links/emphasis/lists/citations/strikethroughs which change structure or produce clickable
// Character of the element.`\` Must be processed first; otherwise, it will re-escape the backslash added later.
func markdownEscape(s string) string {
	replacer:= strings.NewReplacer(
		`\`, `\\`,
		"`", "\\`",
		"*", `\*`,
		"_", `\_`,
		"[", `\[`,
		"]", `\]`,
		"(", `\(`,
		")", `\)`,
		"!", `\!`,
		"#", `\#`,
		">", `\>`,
		"|", `\|`,
		"~", `\~`,
	)
	return replacer.Replace(s)
}

// markdownText Compress untrusted text into a single line and escape it for use in markdown body.
// The other half of unescaping: A newline itself can forge a new list item or quote block.
// And escape characters cannot stop it.
func markdownText(s string, maxRunes int) string {
	return markdownEscape(OneLine(s, maxRunes))
}

// markdownTitle Returns the message title (IM platform's title bar/card title), content is**Unescaped original text**.
//
// This title is intentionally not escaped: this title is used by four renderers in different contexts——markdown Body, Telegram's
// HTML, Feishu card's plain_text, and generic Webhook's JSON with email subject. Each context may otherwise result in a 'MergeConfig thinks it should delete, sameConfigValue thinks there is a value' gap.
// Different escape rules (markdown escape into HTML leaves visible backslashes, into JSON pollutes
// Data), so escaping must be handled by each output end, see writeItem / feishuItemLines /
// telegramEscape. Previously added markdown escaping in shared functions resulted in Telegram messages
// Occurred `\(1\)` This visible backslash.
func markdownTitle(m Message) string {
	if m.Batch {
		return fmt.Sprintf("Vulnerability Summary · Total %d Item", len(m.Items))
	}
	if len(m.Items) == 0 {
		return "Vulnerability notification"
	}
	it:= m.Items[0]
	return fmt.Sprintf("[%s] %s", SeverityLabel(it.Severity), OneLine(it.Title(), 0))
}

// markdownBody Render the message body, return the body and**Actual number of written entries**.
//
// The return value kept is the number of entries actually delivered in this delivery attempt, which the caller marks only the first kept as
// Delivered——Items blocked by channel length limit must remain for the next batch instead of being marked successful together.
// Mark success. This is the source of 'silent loss': messages are truncated, but delivery records show all were delivered,
// There is no place that shows the second half was never sent.
//
// maxBytes<=0 Indicates no limit.
func markdownBody(m Message, maxBytes int) (string, int) {
	if!m.Batch {
		if len(m.Items) == 0 {
			return "", 0
		}
		var b strings.Builder
		writeItem(&b, m.Items[0], "", true)
		// Even long messages are sent as-is (final truncation covers it): partial information of a vulnerability
		// Better to send none at all.
		return TruncateBytes(b.String(), maxBytes), 1
	}

	footer:= ""
	if m.HomeURL!= "" {
		footer = fmt.Sprintf("\n[View all in the platform](%s)\n", m.HomeURL)
	}
	kept:= packItemCount(m.Items, maxBytes, markdownReservedBytes, footer, byteSize, func(it Item, idx int) string {
		var b strings.Builder
		writeItem(&b, it, fmt.Sprintf("%d. ", idx+1), false)
		return b.String()
	})

	items:= m.Items[:kept]
	var b strings.Builder
	b.WriteString(markdownBatchIntro(m, items, len(m.Items)))
	for i, it:= range items {
		writeItem(&b, it, fmt.Sprintf("%d. ", i+1), false)
	}
	b.WriteString(footer)
	return TruncateBytes(b.String(), maxBytes), kept
}

// markdownBatchIntro Render the summary message header: time window, count, and level distribution.
// With these, the person receiving the summary doesn't need to click into the platform to determine if it needs immediate processing.
//
// items is**actually install**Entries where total is the expected number for this batch. Both must be explicitly stated if they differ.
// 'How many more are left to be sent in the next message.——otherwise the reader might think the number in the message header is all,
// And those never sent entries do not exist on the interface at all.
func markdownBatchIntro(m Message, items []Item, total int) string {
	var b strings.Builder
	if m.WindowMinutes > 0 {
		fmt.Fprintf(&b, "**In the last %d minutes: %d new vulnerabilities**", m.WindowMinutes, total)
	} else {
		fmt.Fprintf(&b, "**Only Keep %d Vulnerabilities**", total)
	}
	if extra:= total - len(items); extra > 0 {
		fmt.Fprintf(&b, "(first %d items shown; the rest %d continue in the next message)", len(items), extra)
	}
	// Provide distribution by level so readers can immediately see if there are any severe items. Only count**This line actually contains**To
	// Entry, ensure that 'Severe 3' and the countable entries below match.
	counts:= map[string]int{}
	for _, it:= range items {
		counts[it.Severity]++
	}
	var parts []string
	for _, sev:= range []string{"critical", "high", "medium", "low"} {
		if n:= counts[sev]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", SeverityLabel(sev), n))
		}
	}
	if len(parts) > 0 {
		b.WriteString("\n" + strings.Join(parts, " · "))
	}
	b.WriteString("\n\n")
	return b.String()
}

// writeItem Render a single vulnerability item.
//
// prefix Used for the sequence number in summary lists; single=true Render the full version (including summary and backlink),
// Only render one summary line in the aggregated list——Otherwise, 50 entries would become a long document.
//
// All external content (titles/types/assets/summaries) are processed through markdownText:
// Single-line + Escape. The backlink is composed of the public_base_url configured by the administrator and is not untrusted content.
// And must be clickable links, so output them as is.
func writeItem(b *strings.Builder, it Item, prefix string, single bool) {
	line:= fmt.Sprintf("%s**%s · %s**", prefix, SeverityLabel(it.Severity), markdownText(it.Title(), 0))
	if!single {
		// Summary mode: Single line presentation, asset and summary compressed followed by details.
		var extras []string
		if a:= assetLine(it.Assets, maxAssetsShown); a!= "" {
			extras = append(extras, markdownText(a, 0))
		}
		if it.Summary!= "" {
			extras = append(extras, markdownText(it.Summary, 60))
		}
		if len(extras) > 0 {
			line += " — " + strings.Join(extras, " · ")
		}
		b.WriteString(line + "\n")
		return
	}
	b.WriteString(line + "\n")
	if it.IsStatusChange() {
		fmt.Fprintf(b, "**Status Change**: %s → %s\n",
			markdownText(StatusLabel(it.FromStatus), 0), markdownText(StatusLabel(it.ToStatus), 0))
	}
	if it.VulnClass!= "" && it.VulnClass!= it.Title() {
		fmt.Fprintf(b, "**type**: %s\n", markdownText(it.VulnClass, 0))
	}
	if a:= assetLine(it.Assets, maxAssetsShown); a!= "" {
		fmt.Fprintf(b, "**Asset**: %s\n", markdownText(a, 0))
	}
	if it.Summary!= "" {
		if s:= markdownText(it.Summary, maxSummaryRunes); s!= "" {
			fmt.Fprintf(b, "**Failure**: %s\n", s)
		}
	}
	if it.DetailURL!= "" {
		fmt.Fprintf(b, "[view details](%s)\n", it.DetailURL)
	}
}
