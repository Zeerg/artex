package report

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Zeerg/artex/db"
)

// The rendering used for the 'export' on the findings page:Render a batch of findings into summary Markdown, single-line Markdown,
// Or CSV. JSON serialized directly by server layer using DTO,Not here.

// sortFindingsForExport Sort by severity level descending, then by time in reverse order,Consistent with the grouping of the summary report.
func sortFindingsForExport(fs []*db.DBFinding) {
	sort.SliceStable(fs, func(i, j int) bool {
		ri, rj:= sevRank[fs[i].Severity], sevRank[fs[j].Severity]
		if ri!= rj {
			return ri < rj // sevRank The smaller, the more serious
		}
		return fs[i].CreatedAt.After(fs[j].CreatedAt)
	})
}

// findingTitle Get readable vulnerability title:Name → Statistics → 'Uncategorized'.
func findingTitle(f *db.DBFinding) string {
	return nz(f.Name, nz(f.VulnClass, "Uncategorized"))
}

// FindingsMarkdown Consolidate a batch of findings into a single summary report(Failure + Group by Severity Level,
// Each entry includes category/status/parent task/evidence/detailed report.).
func FindingsMarkdown(fs []*db.DBFinding, generatedAt time.Time) string {
	items:= append([]*db.DBFinding(nil), fs...)
	sortFindingsForExport(items)

	var b strings.Builder
	b.WriteString("# Vulnerability discovery summary report\n\n")
	fmt.Fprintf(&b, "- **Generation time**: %s\n", generatedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "- **Total Discoveries**: %d Pages\n\n", len(items))

	// Failure:Count by severity levels.
	counts:= map[string]int{}
	for _, f:= range items {
		counts[f.Severity]++
	}
	b.WriteString("## Failure\n\n")
	b.WriteString("| Severity level | Quantity |\n| --- | --- |\n")
	for _, s:= range []struct{ key, label string }{
		{"critical", "Support"}, {"high", "Retest"}, {"medium", "Medium Risk"}, {"low", "Low Risk"},
	} {
		fmt.Fprintf(&b, "| %s | %d |\n", s.label, counts[s.key])
	}
	b.WriteString("\n")

	if len(items) == 0 {
		b.WriteString("_No matching vulnerability. _\n")
		return b.String()
	}

	b.WriteString("## Vulnerability Details,\n\n")
	for i, f:= range items {
		fmt.Fprintf(&b, "### %d. [%s] %s\n\n", i+1, strings.ToUpper(nz(f.Severity, "info")), findingTitle(f))
		if f.VulnClass!= "" {
			fmt.Fprintf(&b, "- **Statistics**: %s\n", f.VulnClass)
		}
		fmt.Fprintf(&b, "- **Status**: %s\n", nz(f.Status, "pending"))
		if desc:= strings.TrimSpace(f.TaskDescription); desc!= "" {
			fmt.Fprintf(&b, "- **Belongs to task**: %s\n", desc)
		}
		fmt.Fprintf(&b, "- **discovery time**: %s\n\n", f.CreatedAt.Format("2006-01-02 15:04:05"))
		if s:= strings.TrimSpace(f.Summary); s!= "" {
			fmt.Fprintf(&b, "%s\n\n", s)
		}
		if e:= strings.TrimSpace(f.Evidence); e!= "" {
			fmt.Fprintf(&b, "**Evidence:**\n\n```\n%s\n```\n\n", e)
		}
		if rep:= strings.TrimSpace(f.Report); rep!= "" {
			b.WriteString("**Detailed report:**\n\n")
			b.WriteString(rep)
			b.WriteString("\n\n")
		}
		b.WriteString(findingTrafficMarkdown(f, false))
		b.WriteString("---\n\n")
	}
	return b.String()
}

// SingleFindingMarkdown Render a single vulnerability as an independent Markdown file.(Used for 'One Vulnerability One File' packaging).
func SingleFindingMarkdown(f *db.DBFinding, generatedAt time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# [%s] %s\n\n", strings.ToUpper(nz(f.Severity, "info")), findingTitle(f))
	if f.VulnClass!= "" {
		fmt.Fprintf(&b, "- **Statistics**: %s\n", f.VulnClass)
	}
	fmt.Fprintf(&b, "- **Severity level**: %s\n", nz(f.Severity, "info"))
	fmt.Fprintf(&b, "- **Status**: %s\n", nz(f.Status, "pending"))
	if desc:= strings.TrimSpace(f.TaskDescription); desc!= "" {
		fmt.Fprintf(&b, "- **Belongs to task**: %s\n", desc)
	}
	fmt.Fprintf(&b, "- **discovery time**: %s\n", f.CreatedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "- **Generation time**: %s\n\n", generatedAt.Format("2006-01-02 15:04:05"))
	if s:= strings.TrimSpace(f.Summary); s!= "" {
		fmt.Fprintf(&b, "## Assertion\n\n%s\n\n", s)
	}
	if e:= strings.TrimSpace(f.Evidence); e!= "" {
		fmt.Fprintf(&b, "## Subnet\n\n```\n%s\n```\n\n", e)
	}
	if rep:= strings.TrimSpace(f.Report); rep!= "" {
		b.WriteString("## Detailed Report,\n\n")
		b.WriteString(rep)
		b.WriteString("\n")
	}
	b.WriteString(findingTrafficMarkdown(f, true))
	return b.String()
}

