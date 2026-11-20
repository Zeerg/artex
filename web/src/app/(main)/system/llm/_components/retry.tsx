"use client";

// LLM Common components for retry configuration: five levels of retries with their respective 'number + Interval.
//
// Five levels from inside out: Establish connection.(SDK) → Empty response(SDK) → Same as Provider Security Window → Polling Circuit Breaker → Intent rerun.
// The first three layers follow the endpoint, so each model configuration can override global defaults; the last two are process-level, with only one copy globally.
//
// All inputs follow the same set of 'leave empty'. = Without semantic configuration, it will not interfere with the backend database..RetryRule Consistent:
// Count Empty/0 = Use built-in defaults | -1 = Close this layer's retry | >0 = Use this number of times;
// Interval Empty/0 = use this layer's original exponential backoff | >0 = switch to this fixed millisecond interval

import * as React from "react";

import { Loader2Icon, SaveIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import type { LLMRetryOverride, LLMRetryPolicy, LLMRetryRule } from "@/lib/types";

export const ZERO_RULE: LLMRetryRule = { attempts: 0, interval_ms: 0 };
export const ZERO_OVERRIDE: LLMRetryOverride = {
 connect: ZERO_RULE,
 empty: ZERO_RULE,
 stream: ZERO_RULE,
};
const ZERO_POLICY: LLMRetryPolicy = {
 ...ZERO_OVERRIDE,
 breaker: ZERO_RULE,
 intent: ZERO_RULE,
};

type LayerMeta = {
 title: string;
 /** Where this retry happens, who executes it, */
 where: string;
 /** What kind of error would reach this level.——Specific to status codes, don't let people guess */
 trigger: string;
 /** Errors that look like but [do not] go through this layer save time filling in without response thinking it's a bug */
 skips?: string;
 desc: string;
 attemptsLabel: string;
 /** Default value for placeholder when count is left empty */
 defAttempts: number;
 /** Default strategy when interval is left empty, for placeholders */
 defInterval: string;
 /** Meaning of filling -1 times. */
 offHint: string;
};

export const RETRY_LAYERS = {
 connect: {
 title: "Reconnect Retry",
 where: "SDK · Before getting 200",
 trigger:
 "Unable to connect or haven't received 200: network layer errors like connection reset / read/write timeout / DNS failure, as well as HTTP 408, 429, 500, 502, 503, 504.",
 skips: "All other status codes (400 / 401 / 403 / 404 / 413 / 422 etc.) are deterministic rejections; resending will also fail and be thrown directly.",
 desc: "Resend the same request exactly. Once the stream starts (already received 200), it's no longer managed if interrupted mid-way.",
 attemptsLabel: "Retry Count",
 defAttempts: 3,
 defInterval: "0.5s→1s→2s Exponential (capped at 8s)",
 offHint: "-1 = Do not retry at all, fail immediately and throw up",
 },
 empty: {
 title: "Retry on empty response",
 where: "SDK · Only openai format",
 trigger:
 "HTTP 200, finish_reason It's a normal stop, but no content block is returned in the entire response——Gateway empty frames, thinking field dropped frames, sampling hiccups will look like this.",
 skips: "Those emptied by max_tokens truncation don't count (fix that by raising the output limit - resending just hits it again).",
 desc: "Resending the entire prompt makes it more expensive on long contexts; don't give many attempts.",
 attemptsLabel: "Retry Count",
 defAttempts: 2,
 defInterval: "0.5s→1s→2s Exponential (capped at 8s)",
 offHint: "-1 = Empty response passed through as is",
 },
 stream: {
 title: "Retry with the same provider security window",
 where: "This Project · Before unsubmitted output",
 trigger:
 "After the stream is established (get 200), problems occur: connection interruption, supplier overloaded, streamof 429 / 5xx error events——And a token hasn't been handed over to the caller yet.",
 skips:
 "Quota exhausted (402 / insufficient_quota, switch configuration through polling), context too long (413 / context length, compress), 400 / 401 / 403 / 404 / 422 are definitive rejections; do not retry.",
 desc: "Replaying the same request on the same configuration. Since no output has been delivered yet, replaying won't repeat model outputs or tool executions.",
 attemptsLabel: "Retry Count",
 defAttempts: 2,
 defInterval: "0.5s→1s Exponential (capped at 4s)",
 offHint: "-1 = A broken stream is directly handed over to the outer intent for rerunning.",
 },
 breaker: {
 title: "Polling Circuit Breaker",
 where: "This Project · Process-level, one global copy.",
 trigger:
 "Immediate failure (429, 5xx, network errors) that accumulates continuously until it reaches a threshold will cause a circuit breaker. Insufficient balance (402), key expiration (401 / 403), model non-existence (404) are certain failures that do not consider the threshold; they trigger a circuit breaker on the first occurrence.",
 skips: "Reset after one success, so occasional misconfigurations won't slowly accumulate to trigger a circuit breaker.",
 desc: "After a circuit breaker, enter cooling mode. During the cooling period, polling will skip directly to this configuration. The status is stored in the database and will not be lost on restart.",
 attemptsLabel: "Circuit breaker after consecutive failures",
 defAttempts: 3,
 defInterval: "1min→5min→30min Gradient",
 offHint: "-1 = Instant failures never trip the circuit breaker (deterministic failures still trip)",
 },
 intent: {
 title: "Retry Intent",
 where: "This Project · Process-level, one global copy.",
 trigger:
 "The first few layers didn't catch it: worker ends with model_error.——Inner retries have exhausted, or delivery has already started outputting before being interrupted (at which point replay is unsafe, and the entire thing must be redone).",
 skips: "Quota depletion has been handled by polling for configuration changes; no need to rerun here. When the task is paused/terminated/finishing, it immediately yields without occupying backoff time.",
 desc: "Rerun the entire intent from the start. It's the outermost layer; one re-run means the number of times inside layers will be multiplied again.",
 attemptsLabel: "Retry Count",
 defAttempts: 2,
 defInterval: "Fixed 3s",
 offHint: "-1 = Do not rerun, directly judge the intent as blocked",
 },
} satisfies Record<string, LayerMeta>;

type LayerKey = keyof typeof RETRY_LAYERS;

/** Milliseconds in plain language, used only for echoing next to the input box to avoid counting zeros. */
function humanMs(ms: number) {
 if (!Number.isFinite(ms) || ms <= 0) return "";
 if (ms < 1000) return `${ms}ms`;
 if (ms < 60_000) return `${Number((ms / 1000).toFixed(2))}s`;
 return `${Number((ms / 60_000).toFixed(2))}min`;
}

/** Controlled numeric input: empty string ↔ 0, Intermediate state ("-", "1e")It will remain unchanged locally and will not disturb the parent. */
function NumField({
 id,
 value,
 onChange,
 placeholder,
 min,
}: {
 id: string;
 value: number;
 onChange: (n: number) => void;
 placeholder: string;
 min: number;
}) {
 const [text, setText] = React.useState(value === 0? "": String(value));
 // Follow when the parent changes a whole set of values (read policy, switch configuration); don't go here when typing yourself,
 // Because at that time value was already equal to the result of local text parsing.
 React.useEffect(() => {
 const incoming = value === 0? "": String(value);
 setText((cur) => (Number(cur || 0) === value? cur: incoming));
 }, [value]);
 return (
 <Input
 id={id}
 type="number"
 min={min}
 className="w-28 shrink-0"
 value={text}
 placeholder={placeholder}
 onChange={(e) => {
 setText(e.target.value);
 const n = Number(e.target.value);
 onChange(e.target.value.trim() === "" ||!Number.isFinite(n)? 0: Math.trunc(n));
 }}
 />
);
}

/** Two knobs for one retry. idPrefix is used to keep the label's htmlFor when it appears multiple times on the same page. */
export function RetryRuleFields({
 layer,
 idPrefix,
 value,
 onChange,
 compact,
}: {
 layer: LayerKey;
 idPrefix: string;
 value: LLMRetryRule;
 onChange: (r: LLMRetryRule) => void;
 /** true = Compact version in the configuration drawer: omit expanded explanation, leaving only 'What error would reach this level.' */
 compact?: boolean;
}) {
 const meta = RETRY_LAYERS[layer];
 const human = humanMs(value.interval_ms);
 return (
 <div className={compact? "grid gap-2": "grid gap-3 rounded-lg border p-3"}>
 <div className="grid gap-0.5">
 <div className="flex flex-wrap items-baseline gap-2">
 <Label className="text-sm">{meta.title}</Label>
 <span className="text-muted-foreground text-xs">{meta.where}</span>
 </div>
 {/* Which errors will reach this layer, specifically by status code.——Turning a knob but seeing no effect, likely due to an error not falling in this layer. */}
 <p className="text-muted-foreground text-xs">
 <span className="font-medium text-foreground">Trigger</span>: {meta.trigger}
 </p>
 {!compact && meta.skips && (
 <p className="text-muted-foreground text-xs">
 <span className="font-medium text-foreground">Skip This Layer</span>: {meta.skips}
 </p>
)}
 {!compact && <p className="text-muted-foreground text-xs">{meta.desc}</p>}
 </div>
 <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
 <div className="flex items-center gap-2">
 <Label htmlFor={`${idPrefix}-${layer}-n`} className="text-muted-foreground text-xs">
 {meta.attemptsLabel}
 </Label>
 <NumField
 id={`${idPrefix}-${layer}-n`}
 min={-1}
 value={value.attempts}
 placeholder={`Replacement ${meta.defAttempts}`}
 onChange={(n) => onChange({ ...value, attempts: n })}
 />
 </div>
 <div className="flex items-center gap-2">
 <Label htmlFor={`${idPrefix}-${layer}-ms`} className="text-muted-foreground text-xs">
 Interval ms
 </Label>
 <NumField
 id={`${idPrefix}-${layer}-ms`}
 min={0}
 value={value.interval_ms}
 placeholder="Default Backoff"
 onChange={(n) => onChange({ ...value, interval_ms: n })}
 />
 <span className="text-muted-foreground text-xs">{human? `Fixed ${human}`: meta.defInterval}</span>
 </div>
 </div>
 {!compact && <p className="text-muted-foreground text-xs">leave empty = Use Default;{meta.offHint}. </p>}
 </div>
);
}

/** Three layers of coverage in model configuration drawer (following endpoint's three layers). */
export function ProfileRetryFields({
 value,
 onChange,
}: {
 value: LLMRetryOverride;
 onChange: (o: LLMRetryOverride) => void;
}) {
 return (
 <div className="grid gap-3 rounded-lg border p-3">
 <div className="grid gap-0.5">
 <Label className="text-sm">Retry overwrite</Label>
 <p className="text-muted-foreground text-xs">
 Only for this configuration, overriding the global default in 'retry and backoff'. Leave each cell empty = Follow globally; fill in -1 for times = Turn off this layer's retry;
 If filled at intervals, use a fixed interval instead of exponential backoff. Circuit breaker and intent rerun are process-level, can only be called on the global page.
 </p>
 </div>
 {(["connect", "empty", "stream"] as const).map((k) => (
 <div key={k} className="border-t pt-3 first:border-t-0 first:pt-0">
 <RetryRuleFields
 compact
 layer={k}
 idPrefix="pf"
 value={value[k]}
 onChange={(r) => onChange({ ...value, [k]: r })}
 />
 </div>
))}
 </div>
);
}

/** 'Retry and backoff tab: global default values for all five levels. */
export function RetryPolicyPanel() {
 const [policy, setPolicy] = React.useState<LLMRetryPolicy>(ZERO_POLICY);
 const [loading, setLoading] = React.useState(true);
 const [saving, setSaving] = React.useState(false);

 const load = React.useCallback(async () => {
 setLoading(true);
 try {
 const p = await api.llmRetryPolicy();
 setPolicy({ ...ZERO_POLICY, ...p });
 } catch (e) {
 toast.error(`Failed to read retry strategy:${(e as Error).message}`);
 } finally {
 setLoading(false);
 }
 }, []);

 React.useEffect(() => {
 void load();
 }, [load]);

 async function save() {
 if (saving) return;
 setSaving(true);
 try {
 // The backend will clamp out-of-bounds values back to the range and return them. Directly refresh using the returned value, what you see is what you store.
 const saved = await api.saveLLMRetryPolicy(policy);
 setPolicy({ ...ZERO_POLICY, ...saved });
 toast.success("Saved, immediately effective (this round's call still uses old parameters)");
 } catch (e) {
 toast.error(`Save failed: ${(e as Error).message}`);
 } finally {
 setSaving(false);
 }
 }

 const set = (k: LayerKey) => (r: LLMRetryRule) => setPolicy((p) => ({ ...p, [k]: r }));

 if (loading) {
 return (
 <div className="flex items-center gap-2 rounded-lg border border-dashed p-10 text-muted-foreground text-sm">
 <Loader2Icon className="size-4 animate-spin" /> Read Retry Strategy...
 </div>
);
 }

 return (
 <div className="grid gap-4">
 <div className="rounded-lg border bg-muted/30 p-3 text-muted-foreground text-xs leading-relaxed">
 One model call failure will be retried through five layers, from inside out:
 <span className="text-foreground"> Connect → Empty response → Same as Provider Security Window → Polling Circuit Breaker → Retry Intent</span>
 . Only when the inner layer is exhausted will the outer layer be considered, so the number of times is
 <span className="text-foreground">Multiply</span>
 To——Fill every layer to the fullest, one shake can burn dozens of requests.
 All left blank means the current default value, exactly the same behavior as when this page does not exist. The first three levels can be individually overridden in each model configuration.
 </div>

 <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
 {(Object.keys(RETRY_LAYERS) as LayerKey[]).map((k) => (
 <RetryRuleFields key={k} layer={k} idPrefix="gl" value={policy[k]} onChange={set(k)} />
))}
 </div>

 <div className="flex gap-2">
 <Button onClick={save} disabled={saving}>
 {saving? <Loader2Icon className="animate-spin" />: <SaveIcon />}
 Save
 </Button>
 <Button variant="outline" onClick={() => setPolicy(ZERO_POLICY)} disabled={saving}>
 Restore all defaults
 </Button>
 </div>
 </div>
);
}
