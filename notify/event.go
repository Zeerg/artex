package notify

// Snapshot is notification_events.snapshot Contract for this JSONB column. The writer is at the db layer
// Vulnerability database transaction, read side is the server layer's delivery engine and filtering matching. Defined in this package because it is
// 'Payload for 'Notification domain': db only handles serialization, doesn't understand field meanings.
//
// Why store redundant vulnerability fields instead of querying during rendering: vulnerabilities will be renamed, reclassified, and restatused later,
// Push content should reflect**At the Time of Incident**Conclusion——A recheck will show 'later changed to low'.
// Dangerous misdirection. Therefore, fan-out and rendering do not need to JOIN findings/tasks/assets three tables.
type Snapshot struct {
	// Event type: finding_created / finding_status_changed
	Kind string `json:"kind"`
	FindingID int64 `json:"finding_id"`
	TaskID int64 `json:"task_id"`
	VulnClass string `json:"vulnclass"`
	Name string `json:"name"`
	Severity string `json:"severity"`
	Summary string `json:"summary"`
	AssetIDs []int64 `json:"asset_ids"`
	// Only kind=finding_status_changed Must be non-empty.
	FromStatus string `json:"from_status,omitempty"`
	ToStatus string `json:"to_status,omitempty"`
}

// Item It's a pending push vulnerability, for channel rendering',
type Item struct {
	FindingID int64
	Name string
	VulnClass string
	Severity string
	Summary string
	// Assets It is the parsed asset display name (e.g., domain/IP). Filled by the server layer.——
	// This package does not touch the database, so it cannot get the name.
	Assets []string
	// DetailURL A backlink to vulnerability details; empty means no public_base_url is configured, omit during rendering.
	DetailURL string
	// Event-specific status change; render as 'Pending' when both are non-empty. → Fixed.
	FromStatus string
	ToStatus string
}

// IsStatusChange Report whether this entry is a status change event.
func (i Item) IsStatusChange() bool { return i.FromStatus!= "" || i.ToStatus!= "" }

// Title Display title for entries: Prioritize manually named 'name', fall back to vulnerability type 'vulnclass'.
// Use a placeholder when both are empty——Never output an empty title.
func (i Item) Title() string {
	if i.Name!= "" {
		return i.Name
	}
	if i.VulnClass!= "" {
		return i.VulnClass
	}
	return "(Unnamed vulnerability)"
}

// Message This is a complete content sent through a channel.
type Message struct {
	// Single push length is 1; summary push (digest) is a batch.
	// An empty slice is illegal; the caller must ensure at least one item.
	Items []Item
	// Batch=true When rendering summary messages (changing the title, adding time window and count).
	Batch bool
	// WindowMinutes This is the summary cycle (in minutes), only for Batch=true Used in copy text 'last N minutes'.
	// Deliberately passed in via configuration explicitly rather than calculated at render time..Since: Render with determinism for testing.
	WindowMinutes int
	// HomeURL It's the platform panel address (global public_base_url); empty means no panel entry.
	HomeURL string
}
