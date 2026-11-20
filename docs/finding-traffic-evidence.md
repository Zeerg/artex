# Finding Multi-Traffic Evidence

The finding detail "Linked Traffic" view supports cross-page multi-select, role and note, ordering, and unbinding. The traffic page also supports multi-select, binding multiple records at once to a single existing finding. Finding evidence inherited by a task is read-only; changes must be made in the source task.

The "Agent auto-bind traffic" setting under system settings is off by default. It is read and modified via the `agent_traffic_binding` key of `/api/settings`. Enabling it increases token consumption from request/response inspection, tool calls, and prompt injection; the next agent round picks up the new setting. When disabled, auto-binding parameters and the re-bind tool are hidden, no auto-binding guidance is injected into prompts, and already-running sessions reject newly submitted auto-binds. Manual binding, traffic capture, saved-evidence reads, and exports are unaffected.

Once enabled, the default flow is "finding persisted → report agent auto-triggered → verify and bind traffic → write report at the latest evidence version". The reporter keeps the verification command, key output, existing real traffic IDs, and their role in `evidence`; the report agent combines the finding detail with the execution record, verifies with `traffic_search` / `traffic_get`, calls `bind_finding_traffic`, then reads the latest `version` and saves the report. With the toggle off, the report agent gets no extra raw-traffic search/read tools and does not auto-bind; it can still read manually bound snapshots and produce a report.

Backward compatible for existing callers: `report_finding`'s `traffic_refs` / `evidence_hint_id` can still bind explicitly at report time. Binding is optional: for TCP and other non-HTTP findings, or when nothing was captured or no exact record is found, omitting refs still allows normal reporting and report writing; keep other verifiable evidence such as command output and logs, and a note explaining why nothing was bound is recommended. No new required fields were added. All submitted IDs must be valid and their bodies intact. Any single failure rolls back the whole bind operation; when an explicit bind at report time fails, the entire report rolls back. Re-submitting the same snapshot adds no binding and does not overwrite its note.

## Agent IDs and report versions

`report_finding` gains optional parameters; array order is the initial evidence order:

```json
{
  "traffic_refs": [
    {"traffic_id": "real traffic ID", "role": "baseline", "note": "normal account request"},
    {"traffic_id": "another real traffic ID", "role": "proof", "note": "reproduction request"}
  ]
}
```

Roles are `baseline` (normal control), `proof` (vulnerability proof), `verification` (additional verification), `supporting` (auxiliary evidence, default). Verify real records with `traffic_search` / `traffic_get` first; domain and time are only used to filter candidates, not to infer task ownership.

