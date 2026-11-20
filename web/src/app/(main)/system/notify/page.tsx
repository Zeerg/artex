"use client";

import * as React from "react";

import { BellIcon, PlusIcon, SendIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { NotificationChannel, NotificationFilter, NotificationMeta } from "@/lib/types";

import {
 CHANNEL_FIELDS,
 type ChannelForm,
 emptyForm,
 KIND_LABEL,
 parseIDs,
 parseKeywords,
 parseKV,
 SEVERITY_OPTIONS,
} from "./_components/channel-fields";
import { ConfigField, FilterSummary } from "./_components/channel-form";
import { DeliveryList } from "./_components/delivery-list";
import { formatBacklog, StatTile } from "./_components/stat-tile";

// This page is responsible for orchestration: loading data, maintaining form state, calling interfaces.
// Field definition and parsing in _components/channel-fields.ts, Controls and filter summary at
// _components/channel-form.tsx, Delivery records are in _components/delivery-list..tsx——
// Splitting them apart because each can be read individually, while squeezed into one file this page approaches 1100 lines.
export default function NotifyPage() {
 const [meta, setMeta] = React.useState<NotificationMeta | null>(null);
 const [channels, setChannels] = React.useState<NotificationChannel[]>([]);
 const [tab, setTab] = React.useState<"channels" | "deliveries">("channels");

 const [open, setOpen] = React.useState(false);
 const [editing, setEditing] = React.useState<NotificationChannel | null>(null);
 const [form, setForm] = React.useState<ChannelForm>(emptyForm("dingtalk"));
 const [saving, setSaving] = React.useState(false);
 const [testing, setTesting] = React.useState(false);

 const [globalSaving, setGlobalSaving] = React.useState(false);
 const [baseURL, setBaseURL] = React.useState("");
 const [digestMin, setDigestMin] = React.useState("");

 const load = React.useCallback(() => {
 api
 .notifyMeta()
 .then((m) => {
 setMeta(m);
 setBaseURL(m.public_base_url);
 setDigestMin(m.digest_interval_min);
 })
 .catch((e) => toast.error("Failed to read push configuration:" + (e as Error).message));
 // Report channel list load failure: silent failures will display as 'no channels',
 // Users might think the configuration is lost, which is more alarming than a direct error.
 api
 .notifyChannels()
 .then(setChannels)
 .catch((e) => toast.error("Failed to read channel list:" + (e as Error).message));
 }, []);
 React.useEffect(() => {
 load();
 }, [load]);

 function setF(patch: Partial<ChannelForm>) {
 setForm((f) => ({ ...f, ...patch }));
 }
 function setCfg(key: string, value: unknown) {
 setForm((f) => ({ ...f, config: { ...f.config, [key]: value } }));
 }

 function openAdd() {
 setEditing(null);
 setForm(emptyForm(meta?.kinds[0]?.kind?? "dingtalk"));
 setOpen(true);
 }

 function openEdit(ch: NotificationChannel) {
 setEditing(ch);
 // filter In the backend, it is a Go structure and always serialized into an object (not null), so no fallback is needed.
 const f = ch.filter;
 setForm({
 name: ch.name,
 kind: ch.kind,
 mode: ch.mode,
 enabled: ch.enabled,
 ratePerMin: String(ch.rate_per_min),
 // Backend echoes the config with credentials masked; put it as is into the form, submit and return as is,
 // The backend retains the original value from the database based on this.
 config: { ...ch.config },
 minSeverity: f.min_severity?? "",
 includeText: (f.vulnclass_include?? []).join("\n"),
 excludeText: (f.vulnclass_exclude?? []).join("\n"),
 taskIDsText: (f.task_ids?? []).join(","),
 assetIDsText: (f.asset_ids?? []).join(","),
 onStatusChange: f.on_status_change?? false,
 });
 setOpen(true);
 }

 // buildConfig Convert form state to channel config.
 //
 // Unique rule, two types of values:
 // - Mask Value ("__masked__...")Return as is → The backend interprets it as 'this field hasn't changed, keep the original value from the database'
 // - All other fields are submitted based on user input, an empty string clears the field
 //
 // The reason credentials fields are not specially treated (like 'skip if credential field is empty') is because that would make users**Cannot Clear**
 // A faulty key.——There is no operation on the interface that expresses 'I want to delete it.' Under the current rules,
 // Clearing the input box clears this field, semantically unique and user-controlled.
 // Masked values do not appear in input fields (see ConfigField), so 'text in the box' is always equal to
 // 'User-provided'.
 function buildConfig(): Record<string, unknown> {
 const defs = CHANNEL_FIELDS[form.kind]?? [];
 const out: Record<string, unknown> = {};
 for (const d of defs) {
 const raw = form.config[d.key];
 if (d.kind === "switch") {
 out[d.key] = raw === true;
 continue;
 }
 if (typeof raw === "string" && raw.startsWith("__masked__")) {
 out[d.key] = raw;
 continue;
 }
 if (d.kind === "number") {
 const n = Number(raw);
 out[d.key] = Number.isFinite(n) && n > 0? n: 0;
 continue;
 }
 if (d.kind === "kv") {
 out[d.key] = parseKV(String(raw?? ""));
 continue;
 }
 if (d.kind === "list") {
 out[d.key] = String(raw?? "")
 .split(/[\s,, ]+/)
 .map((s) => s.trim())
 .filter(Boolean);
 continue;
 }
 out[d.key] = String(raw?? "").trim();
 }
 return out;
 }

 function buildFilter(): NotificationFilter {
 return {
 min_severity: form.minSeverity || undefined,
 vulnclass_include: parseKeywords(form.includeText),
 vulnclass_exclude: parseKeywords(form.excludeText),
 task_ids: parseIDs(form.taskIDsText),
 asset_ids: parseIDs(form.assetIDsText),
 on_status_change: form.onStatusChange,
 };
 }

 async function saveForm() {
 if (!form.name.trim()) {
 toast.error("Please enter a channel name");
 return;
 }
 setSaving(true);
 try {
 const payload = {
 name: form.name.trim(),
 kind: form.kind,
 mode: form.mode,
 enabled: form.enabled,
 config: buildConfig(),
 filter: buildFilter(),
 rate_per_min: form.ratePerMin.trim() === ""? undefined: Number(form.ratePerMin),
 };
 if (editing) {
 await api.notifyUpdateChannel(editing.id, payload);
 toast.success("Saved");
 setOpen(false);
 } else {
 await api.notifyCreateChannel(payload);
 toast.success("Channel added");
 setOpen(false);
 }
 load();
 } catch (e) {
 toast.error("Save failed: " + (e as Error).message);
 } finally {
 setSaving(false);
 }
 }

 async function testChannel() {
 if (!editing) return;
 setTesting(true);
 try {
 const r = await api.notifyTestChannel(editing.id);
 toast.success(`Test message sent ()${r.latency_ms} ms), Please confirm in the group`);
 } catch (e) {
 // The backend returns the original error from the channel truthfully. This is the only clue for troubleshooting configuration and is displayed in its original form.
 toast.error("Test failed:" + (e as Error).message, { duration: 12000 });
 } finally {
 setTesting(false);
 }
 }

 async function removeChannel(ch: NotificationChannel) {
 try {
 await api.notifyDeleteChannel(ch.id);
 toast.success(`Deleted:${ch.name}`);
 setOpen(false);
 load();
 } catch (e) {
 toast.error("Delete failed: " + (e as Error).message);
 }
 }

 async function toggleEnabled(ch: NotificationChannel) {
 try {
 await api.notifyUpdateChannel(ch.id, { enabled:!ch.enabled });
 load();
 } catch (e) {
 toast.error("Operation failed: " + (e as Error).message);
 }
 }

 async function toggleGlobal(on: boolean) {
 setGlobalSaving(true);
 try {
 await api.setSettings({ notify_enabled: on });
 setMeta((m) => (m? { ...m, enabled: on }: m));
 toast.success(on? "Push enabled;": "Push paused");
 } catch (e) {
 toast.error("Operation failed: " + (e as Error).message);
 } finally {
 setGlobalSaving(false);
 }
 }

 async function saveGlobal() {
 setGlobalSaving(true);
 try {
 const patch: Record<string, unknown> = { notify_public_base_url: baseURL.trim() };
 const n = Number(digestMin);
 if (Number.isFinite(n) && n > 0) patch.notify_digest_interval_min = n;
 await api.setSettings(patch);
 toast.success("Saved");
 load();
 } catch (e) {
 toast.error("Save failed: " + (e as Error).message);
 } finally {
 setGlobalSaving(false);
 }
 }

 const fields = CHANNEL_FIELDS[form.kind]?? [];
 const secretKeys = new Set(meta?.kinds.find((k) => k.kind === form.kind)?.secret_keys?? []);
 const defaultRate = meta?.kinds.find((k) => k.kind === form.kind)?.default_rate_per_min?? 0;

 return (
 <div className="flex flex-1 flex-col gap-4 md:gap-6">
 <div className="flex items-start justify-between gap-4">
 <div>
 <h1 className="text-xl font-semibold tracking-tight">Notification Push</h1>
 <p className="text-muted-foreground text-sm">
 When a vulnerability is discovered, push it to DingTalk / Feishu / Enterprise WeChat, etc. · Each channel can independently set push timing and filter rules
 </p>
 </div>
 {meta && (
 // Use div instead of label: Switch has built-in aria-label, wrap with another layer of label
 // Neither associated with any native controls nor making clicking text look like it should switch.
 <div className="flex shrink-0 items-center gap-2 text-sm">
 <span className="text-muted-foreground">Master Switch</span>
 <Switch
 checked={meta.enabled}
 disabled={globalSaving}
 onCheckedChange={toggleGlobal}
 aria-label="Push total switch"
 />
 </div>
)}
 </div>

 {meta && (
 <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
 <StatTile label="Channel" value={`${meta.stats.channels_on} / ${meta.stats.channels}`} hint="Enable / Total" />
 <StatTile label="Delivered Today" value={String(meta.stats.sent_today)} />
 <StatTile label="Pending Send" value={String(meta.stats.pending)} />
 <StatTile label="Success" value={String(meta.stats.failed)} tone={meta.stats.failed > 0? "red": undefined} />
 <StatTile
 label="Longest backlog"
 value={formatBacklog(meta.stats.backlog_age_ms)}
 // Stale age is much more useful than the number of stales: 3 stales can range from 3 seconds to 3 hours.
 hint={meta.stats.backlog_age_ms > 5 * 60_000? "Push may be stuck": undefined}
 tone={meta.stats.backlog_age_ms > 5 * 60_000? "red": undefined}
 />
 </div>
)}

 <Card className="gap-3">
 <CardHeader>
 <CardTitle className="text-base">Global Settings</CardTitle>
 </CardHeader>
 <CardContent className="grid gap-4 sm:grid-cols-2">
 <div className="grid gap-2">
 <Label htmlFor="n-base">Return Link Address</Label>
 <Input
 id="n-base"
 placeholder="https://artex.example.com"
 value={baseURL}
 onChange={(e) => setBaseURL(e.target.value)}
 />
 <p className="text-muted-foreground text-xs">The address pointed to by the 'View Details' button in the message. Leave it empty if no button is needed.</p>
 </div>
 <div className="grid gap-2">
 <Label htmlFor="n-digest">Summary cycle (minutes)</Label>
 <Input
 id="n-digest"
 type="number"
 min={1}
 max={1440}
 placeholder="30"
 value={digestMin}
 onChange={(e) => setDigestMin(e.target.value)}
 />
 <p className="text-muted-foreground text-xs">Only effective for 'summary' mode channels.</p>
 </div>
 <div className="sm:col-span-2">
 <Button onClick={saveGlobal} disabled={globalSaving}>
 Save global settings
 </Button>
 </div>
 </CardContent>
 </Card>

 <Tabs value={tab} onValueChange={(v) => setTab(v as "channels" | "deliveries")} className="flex flex-col gap-4">
 <TabsList>
 <TabsTrigger value="channels">Channel</TabsTrigger>
 <TabsTrigger value="deliveries">Delivery Records</TabsTrigger>
 </TabsList>

 <TabsContent value="channels">
 <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
 <button
 type="button"
 onClick={openAdd}
 className="text-foreground/70 border-foreground/70 hover:bg-muted/60 hover:shadow-sm flex min-h-[130px] flex-col items-center justify-center gap-2 rounded-xl border border-dashed transition"
 >
 <PlusIcon className="size-6" />
 <span className="text-sm">Add channel</span>
 </button>

 {channels.map((ch) => (
 <Card
 key={ch.id}
 onClick={() => openEdit(ch)}
 className="hover:border-primary/60 cursor-pointer gap-3 transition hover:shadow-sm"
 >
 <CardHeader>
 <div className="flex items-center gap-2">
 <BellIcon className="text-muted-foreground size-4 shrink-0" />
 <CardTitle className="truncate text-base">{ch.name}</CardTitle>
 {/* The entire card is clickable (to enter edit mode), so these two controls must each swallow the bubble.
 Otherwise, toggling/deleting will trigger editing. Attach stopPropagation to the control itself
 On themselves, not wrapped in a div: wrapping div will create a 'look like interactive but no
 Static elements of 'Role', which trigger an a11y alert and make semantic sense. */}
 <div className="ml-auto flex items-center gap-2">
 <Switch
 checked={ch.enabled}
 onCheckedChange={() => toggleEnabled(ch)}
 onClick={(e) => e.stopPropagation()}
 aria-label="Enable"
 />
 <Button
 size="icon"
 variant="outline"
 aria-label="Selected"
 onClick={(e) => {
 e.stopPropagation();
 // void Explicitly discard Promise: removeChannel catches its own error and toasts,
 // No need for await (onClick is not async).
 void removeChannel(ch);
 }}
 >
 <Trash2Icon className="text-destructive" />
 </Button>
 </div>
 </div>
 </CardHeader>
 <CardContent className="grid gap-3">
 <div className="flex flex-wrap items-center gap-2">
 <Badge variant="outline">{KIND_LABEL[ch.kind]?? ch.kind}</Badge>
 <Badge variant="outline">{ch.mode === "digest"? "Summarize": "Default"}</Badge>
 {!ch.enabled && <Badge variant="outline">Disabled</Badge>}
 </div>
 <FilterSummary filter={ch.filter} />
 </CardContent>
 </Card>
))}
 </div>
 </TabsContent>

 <TabsContent value="deliveries">
 <DeliveryList channels={channels} />
 </TabsContent>
 </Tabs>

 <Sheet open={open} onOpenChange={setOpen}>
 <SheetContent side="right" className="w-full data-[side=right]:sm:max-w-lg">
 <SheetHeader>
 <SheetTitle>{editing? editing.name: "Add notification channel"}</SheetTitle>
 <SheetDescription>
 {KIND_LABEL[form.kind]?? form.kind}
 {defaultRate > 0? ` · Default Rate Limit ${defaultRate} Per Minute`: " · Unlimited Rate Limiting"}
 </SheetDescription>
 </SheetHeader>

 <div className="flex min-h-0 flex-1 flex-col overflow-y-auto px-4">
 <div className="grid gap-4 py-4">
 <div className="grid gap-2">
 <Label>Channel Type</Label>
 <Select
 value={form.kind}
 onValueChange={(v) => {
 // Changing type means switching to a new set of credential fields, cannot merge old configurations.
 setF({ kind: v, config: {} });
 }}
 disabled={!!editing}
 >
 <SelectTrigger>
 <SelectValue />
 </SelectTrigger>
 <SelectContent>
 {(meta?.kinds?? []).map((k) => (
 <SelectItem key={k.kind} value={k.kind}>
 {KIND_LABEL[k.kind]?? k.kind}
 </SelectItem>
))}
 </SelectContent>
 </Select>
 {editing && (
 <p className="text-muted-foreground text-xs">
 Channel typenot modifiable——Changing the type is equivalent to changing a set of credentials; please create a new channel.
 </p>
)}
 </div>

 <div className="grid gap-2">
 <Label htmlFor="n-name">Channel Name</Label>
 <Input
 id="n-name"
 placeholder="Emergency response group / Daily broadcast group."
 value={form.name}
 onChange={(e) => setF({ name: e.target.value })}
 />
 </div>

 {fields.length === 0? (
 <p className="text-muted-foreground text-sm">
 The form for this channel has not been defined (frontend missing CHANNEL_FIELDS entries), please complete it before retrying.
 </p>
): (
 fields.map((d) => (
 <ConfigField
 key={d.key}
 def={d}
 value={form.config[d.key]}
 isSecret={secretKeys.has(d.key)}
 onChange={(v) => setCfg(d.key, v)}
 />
))
)}

 <div className="grid gap-2">
 <Label>Push Timing</Label>
 <Select value={form.mode} onValueChange={(v) => setF({ mode: v as "realtime" | "digest" })}>
 <SelectTrigger>
 <SelectValue />
 </SelectTrigger>
 <SelectContent>
 <SelectItem value="realtime">Default · Each vulnerability on a separate line</SelectItem>
 <SelectItem value="digest">Summarize · Merge into one per cycle</SelectItem>
 </SelectContent>
 </Select>
 <p className="text-muted-foreground text-xs">
 To do 'high-risk real-time, others summary', create two channels: one real-time + High-risk threshold, one summary. + No level limit.
 </p>
 </div>

 <div className="grid gap-2">
 <Label htmlFor="n-rate">Rate limit (items/minute)</Label>
 <Input
 id="n-rate"
 type="number"
 min={0}
 placeholder={defaultRate > 0? String(defaultRate): "0 = Unlimited"}
 value={form.ratePerMin}
 onChange={(e) => setF({ ratePerMin: e.target.value })}
 />
 <p className="text-muted-foreground text-xs">
 Leave empty for channel default value; 0 means no rate limiting. Exceeding limit will delay sending rather than drop messages.
 </p>
 </div>

 <div className="border-t pt-4">
 <p className="mb-3 text-sm font-medium">Filter rules (leave empty for no filtering)</p>
 <div className="grid gap-4">
 <div className="grid gap-2">
 <Label>Lowest level</Label>
 <Select
 value={form.minSeverity || "all"}
 onValueChange={(v) => setF({ minSeverity: v === "all"? "": v })}
 >
 <SelectTrigger>
 <SelectValue />
 </SelectTrigger>
 <SelectContent>
 {SEVERITY_OPTIONS.map((o) => (
 <SelectItem key={o.value || "all"} value={o.value || "all"}>
 {o.label}
 </SelectItem>
))}
 </SelectContent>
 </Select>
 </div>
 <div className="grid gap-2">
 <Label htmlFor="n-inc">Only push these vulnerability types</Label>
 <Textarea
 id="n-inc"
 placeholder={"SQLCompleted\nCommand Execution"}
 value={form.includeText}
 onChange={(e) => setF({ includeText: e.target.value })}
 />
 <p className="text-muted-foreground text-xs">
 One keyword per line, case-insensitive substring matching. Leave empty=All types.
 </p>
 </div>
 <div className="grid gap-2">
 <Label htmlFor="n-exc">Exclude these vulnerability types</Label>
 <Textarea
 id="n-exc"
 placeholder={"Information Leakage"}
 value={form.excludeText}
 onChange={(e) => setF({ excludeText: e.target.value })}
 />
 <p className="text-muted-foreground text-xs">Exclusion takes precedence over inclusion: When both are hit, it will be excluded.</p>
 </div>
 <div className="grid gap-2">
 <Label htmlFor="n-tasks">Limit task ID.</Label>
 <Input
 id="n-tasks"
 placeholder="1, 2, 3"
 value={form.taskIDsText}
 onChange={(e) => setF({ taskIDsText: e.target.value })}
 />
 </div>
 <div className="grid gap-2">
 <Label htmlFor="n-assets">Limit asset ID</Label>
 <Input
 id="n-assets"
 placeholder="10, 11"
 value={form.assetIDsText}
 onChange={(e) => setF({ assetIDsText: e.target.value })}
 />
 <p className="text-muted-foreground text-xs">Task/Asset left empty.=Unlimited; requires intersection with vulnerabilities after filling in</p>
 </div>
 <div className="flex items-center gap-2 text-sm">
 <Switch
 checked={form.onStatusChange}
 onCheckedChange={(v) => setF({ onStatusChange: v })}
 aria-label="Receive status change"
 />
 Push on vulnerability disposition status change (only in real-time mode)
 </div>
 </div>
 </div>

 <div className="flex items-center gap-2 text-sm">
 <Switch checked={form.enabled} onCheckedChange={(v) => setF({ enabled: v })} aria-label="Enable" />
 Enable this channel
 </div>
 </div>

 <div className="flex gap-2 pt-2 pb-6">
 <Button onClick={saveForm} disabled={saving}>
 {editing? "Save": "Add"}
 </Button>
 {editing && (
 <Button variant="outline" onClick={testChannel} disabled={testing}>
 <SendIcon /> Send test message
 </Button>
)}
 </div>
 </div>
 </SheetContent>
 </Sheet>
 </div>
);
}