var unsafeFilenameChars = regexp.MustCompile(`[^\p{Han}\p{L}\p{N}._-]+`)

// FindingFilename Generating a safe file for 'one finding per file'. .md File name,in the form of
// `critical_SQLInject_#123.md`. Remove path separators and control characters.,Avoid illegal paths within zip files.
func FindingFilename(f *db.DBFinding) string {
	sev:= nz(f.Severity, "info")
	title:= findingTitle(f)
	name:= fmt.Sprintf("%s_%s_#%d", sev, title, f.ID)
	name = unsafeFilenameChars.ReplaceAllString(name, "_")
	name = strings.Trim(name, "._")
	if name == "" {
		name = fmt.Sprintf("finding_%d", f.ID)
	}
	// Defensive:Peel off another layer of path.,Prevent zip slip.
	name = path.Base(name)
	if len(name) > 120 {
		name = name[:120]
	}
	return name + ".md"
}

// FindingsCSV Render a batch of findings into CSV.(With UTF-8 BOM.,Convenient for Excel to correctly recognize Chinese).
// Does not contain large sections of report/evidence text,Only place summary fields;Need to export full text using Markdown/JSON.
func FindingsCSV(fs []*db.DBFinding) []byte {
	items:= append([]*db.DBFinding(nil), fs...)
	sortFindingsForExport(items)

	var buf bytes.Buffer
	buf.WriteString("\xEF\xBB\xBF") // UTF-8 BOM
	w:= csv.NewWriter(&buf)
	_ = w.Write([]string{"ID", "Name", "Statistics", "Severity level", "Status", "Belongs to task", "discovery time", "Assertion", "Traffic evidence count", "Traffic evidence ID"})
	for _, f:= range items {
		_ = w.Write([]string{
			fmt.Sprintf("%d", f.ID),
			findingTitle(f),
			f.VulnClass,
			nz(f.Severity, "info"),
			nz(f.Status, "pending"),
			f.TaskDescription,
			f.CreatedAt.Format("2006-01-02 15:04:05"),
			strings.TrimSpace(f.Summary),
			fmt.Sprint(len(f.TrafficBindings)), findingTrafficIDs(f),
		})
	}
	w.Flush()
	return buf.Bytes()
}

func findingTrafficIDs(f *db.DBFinding) string {
	ids:= make([]string, 0, len(f.TrafficBindings))
	for _, b:= range f.TrafficBindings {
		ids = append(ids, fmt.Sprint(b.ID))
	}
	return strings.Join(ids, ",")
}

func findingTrafficMarkdown(f *db.DBFinding, attachments bool) string {
	stale:= f.Report!= "" && f.EvidenceVersion!= f.ReportEvidenceVersion
	if len(f.TrafficBindings) == 0 &&!stale {
		return ""
	}
	var out strings.Builder
	out.WriteString("\n## Associate traffic evidence\n\n")
	fmt.Fprintf(&out, "Evidence version:%d; Binding count:%d. \n\n", f.EvidenceVersion, len(f.TrafficBindings))
	if stale {
		out.WriteString("Evidence has changed, detailed report pending update.\n\n")
	}
	for i, b:= range f.TrafficBindings {
		fmt.Fprintf(&out, "%d. **Subnet #%d · %s** — `%s %s`, Status Code %d\n", i+1, b.ID, b.Role, b.Snapshot.Method, strings.ReplaceAll(b.Snapshot.URL, "`", "%60"), b.Snapshot.Status)
		if b.Note!= "" {
			fmt.Fprintf(&out, "   %s\n", strings.ReplaceAll(b.Note, "\n", "\n   "))
		}
		if attachments {
			fmt.Fprintf(&out, " [Request message](evidence/%d/%d/request.http) · [Response Message](evidence/%d/%d/response.http)\n", f.ID, b.ID, f.ID, b.ID)
		}
	}
	out.WriteString("\n")
	return out.String()
}