Prompt design reference: [CyberStrikeAI's vulnerability reporting tool guidance](https://github.com/RuoJi6/CyberStrikeAI/blob/54d56774b8bd285817d16d48b70a4a5e6e0963f7/internal/app/vulnerability_tools.go), combined with this project's optional-binding convention — no required-reason validation when nothing was captured. IDs must never be guessed, and targets should not be re-probed just to fill a package.

The first line of the response is still `finding recorded: <exploration node ID>`; the following JSON carries the standalone finding record `finding_id`, the exploration node `finding_node_id`, and a binding summary.

- `get_finding_traffic(finding_id)` uses the **standalone finding record ID** and returns an ordered list plus `version`; `binding_id`, `side=request|response`, `offset`, and `length` allow ranged reads of up to 8192 bytes per segment.
- `update_finding_report`'s `finding_id` **continues to use the exploration node ID**. The new `evidence_version` should be set to the version actually read; if evidence changed during generation, writes against the stale version are rejected and evidence must be re-read and regenerated.
- Legacy report calls that pass no version do not claim to cover existing traffic evidence. After bindings, notes, roles, or ordering change, existing reports are flagged as needing an update.

With auto-binding enabled, `add_hint` / `add_task_hint` accept `traffic_refs` on a single hint or on each element of a batch `hints`. When the planner reports on a worker's behalf it may pass `evidence_hint_id`, explicitly selecting the refs saved on that task's hints — refs on inherited hints cannot be used. The system never guesses bindings from domain, time, or browsing history. A failed submission produces no partial finding and does not trigger the report early.

When an existing finding missed binding, `bind_finding_traffic(finding_id, traffic_refs)` re-binds without re-registering. `list_findings` / `list_task_findings` / `node_detail` / `get_task_node_detail` return explicit `finding_id` and `finding_node_id`; the legacy `id` keeps exploration-node semantics.

At startup only optional properties are added to legacy tool schemas, and the original default traffic-tool binding is extended to the report agent; the re-bind tool is assigned to the report agent by default. Custom tool binding lists, prompts, descriptions, and enable states are preserved. Guidance is injected uniformly after final tool assembly: the reporting role hands off existing evidence, and the report agent verifies, binds, and writes the report. Platform conversations lacking task context should hand off to the task agent via a structured hint instead of reporting directly. Existing evidence should be handed off before a task is judged complete; missing captures do not force a wait. A failed `report_finding` does not trigger the report agent.

## API

Base path: `/api/exploration/findings/{finding_id}/traffic`, using the standalone finding ID. Authentication is unchanged; `context_task` validates task visibility and inherited read-only.

| Method / relative path | Request / response |
| --- | --- |
| `GET` | Ordered summary, evidence version, version used by the report |
| `POST` | `{"traffic_refs":[...]}` batch append |
| `PATCH /{binding_id}` | `{"version":1,"role":"proof","note":"note"}` |
| `DELETE /{binding_id}` | `{"version":1}` |
| `PUT /order` | `{"version":1,"binding_ids":["2","1"]}`, must be the complete list |
| `GET /{binding_id}` | Snapshot metadata and bounded body preview |
| `GET /{binding_id}/body` | `side`, `offset`, `length`; `download=1` downloads the full raw bytes |

Conflicting version/order sets and writes during archiving return `409`; inherited writes return `403`; bindings that don't exist or don't belong to the finding return `404`; traffic/attachment read and validation failures return explicit errors.

## Storage and migration

An idempotent PostgreSQL migration runs at startup: adds `traffic_evidence_snapshots`, `finding_traffic_bindings`, and `findings.evidence_version` / `report_evidence_version` (default 0). No retroactive binding is guessed from historical text.

Snapshots store the source traffic ID, capture time, URL, method, status, request/response headers, body length, and SHA-256. Bodies are stored by hash under `<data>/evidence/blobs/<first two hex>/<hash>.bin`, independent of the cleanable `data/traffic`; multiple findings can share snapshots/bodies. Snapshots have no content-update interface; reads and exports fail on hash mismatch.

The full body is read under the source traffic write lock, including large-body blobs and legacy directory records. Files are persisted and verified first, then a single PostgreSQL transaction writes the exploration node, intent relations, finding, snapshot, and bindings; the planner is notified only after commit. Failures may leave unreferenced files but never partial business records.

PostgreSQL advisory lock `7337741004` coordinates evidence files and SQL references; the task row lock forbids evidence modification once archiving is queued. Restore holds the evidence lock from body install through metadata commit. Deleting a finding cascades its bindings.

The cleaner runs hourly, reclaiming only unreferenced content that is not part of an in-flight operation, with at least a 24-hour delay. Normal traffic cleanup never touches the evidence directory. When backing up hot data, back up both PostgreSQL and `data/evidence`.

## Export and archiving

Markdown includes the ordered evidence list and its version; JSON includes metadata; CSV additionally includes counts and binding IDs. `md-zip` keeps the finding Markdown and provides:

```text
evidence/<finding_id>/<binding_id>/
  manifest.json
  request.http
  response.http
  request.bin
  response.bin
```

Markdown references messages via relative links. Before the download is sent, attachment copying, hash verification, compression, disk sync, and CRC read-verification of every ZIP entry all complete; anything missing or corrupt fails the whole download. Full attachments preserve the original binary bytes.

Archive v3 collects snapshots and bodies by finding-binding relationship, independent of the source traffic or domain. Hot data is cleaned only after package verification completes; shared evidence remains. Restore verifies the installed bodies first, then restores metadata and bindings in a transaction, with retry on failure. v1/v2 archives remain restorable; missing new fields are explicitly backfilled to 0.

## Verification and boundaries

Each test package gets its own freshly created PostgreSQL test database, specified via `ARTEX_PG_DSN`, to avoid residual task/model fixtures triggering background runs. Run the related packages' full test suites and confirm nothing is skipped for missing configuration:

```sh
# Set ARTEX_PG_DSN to a dedicated test database per package before running;
# an explicitly configured failure must be reported as an error.
go test ./<package> -count=1
go test -race -p 1 ./evidence ./db ./agent ./server -run 'TestEvidence|TestFindingTraffic|TestFindingEvidence|TestReportFindingAtomicContract|TestTaskArchive'
```

Frontend verification includes `npx tsc --noEmit`, Biome checks on affected files, a Webpack build, and a `NEXT_EXPORT=1` static export. Use a separate cache directory to avoid clobbering a running dev server.

Local end-to-end acceptance uses a dedicated port, controlled HTTP / domain HTTPS targets, and a temporary data directory, covering both binding entry points, cross-page selection, ordering/notes, error messaging, inherited read-only, downloads, plus export/archive/hot-body-reclaim/restore after deleting the source traffic, with hash verification.

The first version uses a global evidence coordination lock; during large batch binds/exports or slow attachment downloads, other evidence operations may wait. Evidence cannot be fabricated when recordings or full bodies are missing. This feature does not change the capture toggle and does not address certificate issues for HTTPS access to raw IPs.
