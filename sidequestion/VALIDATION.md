# `/btw` Verification Record,

Date: 2026-09-10. Branch:`codex/btw-side-question`. Baseline:`8dae851b9b622f2ff2631f332fde9719d0b16fba`.

Use an independent PostgreSQL test database and data directory; real model credentials are injected into the independent testing environment only, without writing to code or this record, nor modifying the product's default model. Go 1.26.3, norma v0.3.6, Next.js 16.2.9.

Actual model dialogues, returned objects, engineering assertions, and Qwen original review text are saved in [validation-2026-09-10.json](validation-2026-09-10.json), None of the API credentials.

## Engineering Check,

| range | Result | Subnet |
| --- | --- | --- |
| Structured message, deep copy of tool parameters | Success | `TestCheckpointDeepCopyAndBoundaries` |
| Summary / compressed request does not overwrite, complete response and final state release, half-response exclusion | Success | `TestCheckpointDeepCopyAndBoundaries`, `TestSnapshotExcludesPartialStreamAndSelectsPoolMember` |
| Actual model pool member identity | Success | `TestSnapshotExcludesPartialStreamAndSelectsPoolMember` |
| Tool pairing, 20 sets of replay, budget trimming and over-limit errors | Success | `TestBuildRequestCompactionToolPairingAndBudget` |
| Main side-channel parallel, bidirectional isolation cancellation. | Success | Blocking Provider,`TestMainSideConcurrencyAndIndependentCancellation` |
| No tool execution, stream / non-stream, usage already counted on failure | Success | `TestServiceNoToolsAndUsageOnFailure` |
| The real norma ChatAgent + Local Read tool, main transcript / activity isolation | Success | `TestSideActualChatCheckpointToolResultAndTranscriptIsolation`, Stream and non-stream sub-cases |
| Persisted, paginated, idempotent, and restarts should retain partial answers. | Success | `TestSideHistoryIdempotencyPagingAndRecovery` |
| Clear the competition between delayed writes, parent resource deletion, and version comparison. | Success | `TestSideClearLateWritersAndDeletedParent` |
| MainAgent / Worker Archive and recovery, v1/v2/v3 | Success | `TestSideTaskArchiveVersions` |
| Three parent interfaces, authentication, resource ownership, Worker logical deletion | Success | `TestSideHTTPGlobalLimitTaskWorkerAndDeletion`, `TestSideCheckpointPersistsBeforeAdmissionAndRestart` |
| Busy main session can bypass, independently reconnect/disconnect/cancel/clear | Success | `TestSideHTTPBusyIsolationClearAndReconnect` |
| 1 per parent session / globally 4 concurrent | Success | Two `TestSideHTTP...` Use Case |
| Snapshot stored in database before submission, restart for follow-up questions, old sessions cannot fake snapshots. | Success | `TestSideCheckpointPersistsBeforeAdmissionAndRestart` |
| Configuration in cache is rejected after deletion or model change. | Success | `TestSideRejectsDeletedOrChangedCachedProfile` |
| Cancel archiving before final answer and wait for usage data to be stored in the database. | Success | `TestSideTaskDrainPersistsBeforeArchive` |
| Only count usage and side question attribution once when a stream consumer cancels early. | Success | `TestSideUsageRecordedOnceOnConsumerCancellation` |
| Restart automatically recovering Worker / deadline running context to continue publishing new snapshots. | Success | `TestSideRestoredWorkerRuntimePublishesNewCheckpoint` |
| Related package race check | Success | the requirements of the following commands |
| TypeScript With production build | Success | `npx tsc --noEmit`, `npm run build` |
| A new frontend module Biome has been added. | Success | `biome check`, 3 One new module |

Configure in a separate disposable database `ARTEX_PG_DSN` Afterward, you can reproduce automated checks (do not point to production databases):

```sh
go test -race ./agent ./db ./server ./sidequestion ./llmrec ./llmpool \
  -run 'Test(Side|Checkpoint|Snapshot|BuildRequest|Service|MainSide|CaptureRun|TaskArchive|CompleteForwards|StopIntent|CancelIntent)' -count=1
cd web
npx tsc --noEmit
npx biome check src/lib/side-questions.ts src/hooks/use-side-questions.ts src/components/side-question-workspace.tsx
npm run build
```

Full Go regression is not all green:`server` Both have two existing tests failing during temporary directory cleanup. `TempDir RemoveAll ... directory not empty`:

- `TestInheritedActivityDetailAndRelationDeletion`
- `TestTaskMetadataPatchReturnsRenameAndPin`

