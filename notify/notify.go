// Package notify Implement the IM / email push channel adapter for vulnerability discovery.
//
// Layering: This package is**Sub-package**, Depends solely on the standard library. It does not recognize databases or servers. Channel configuration
// As map[string]any Corresponding notification_channels.config This JSONB column),
// Content to be pushed is passed in as a Message. ThisThe benefit of splitting: signature computation, UTF-8 truncation, and filter matching
// The truly error-prone parts can be decoupled from PostgreSQL unit tests. The host only needs to do orchestration on the server side.
//
// Concurrency agreement: The implementation of Channel must**Stateless**. The same Channel instance is configured by multiple channels.
// (even multiple robot instances in the same channel can be concurrently reused, all credentials are passed in via the cfg parameter,
// It is not allowed to cache things like webhook URLs in fields that implement themselves.
package notify

// Channel type identifier. The value is also notification_channels..kind A legal set, determined by the server's return type,
// Whitelist validation (with findings.status Similarly, no need for DB CHECK, convenient for adding channels later.
const (
	KindDingTalk = "dingtalk" // DingTalk custom robot
	KindFeishu = "feishu" // Feishu(Contains Lark)Custom robot
	KindWeCom = "wecom" // Enterprise WeChat robot
	KindWebhook = "webhook" // Generic Webhook: Custom method/header/JSON template
	KindTelegram = "telegram" // Telegram Bot API
	KindEmail = "email" // SMTP Selected
)

// Event type, corresponding to notification_events.kind.
const (
	EventFindingCreated = "finding_created"
	EventFindingStatusChanged = "finding_status_changed"
)

// InitKind Fallback value for kind empty in config.
const InitKind = KindDingTalk

// severityRank Map vulnerability levels to comparable sequence numbers. Unknown level returns 0, so any
// min_severity Settings will block unknown levels outside——Do not push if there is doubt, to avoid false positives flooding the screen.
var severityRank = map[string]int{
	"low": 1,
	"medium": 2,
	"high": 3,
	"critical": 4,
}

// SeverityRank Return the sequence number of the level; return 0 for unknown levels.
func SeverityRank(severity string) int { return severityRank[severity] }

// SeverityLabel Returns emoji-prefixed Chinese level name for use in message titles and card coloring.
// Unknown level echoed as-is, no speculation.
func SeverityLabel(severity string) string {
	switch severity {
	case "critical":
		return "🔴 Support"
	case "high":
		return "🟠 Retest"
	case "medium":
		return "🟡 Medium Risk"
	case "low":
		return "🔵 Low Risk"
	default:
		return severity
	}
}

// StatusLabel Translate disposition status to Chinese for use in state change messages.
func StatusLabel(status string) string {
	switch status {
	case "pending":
		return "Pending"
	case "in_progress":
		return "Processing"
	case "confirmed":
		return "Confirmed"
	case "resolved":
		return "Completed"
	case "fixed":
		return "Fixed"
	case "false_positive":
		return "Injection"
	case "ignored":
		return "Response"
	case "duplicate":
		return "Return"
	case "risk_accepted":
		return "Risk acceptance"
	default:
		return status
	}
}

// AtLeast Determines if severity reaches the minimum threshold. If min is empty, it means no threshold is set, and all pass.
// Note that unknown severity has an index of 0 and will be rejected by any non-empty min (see severityRank comments).
func AtLeast(severity, min string) bool {
	if min == "" {
		return true
	}
	return SeverityRank(severity) >= SeverityRank(min)
}
