package agent

import (
	"log"
	"path/filepath"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/noaadapter"
)

// noaWarn returns a diagnostics sink tagging non-fatal noa messages with the
// session, routed through the package logger (agents have no per-instance one).
func noaWarn(session string) func(string) {
	return func(msg string) { log.Printf("[noa] %s: %s", session, msg) }
}

// noa It is norma v0..4.0 The introduced 'model-driven context compression' mechanism,As a platform experimental feature, users can use it in
// A toggle in system settings. It is mutually exclusive with the built-in compaction.:noaadapter.Enable Is The Only Entry,Hang in there
// Context Takeover Tool(Compactor), Compress Tool with three fixed prompts,Not calling Enable means it is disabled.
// (Built-in compaction works normally.). The switch is parsed by each agent's noaEnabledFn.,Every run reads
// Once,This switch only affects runs started after it is toggled,No need to rebuild agent.

// enableNoa When parser report is enabled, integrates noa into opts. archiveRoot is the persistent base directory for compressed original text.
// (Get global workDir,Each agent lands uniformly <workDir>/noa down,Does not scatter with task/intent directories.),sessionID
// Name the archived subdirectory under it(Globally unique,So they do not conflict within the same base directory).
//
// noa Experimental feature:Do not interrupt real tasks uponintegration failure. When an error occurs, report it through onWarn and revert to built-in compression.
// Clear opts when enabled successfully.Compaction,To avoid agentcore alerting due to 'two context managers being set simultaneously'.
func enableNoa(opts *agentcore.Options, enabled func() bool, archiveRoot, sessionID string, onWarn func(string)) {
	if enabled == nil ||!enabled() {
		return
	}
	if opts.OnWarn == nil {
		opts.OnWarn = onWarn
	}
	if err:= noaadapter.Enable(opts, noaadapter.Options{
		ArchiveBaseDir: filepath.Join(archiveRoot, "noa"),
		SessionID: sessionID,
		OnWarn: onWarn,
	}); err!= nil {
		if onWarn!= nil {
			onWarn("noa Compression enabled failed,Revert built-in compression:" + err.Error())
		}
		return
	}
	// Compactor Cover Compaction,But when both exist, agentcore will issue an alert each time.;Clear explicitly.
	opts.Compaction = nil
}
