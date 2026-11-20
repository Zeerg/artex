# Off-road long conversation with context budget

2026-09-11 Fix. The original implementation treated the number of characters in the request JSON as tokens and inherited the main task's 32K output reservation, so it would prematurely reject normal side-channel issues when HTML, JS, or tool results weremore.

## Open source implementation verification

- [Grok CLI Sideload context](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/agent/agent.ts#L739): Extract fragments from the nearest user, assistant text; character budget about 2000, each up to 400 characters. No continuous side-by-side question history maintained at this path.
- [Grok CLI independent request](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/utils/side-question.ts): Independent cancellation signal; Output limit is 2048 tokens when model supports; No tools provided.
- [Grok CLI Main session compression](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/agent/compaction.ts): Estimate tokens, retain recent content, update new content into old summaries, and handle cross-round truncation.
- [OpenCode Session Compression](https://github.com/anomalyco/opencode/blob/b3f1a96c6dd7adeb28b36dd11add1998fc84d67b/packages/core/src/session/compaction.ts): Full request estimation, output/buffer reservation, recent content with scrolling summary, no tool summary requests; this implementation defaults to a recent budget of 8000 tokens and an upper limit of 4096 tokens for summary output.
- [OpenCode Overflow recovery.](https://github.com/anomalyco/opencode/blob/b3f1a96c6dd7adeb28b36dd11add1998fc84d67b/packages/core/src/session/runner/llm.ts): Try to recover from overflow only when the assistant output has not yet started. The call after recovery will no longer enter the same overflow recovery path.

ARTEX Adopt the approach of independent output budget, recent content, and limited recovery. Maintain norma v0.3.6 Structured messages paired with tools, not copying Grok's text excerpts; do not write OpenCode's main session compressed events to ARTEX's main transcript.

## Request budget and execution

- Messages use norma's content-based UTF-8 byte estimation and a 4/3 margin; additional system prompt, tool schema, and message encapsulation overhead is accounted for. The estimation is not an exact token count by the model.
- Default side output is at most 8192 tokens, also not exceeding the main configuration's set output limit. Available service environment variables `ARTEX_BTW_MAX_OUTPUT_TOKENS` Set to 256–32768 The upper limit; it will not modify the product's default model or primary task parameters.
- Input budget as context window minus output limit and safety margin; unknown windows use platform default 200K. Safety margin is 5%, Minimum 128, maximum 8192 tokens.
- Successful questions load up to 20 groups in ascending order. Up to 20 original groups are retained, with a token budget of at most 1/4 of the input budget and no more than 16K.
- Excessive Q&A updates to rolling summary. The summary includes historical source and context time; historical assistant answers are not equivalent to new tool evidence, preferring the latest main snapshot in case of conflict.
- When the main context is still too long, only summarize old messages in the copy, retaining up to 8K tokens recently; the truncation point will not split tool calls and results. Large groups as a whole will enter summary.
- Summarize input by UTF-8 safe chunking based on the remaining window; output limit is 2048 tokens. Empty summaries, truncations, tool calls, or exceeding summary budget do not cache. A single side question can have up to 12 summary calls and is subject to a 120-second timeout restriction. Reaching the limit will fail explicitly, no infinite loop.
- If the model first returns context overflow and has not yet output text or tool calls, further reduce it and retry once; if estimated size does not decrease, stop recovery immediately. Other model errors and partial streaming outputs do not trigger this recovery.
- All usage consumed, including summary, failed attempts, and usage canceled at the time, is added to the same side-channel request. The provider cannot return usage but can only record zero; it cannot disguise estimates as actual usage.

## Persistence and interface

`side_question_sessions.memory` Save old Q&A summary, overwritten ordinal, and main context summary cached as a snapshot identity.`side_question_requests.context_info` Save preparation phase, actual playback group count, summary usage, and budget estimation.

The summary is saved only when the original request is still running and the cleanup version matches; clearing also clears the cache. Late writes do not recover cleaned data. New snapshots do not reuse old snapshot summaries. Summary fields are archived with v3 tasks; when recovering missing fields from old v3, fill in empty objects, v1/v2 continue to be compatible.

POST First accept and return the request, prepare to execute it in the background, not holding an access lock or database transaction. SSE/History shows preparation, organizing questions and answers, compressing copies, answering phase; compression failure is saved as the final state for this side-channel request. The frontend retains the error and restores the draft of this failed attempt without being obstructed by a floating notification; history polling no longer clears submitted errors.

## Verification Record,

- 19/20/21/50 Group playback, retain 20 old conclusions across groups, summary cache restart reuse: automated through.
- Very long Chinese answers and code context, chunked request budget, tool pairing, snapshots unchanged, new snapshot cache invalidated: automated pass.
- Summary failure/cancellation/truncation/long/tool return, clear competition, call limit, recover only once for overflow and partial stream not retried: automated through.
- Independent PostgreSQL pagination, restarts, v1/v2/v3 archiving, summary cache and budget metadata recovery, old v3 missing new fields, 20 parent sessions sharing four concurrent slots: Approved.
- Go Candidate service construction, frontend TypeScript checks, modification component Biome checks, and independent directory Next.js Production build: Passed.
- The built-in browser uses an independent UI harness at 1280×720 And 390×844 Validation sorting stage, summary range hint, draft recovery after failure, no floating error notifications, no horizontal overflow and no errors in the console. Temporary fixtures have been removed.
- Only read backplay the existing Worker snapshots on this machine. New budget checks pass; for example, Worker #3 The 293085 character snapshot is no longer misjudged and rejected by local character counting. This item has no external model calls.
- This private Worker snapshot was automatically approved for review in the Grok real conversation test and was not executed, not considered a pass.
- 2026-09-11 00:37 Restart this machine's backend as requested by the user, using the original database, data directory, and login configuration. Running file and candidate binary SHA-256 match; backend and frontend proxy are consistent. `/api/health` Both return normally.

Validation command (use independent test library only):

```sh
go test -race ./sidequestion ./db ./server -run 'TestSide|TestCheckpoint|TestSnapshot|TestBuildRequest|TestService|TestMainSide|TestTaskArchive' -count=1
go build ./cmd/artex
npx tsc --noEmit
npm run build -- --webpack
```

Frontend production builds use a separate copy to avoid overwriting the current preview. `.next`. Candidate service located at `/private/tmp/artex-btw-budget-candidate`, Copied to `/private/tmp/artex-btw-preview/artex` And start; original binary backup as the same directory's `artex.before-context-budget`.
