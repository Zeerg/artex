package notify

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Filter notification_channels..filter Contract for this JSONB column: filter conditions for channel instances.
// All fields are optional, with default being 'no filtering'.——This is the fallback semantics of malformed configurations, see ParseFilter.
type Filter struct {
	// MinSeverity The lowest level threshold (low/medium/high/critical), empty=No threshold set.
	MinSeverity string `json:"min_severity"`
	// TaskIDs / AssetIDs Empty array means no limit; non-empty requires event intersection with it.
	TaskIDs []int64 `json:"task_ids"`
	AssetIDs []int64 `json:"asset_ids"`
	// VulnClassInclude An empty value means all. If non-empty, it requires that vulnclass matches any of the keywords.
	// VulnClassExclude Exclude any keyword hit (exclusion takes precedence over inclusion).
	// The matching method is case-insensitive substring——Safer than regex: user misconfiguring regex won't make the channel silentlyinvalidation.
	VulnClassInclude []string `json:"vulnclass_include"`
	VulnClassExclude []string `json:"vulnclass_exclude"`
	// OnStatusChange Decide whether this channel should receive vulnerability status change events (only meaningful in real-time mode).
	OnStatusChange bool `json:"on_status_change"`
}

// ParseFilter Parse channel filter configuration.
//
// **Never return error.** This is an intentional design choice: when filter conditions are malformed, they should degrade to zero values.
// Filter (= No filtering = All hits), because for a vulnerability notification system,**Pushing one more is better
// Silently miss a high-risk one**. Let parsing failures become 'no push', which gives users the impression that everything is configured,
// A channel that actually pushes nothing——This is the worst failure mode.
func ParseFilter(raw []byte) Filter {
	var f Filter
	if len(raw) == 0 {
		return f
	}
	// When parsing fails, f remains zero value, meaning no filtering.
	_ = json.Unmarshal(raw, &f)
	return f
}

// ValidMinSeverity Check if report level is a valid threshold (empty string means no threshold).
func ValidMinSeverity(s string) bool {
	if s == "" {
		return true
	}
	_, ok:= severityRank[s]
	return ok
}

// Validate Validation filter configuration**value restricted**The field for calling when saving a channel.
//
// Why must it be intercepted when writing: Match's judgment on unknown thresholds is `rank >= 0`, Always true——
// That means min_severity misspelled one character ("hgih"), Filter will**Silent Ineffectiveness**Turn into
// 'Full push. This aligns with this package's approach of 'prefer more over less' (won't miss any).
// But the consequence is that users think they are doing tiered pushes while actually flooding the entire vulnerability into the group,
// And there are no signs to suggest he made a mistake. This kind of 'silent downgrade' should be stopped at the entry point.
//
// Note that Validate is only used for**Write**Path. Reading paths still follows ParseFilter's lenient semantics,
// So bad values already existing in historical data won't prevent the channel from reading entirely.
func (f Filter) Validate() error {
	if!ValidMinSeverity(f.MinSeverity) {
		return fmt.Errorf("Lowest level %q Invalid, optional: low / medium / high / critical, or leave empty for none.", f.MinSeverity)
	}
	return nil
}

// Match Determine whether an event should be delivered to a channel with this filter condition.
//
// **Never return error.**, The reason is the same as ParseFilter: any internal exception is treated as a 'hit'.
// Order of determination: event type → Level Threshold → Task/asset scope → Vulnerability type keywords.
func Match(f Filter, s Snapshot) bool {
	// Status change events only received by explicitly enabled channels. Default off because most users
	// Expect 'push' to mean 'discover new vulnerabilities,' not a log of every state transition.
	if s.Kind == EventFindingStatusChanged &&!f.OnStatusChange {
		return false
	}
	if!AtLeast(s.Severity, f.MinSeverity) {
		return false
	}
	if len(f.TaskIDs) > 0 &&!slices.Contains(f.TaskIDs, s.TaskID) {
		return false
	}
	if len(f.AssetIDs) > 0 &&!intersectsInt(f.AssetIDs, s.AssetIDs) {
		return false
	}
	// Exclude priority: Eliminate if any exclusion keyword is hit, even if it also hits the inclusion list.
	if len(f.VulnClassExclude) > 0 && containsAnyFold(s.VulnClass, f.VulnClassExclude) {
		return false
	}
	if len(f.VulnClassInclude) > 0 &&!containsAnyFold(s.VulnClass, f.VulnClassInclude) {
		return false
	}
	return true
}

func intersectsInt(a, b []int64) bool {
	// A small set can be linearly scanned; both sides have a magnitude of 'manually selected dozens'.
	// The cost of creating a map is greater than the benefit.
	for _, v:= range b {
		if slices.Contains(a, v) {
			return true
		}
	}
	return false
}

// containsAnyFold Does report s contain any keyword from keywords (case-insensitive).
func containsAnyFold(s string, keywords []string) bool {
	lower:= strings.ToLower(s)
	for _, kw:= range keywords {
		kw = strings.ToLower(strings.TrimSpace(kw))
		if kw!= "" && strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}
