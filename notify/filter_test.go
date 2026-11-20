package notify

import "testing"

func TestParseFilterMalformedFallsBackToMatchAll(t *testing.T) {
	// Malformed JSON, empty input, fields of incorrect type.——All must degrade to zero-value Filter,
	// That is, 'no filtering'. This invariant is the endpoint of 'prefer more pushes over missing pushes':
	// Once this is changed to error or partial parsing, a single character typo by the user will silently discard all high-risk notifications.
	cases := []struct {
		name string
		raw  string
	}{
		{"Empty Input", ""},
		{"Invalid JSON", `{not json`},
		{"Truncated JSON", `{"min_severity":`},
		{"Type mismatch", `{"min_severity": 123, "task_ids": "abc"}`},
		{"Top level is array", `[1,2,3]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := ParseFilter([]byte(tc.raw))
			if f.MinSeverity != "" || len(f.TaskIDs) != 0 || len(f.AssetIDs) != 0 {
				t.Fatalf("Malformed configuration should degrade to zero-value Filter, resulting in %+v", f)
			}
			// A zero-value Filter must match any event.
			ev := Snapshot{Kind: EventFindingCreated, Severity: "low", VulnClass: "XSS"}
			if !Match(f, ev) {
				t.Fatal("Zero-value Filter should hit all events")
			}
		})
	}
}

func TestMatchSeverityThreshold(t *testing.T) {
	ev := func(sev string) Snapshot {
		return Snapshot{Kind: EventFindingCreated, Severity: sev}
	}
	cases := []struct {
		min    string
		sev    string
		expect bool
	}{
		{"", "low", true},
		{"", "critical", true},
		{"high", "critical", true},
		{"high", "high", true},
		{"high", "medium", false},
		{"high", "low", false},
		{"critical", "high", false},
		{"critical", "critical", true},
		// Unknown level sequence number is 0, should be blocked by any non-empty threshold (doubtful do not push).
		{"low", "", false},
		{"low", "unknown", false},
		{"", "", true},
	}
	for _, tc := range cases {
		got := Match(Filter{MinSeverity: tc.min}, ev(tc.sev))
		if got != tc.expect {
			t.Errorf("min=%q sev=%q: Expectation %v Got %v", tc.min, tc.sev, tc.expect, got)
		}
	}
}

func TestMatchScopeRestrictions(t *testing.T) {
	ev := Snapshot{
		Kind:      EventFindingCreated,
		Severity:  "high",
		TaskID:    7,
		AssetIDs:  []int64{10, 20},
		VulnClass: "SQLCompleted",
	}
	cases := []struct {
		name   string
		filter Filter
		expect bool
	}{
		{"Empty range=Unlimited", Filter{}, true},
		{"task hit", Filter{TaskIDs: []int64{7}}, true},
		{"Task not hit", Filter{TaskIDs: []int64{8}}, false},
		{"Task Multi-Select Contains Hits", Filter{TaskIDs: []int64{8, 7}}, true},
		{"Assets have an intersection", Filter{AssetIDs: []int64{20, 99}}, true},
		{"Assets with no intersection", Filter{AssetIDs: []int64{99}}, false},
		{"Task and asset hit simultaneously", Filter{TaskIDs: []int64{7}, AssetIDs: []int64{10}}, true},
		{"Task hit but asset not hit", Filter{TaskIDs: []int64{7}, AssetIDs: []int64{99}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.filter, ev); got != tc.expect {
				t.Errorf("Expectation %v Got %v", tc.expect, got)
			}
		})
	}
}

func TestMatchVulnClassKeywords(t *testing.T) {
	ev := func(class string) Snapshot {
		return Snapshot{Kind: EventFindingCreated, Severity: "high", VulnClass: class}
	}
	cases := []struct {
		name   string
		filter Filter
		class  string
		expect bool
	}{
		{"include Empty=Collect All", Filter{}, "Any type", true},
		{"include Hit", Filter{VulnClassInclude: []string{"SQL"}}, "SQLCompleted", true},
		{"include Not hit", Filter{VulnClassInclude: []string{"Command Execution"}}, "SQLCompleted", false},
		{"include Any of multiple words hit", Filter{VulnClassInclude: []string{"Command Execution", "SQL"}}, "SQLCompleted", true},
		{"Case insensitive", Filter{VulnClassInclude: []string{"sql"}}, "SQLCompleted", true},
		{"exclude Hit to exclude", Filter{VulnClassExclude: []string{"Information Leakage"}}, "Information Leakage", false},
		{"exclude Allow if not hit", Filter{VulnClassExclude: []string{"Information Leakage"}}, "SQLCompleted", true},
		// Exclusion takes precedence over inclusion: When both apply, it should be disqualified.
		{"Exclusion takes precedence over inclusion", Filter{
			VulnClassInclude: []string{"SQL"},
			VulnClassExclude: []string{"Completed"},
		}, "SQLCompleted", false},
		// Blank keywords should be ignored, otherwise they'll degrade into 'matching all strings with spaces'.
		{"Blank keywords are ignored", Filter{VulnClassInclude: []string{"", "  "}}, "SQLCompleted", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.filter, ev(tc.class)); got != tc.expect {
				t.Errorf("Expectation %v Got %v", tc.expect, got)
			}
		})
	}
}

func TestMatchStatusChangeRequiresOptIn(t *testing.T) {
	ev := Snapshot{Kind: EventFindingStatusChanged, Severity: "critical", FromStatus: "pending", ToStatus: "fixed"}
	// Default off: Most people say 'pushing vulnerabilities' refers to discovering new vulnerabilities, not status logs.
	if Match(Filter{MinSeverity: "low"}, ev) {
		t.Fatal("Status change events should be skipped when not enabled.")
	}
	if !Match(Filter{OnStatusChange: true}, ev) {
		t.Fatal("After enabling on_status_change, status change events should hit")
	}
	// Event creation should not be affected by on_status_change.
	created := Snapshot{Kind: EventFindingCreated, Severity: "critical"}
	if !Match(Filter{MinSeverity: "low"}, created) {
		t.Fatal("Event creation should not depend on on_status_change.")
	}
}