After exporting the source code from the unmodified baseline in the above environment, rerun it. `server` Package, also reproduces these two cleanup failures. Baseline run encounters another `TestCoreTaskLifecyclePG` Assertion failed for the number of target nodes; the final modified content and side questions are displayed simultaneously; the desktop sidebar functions normally. `server` Regression failed for this assertion. Other packages passed, this side-channel related test case and race check passed. No baseline issues were marked as passed in this inspection, nor were hidden issues modified to existing assertions.

Next.js Build output warns about existing multiple lockfile / workspace root; build complete and all pages generated successfully.

## Browser check

Use Codex In-app Browser to connect an independent local Go service and Next.js Development server. Desktop with 390 × 844 Narrow screen complete the following automated operations, check screenshots and browser logs:

- Input during normal chat session. `/btw`, Backup chain; these two backup services were not verified this time.
- Continuous follow-up; retain generated part after side-channel stop; main flow continues.
- Request continuation when closing the panel, resume completed answers after reopening; empty on page refresh. `/btw` Restore history.
- Narrow screen Drawer's input, button, history, and close operations work normally, no horizontal overflow.
- Clear the confirmation dialog for clearing usage; after clearing, history disappears, but the main transcript and snapshot are retained.
- Task MainAgent asks two Workers separately and switches, Agent labels and history are not serialized.
- Blocking local model fixtures keep Worker running; submit from the main input box of the Worker `/btw`, After stopping the side channel, the Worker still displays real-time operation and its own pause button. Save partial answers from the side channel.
- Browser error / warning logs are empty.

Controllable fixtures are used to precisely verify concurrent timing without relying on the output speed of the real model. During debugging, two Worker runs check if an effective concurrency window is formed (task already ended / answer prematurely ended), correct the fixture and redo it; these initial operations are not counted as valid passes.

## Real model conversation

Prioritize Probing, `grok-4.6`, OpenAI Compatible Interface, `http://127.0.0.1:12580/tingly/openai`. Probe HTTP 200, return model name `grok-4.6` And `READY`, Time Taken 2.82 Seconds. Preferably available, so Tingly is not enabled. `glm` Or knowledge base `glm-5.3` What assets and test marks were provided previously?

| Scenario | Actual result. |
| --- | --- |
| During the main session, ask for assets, targets, tags | Support `redhaze.top`, Read and summarize targets on the homepage.`BTW-REAL-0910`; Side completion, 16.97 Second |
| After completing the main session, ask the tool for its basis. | Correctly reference WebFetch 200 and curl redirect 301 → 302 → 200, Page title; 7.24 Second |
| Side requirement for Bash to create test files | Refuse execution, target file not created; 7.74 Second |
| The side-channel remains unchanged after completion. | Main transcript SHA-256 consistent with main activity record; side tool execution count is 0. |
| Truly stop / restart Go service subsequent questions | Retain previous 3 side history entries, directly answer assets, tags, and titles from persistent snapshot without rerunning main agent. |
| New sessions use Grok non-streaming configuration. | Correctly answered asset and `ATOMIC-0910`; Return and save usage: input 11734, output 138, cache_read 11520 |

The main session for an asset case uses WebFetch and Bash/curl to read the public homepage, with the landing page being `https://id.redhaze.top/home`, Title is“RedHaze Group · Global Comprehensive Group Portal”. Bash Temporarily store response in local test file; no write to remote. This fact is that“No execution tool in the side channel”Separate verification.

Main transcript validation value:`e7e61f135a4a120954b539f357e8c4205d7d5cd7460dcaf3dc0fd066463e1d00`.

**Usage limit:** Tingly The Grok stream response did not return usage. Send it directly instead. `stream_options.include_usage=true` Validation, HTTP 200, 12 data frames, 0 usage frames. Therefore, a 0 in stream testing indicates that the endpoint does not provide usage; it cannot be interpreted as no billing. Non-stream usage and fixture failures/cancellations are correctly saved.

## Qwen Review

Review Model, `qwen-flash`, OpenAI Compatible Interface, `https://dashscope.aliyuncs.com/compatible-mode/v1`, HTTP 200. Provides the first three real-side conversations, main session tool basis, and engineering assertions; returns `verdict: accept`, `concerns: []`, Consider the answer consistent with asset, tag, page read evidence, side tools reject constraints. Review usage: prompt 6625, completion 312, total 6937.

This Qwen review scope does not include later appended service restarts and non-stream tests. Qwen for“No write access”The scope is too broad: The main session curl did indeed create a local response temporary file, which has been clearly recorded. Concurrency, zero-tool execution, and transcript isolation are determined by engineering assertions; model review only assists in evaluating the quality of answers.
