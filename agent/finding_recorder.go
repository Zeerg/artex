package agent

import (
	"context"

	"github.com/Zeerg/artex/db"
)

// FindingRecorder is injected by the host; agents never synthesize or copy
// evidence bodies themselves. Its implementation owns the atomic write.
type FindingRecorder interface {
	Record(context.Context, db.RecordFindingInput, []db.TrafficRef) (*db.RecordedFinding, error)
}

// Tool-use guidance is appended without replacing the user's editable prompt.
// It does not require capture or claim that unavailable traffic tools exist.
const findingTrafficGuidance = "\n\n**Vulnerability traffic evidence (optional).**: When reporting a vulnerability using report_finding, if there are HTTP requests/responses already viewed and confirmed to support the vulnerability conclusion, use traffic_refs to bind real IDs in the order of reproduction. Domain and time are only used for candidate filtering; no association is presumed. [], Retain command output, logs etc. in evidence, suggest explaining unbound reason. Don't guess ID, don't repeat probing for packet completion."

func (t *ToolSet) SetFindingRecorder(r FindingRecorder) { t.findingRecorder = r }
func (w *Worker) SetFindingRecorder(r FindingRecorder) { w.findingRecorder = r }
func (p *Planner) SetFindingRecorder(r FindingRecorder) { p.findingRecorder = r }
func (m *MainAgent) SetFindingRecorder(r FindingRecorder) { m.findingRecorder = r }
