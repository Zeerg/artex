"use client";

import * as React from "react";

import { CpuIcon, FlaskConicalIcon, KeyboardIcon, RadioTowerIcon, SearchIcon, ShieldAlertIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import { CHAT_SEND_MODE_OPTIONS, type ChatSendMode, setChatSendMode, useChatSendMode } from "@/lib/chat-send-mode";
import type { Settings } from "@/lib/types";

import { UpdateCard } from "./_components/update-card";

export default function SystemSettingsPage() {
 const [trafficCapture, setTrafficCapture] = React.useState(false);
 const [agentTrafficBinding, setAgentTrafficBinding] = React.useState(false);
 const [webSearch, setWebSearch] = React.useState(false);
 const [backend, setBackend] = React.useState("ddgs");
 const [braveKeySet, setBraveKeySet] = React.useState(false);
 const [braveKeyInput, setBraveKeyInput] = React.useState("");
 const [tavilyKeySet, setTavilyKeySet] = React.useState(false);
 const [tavilyKeyInput, setTavilyKeyInput] = React.useState("");
 const [savingTavilyKey, setSavingTavilyKey] = React.useState(false);
 const [proxyInput, setProxyInput] = React.useState("");
 const [savingProxy, setSavingProxy] = React.useState(false);
 const [globalProxyInput, setGlobalProxyInput] = React.useState("");
 const [savingGlobalProxy, setSavingGlobalProxy] = React.useState(false);
 const [testing, setTesting] = React.useState(false);
 const [loaded, setLoaded] = React.useState(false);
 const [saving, setSaving] = React.useState(false);
 const [savingKey, setSavingKey] = React.useState(false);
 const [pyInterp, setPyInterp] = React.useState("");
 const [workers, setWorkers] = React.useState("3");
 const [savingWorkers, setSavingWorkers] = React.useState(false);
 // Operation constraint injection range(Default is on).
 const [injectPlanner, setInjectPlanner] = React.useState(true);
 const [injectWorker, setInjectWorker] = React.useState(true);
 // Experimental Features:noa Context compression(Default off).
 const [noaCompaction, setNoaCompaction] = React.useState(false);
 // Pure frontend preference: does not use /api/settings, directly reads/writes localStorage.
 const sendMode = useChatSendMode();

 const apply = React.useCallback((s: Settings) => {
 setTrafficCapture(!!s.traffic_capture);
 setAgentTrafficBinding(!!s.agent_traffic_binding);
 setWebSearch(!!s.web_search_enabled);
 setBackend(s.web_search_backend || "ddgs");
 setBraveKeySet(!!s.brave_key_set);
 setTavilyKeySet(!!s.tavily_key_set);
 setProxyInput(s.web_search_proxy?? "");
 setGlobalProxyInput(s.global_proxy?? "");
 setPyInterp(s.python_interpreter?? "");
 setWorkers(String(s.workers?? 3));
 setInjectPlanner(s.constraints_inject_planner!== false);
 setInjectWorker(s.constraints_inject_worker!== false);
 setNoaCompaction(!!s.noa_compaction);
 }, []);

 const saveWorkers = () => {
 const n = Number(workers);
 if (!Number.isInteger(n) || n <= 0) {
 toast.error("Concurrency must be an integer greater than 0.");
 return;
 }
 setSavingWorkers(true);
 api
 .setSettings({ workers: n })
 .then((s) => {
 apply(s);
 toast.success("Saved concurrent working agent count (effective for subsequent tasks)");
 })
 .catch((e) => toast.error("Save failed: " + (e as Error).message))
 .finally(() => setSavingWorkers(false));
 };

 const savePython = () => {
 setSaving(true);
 api
 .setSettings({ python_interpreter: pyInterp.trim() })
 .then((s) => {
 apply(s);
 toast.success("Python interpreter configuration saved");
 })
 .catch((e) => toast.error("Save failed: " + (e as Error).message))
 .finally(() => setSaving(false));
 };
 const detectPython = () => {
 setSaving(true);
 api
 .detectPython()
 .then((r) => setPyInterp(r.python_interpreter))
 .catch(() => undefined)
 .finally(() => setSaving(false));
 };

 React.useEffect(() => {
 api
 .settings()
 .then(apply)
 .catch(() => undefined)
 .finally(() => setLoaded(true));
 }, [apply]);

 const toggleTraffic = (v: boolean) => {
 setTrafficCapture(v); // optimistic
 setSaving(true);
 api
 .setSettings({ traffic_capture: v })
 .then(apply)
 .catch(() => setTrafficCapture(!v)) // revert on failure
 .finally(() => setSaving(false));
 };

 const toggleInjectPlanner = (v: boolean) => {
 setInjectPlanner(v); // optimistic
 api
 .setSettings({ constraints_inject_planner: v })
 .then(apply)
 .catch(() => setInjectPlanner(!v)); // revert on failure
 };

 const toggleAgentTrafficBinding = (v: boolean) => {
 setAgentTrafficBinding(v);
 setSaving(true);
 api
 .setSettings({ agent_traffic_binding: v })
 .then((s) => {
 apply(s);
 toast.success(v? "Agent auto-binding traffic has been enabled.": "Agent auto-binding traffic has been disabled.");
 })
 .catch((e) => {
 setAgentTrafficBinding(!v);
 toast.error(`Save failed: ${(e as Error).message}`);
 })
 .finally(() => setSaving(false));
 };

 const toggleInjectWorker = (v: boolean) => {
 setInjectWorker(v); // optimistic
 api
 .setSettings({ constraints_inject_worker: v })
 .then(apply)
 .catch(() => setInjectWorker(!v)); // revert on failure
 };

 const toggleNoaCompaction = (v: boolean) => {
 setNoaCompaction(v); // optimistic
 api
 .setSettings({ noa_compaction: v })
 .then((s) => {
 apply(s);
 toast.success(v? "Long context compression has been enabled on noa (effective for subsequent runs).": "No longer compresses noa context (restoring built-in compression).");
 })
 .catch((e) => {
 setNoaCompaction(!v); // revert on failure
 toast.error(`Save failed: ${(e as Error).message}`);
 });
 };

 // Persist a web-search patch (enable and/or backend). Optimistic with refetch.
 const saveWebSearch = (patch: Partial<Settings>) => {
 setSaving(true);
 api
 .setSettings(patch)
 .then((s) => {
 apply(s);
 toast.success("Network search configuration saved");
 })
 .catch((e) => {
 toast.error("Save failed: " + (e as Error).message);
 api
 .settings()
 .then(apply)
 .catch(() => undefined);
 })
 .finally(() => setSaving(false));
 };

 const saveBraveKey = () => {
 setSavingKey(true);
 api
 .setSettings({ brave_search_api_key: braveKeyInput })
 .then((s) => {
 apply(s);
 setBraveKeyInput("");
 toast.success("Brave API Key saved");
 })
 .catch((e) => toast.error("Save failed: " + (e as Error).message))
 .finally(() => setSavingKey(false));
 };

 const saveTavilyKey = () => {
 setSavingTavilyKey(true);
 api
 .setSettings({ tavily_search_api_key: tavilyKeyInput })
 .then((s) => {
 apply(s);
 setTavilyKeyInput("");
 toast.success("Tavily API Key saved.");
 })
 .catch((e) => toast.error("Save failed: " + (e as Error).message))
 .finally(() => setSavingTavilyKey(false));
 };

 const saveProxy = () => {
 setSavingProxy(true);
 api
 .setSettings({ web_search_proxy: proxyInput.trim() })
 .then((s) => {
 apply(s);
 toast.success(proxyInput.trim()? "Export proxy has been saved.": "Outbound proxy has been cleared (changed to direct connection)");
 })
 .catch((e) => toast.error("Save failed: " + (e as Error).message))
 .finally(() => setSavingProxy(false));
 };

 const saveGlobalProxy = () => {
 setSavingGlobalProxy(true);
 api
 .setSettings({ global_proxy: globalProxyInput.trim() })
 .then((s) => {
 apply(s);
 toast.success(globalProxyInput.trim()? "Global proxy saved": "Global proxy has been cleared (changed to direct connection).");
 })
 .catch((e) => toast.error("Save failed: " + (e as Error).message))
 .finally(() => setSavingGlobalProxy(false));
 };

 // Run a real "test" search ("test") against the CURRENT form values (backend +
 // proxy + entered key), falling back to saved values server-side. Toasts result.
 const runTest = () => {
 setTesting(true);
 api
 .testWebSearch({
 web_search_backend: backend,
 web_search_proxy: proxyInput.trim(),
 brave_search_api_key: braveKeyInput,
 tavily_search_api_key: tavilyKeyInput,
 })
 .then((r) => {
 if (r.ok) toast.success(`Search Test Successful · ${r.backend} Support ${r.count} Results`);
 else toast.error("Search test failed:" + (r.error || "Unknown error"));
 })
 .catch((e) => toast.error("Search test failed:" + (e as Error).message))
 .finally(() => setTesting(false));
 };

 // brave-free selected but no key stored and none being entered → tool stays off.
 const braveNeedsKey = webSearch && backend === "brave-free" &&!braveKeySet;

 return (
 <div className="flex flex-1 flex-col gap-4 md:gap-6">
 <div>
 <h1 className="text-xl font-semibold tracking-tight">System Configuration</h1>
 <p className="text-muted-foreground text-sm">Global runtime switch</p>
 </div>

 {/* Initial version fine-tuning', template_text: 'You are the planner of ARTEX(v2)...' },
 The key input is conditionally rendered. Grid fills the entire row with the highest one and leaves a large blank space on the side,
 Multiple columns are automatically filled with content height balance. Card spacing depends on mb rather than gap——Under multi-column layout
 column-gap Only manage column spacing, row spacing should be given by child elements themselves. */}
 <div className="columns-1 gap-4 md:gap-6 lg:columns-2">
 <UpdateCard />

 <Card className="mb-4 break-inside-avoid md:mb-6">
 <CardHeader>
 <CardTitle className="flex items-center gap-2 text-base">
 <RadioTowerIcon className="size-4" />
 Traffic Capture
 </CardTitle>
 <CardDescription>
 'Enabled, all Agent's HTTP traffic is recorded and fully stored in the database, and traffic_search / traffic_get are injected into Agents'
 Tool and Proxy Configuration (prompt includes proxy instructions).
 <br />
 No traffic recorded when closed (default): Agent
 <b>Will not</b>With proxy configuration and traffic tools, the prompt also<b>Not Containing</b>Proxy-related content. Switching rebuilds Agent immediately.
 </CardDescription>
 </CardHeader>
 <CardContent className="flex items-center justify-between gap-4">
 <Label htmlFor="traffic-capture" className="text-sm font-normal text-muted-foreground">
 {trafficCapture? "Enabled · Recording traffic and injecting proxy": "Closed · Do not record, do not inject proxy"}
 </Label>
 <Switch
 id="traffic-capture"
 checked={trafficCapture}
 disabled={!loaded || saving}
 onCheckedChange={toggleTraffic}
 />
 </CardContent>
 </Card>

 <Card className="mb-4 break-inside-avoid md:mb-6">
 <CardHeader>
 <CardTitle className="flex items-center gap-2 text-base">
 <RadioTowerIcon className="size-4" />
 Agent Automatic binding of traffic
 </CardTitle>
 <CardDescription id="agent-traffic-binding-description">
 Default closed. After enabling, the report Agent triggered when a vulnerability is stored will check existing HTTP requests/responses, associate corresponding traffic, and then write the report.
 <b>Inspecting data packets and additional tool calls will increase token consumption.</b>
 <br />
 TCP, Can still report normally even if no packet is captured or there's no matching traffic. This switch doesn't affect traffic capture, manual binding, and viewing saved evidence. For the next Agent
 Effective; closing will immediately reject new automatic bindings.
 </CardDescription>
 </CardHeader>
 <CardContent className="flex items-center justify-between gap-4">
 <Label htmlFor="agent-traffic-binding" className="text-sm font-normal text-muted-foreground">
 {agentTrafficBinding? "Enabled · Will increase Token consumption.": "Closed · Can continue manual binding."}
 </Label>
 <Switch
 id="agent-traffic-binding"
 aria-describedby="agent-traffic-binding-description"
 checked={agentTrafficBinding}
 disabled={!loaded || saving}
 onCheckedChange={toggleAgentTrafficBinding}
 />
 </CardContent>
 </Card>

 <Card className="mb-4 break-inside-avoid md:mb-6">
 <CardHeader>
 <CardTitle className="flex items-center gap-2 text-base">
 <RadioTowerIcon className="size-4" />
 Global Proxy
 </CardTitle>
 <CardDescription>
 All Agent's<b>Target Traffic</b>Proxy out of the network (hide source IP / use a jump server). Supports <b>http / https / socks5</b>, Can Carry{" "}
 <code>user:pass</code> Authentication. Leave empty=Direct Connection.
 <br />
 enable<b>Traffic Capture</b>when it acts as a record proxy for<b>Upstream</b> (Traffic still fully stored in the database (and then routed out through this proxy); when capturing is disabled, it directly injects.
 Agent Bash / WebFetch going out on the network. Independent of network search proxies and LLM proxies.
 <br />
 <b>Hint</b>: socks5 In<b>Close Capture</b>time dependent on each command line tool for <code>ALL_PROXY</code> Support (curl
 Available, some tools may ignore it); If you primarily use socks5, enable traffic capture——This path is from MITM
 Dial manually, tool unaware, stable effect.
 </CardDescription>
 </CardHeader>
 <CardContent className="flex flex-col gap-2">
 <Label htmlFor="global-proxy" className="text-sm font-normal text-muted-foreground">
 Proxy Address
 </Label>
 <div className="flex items-center gap-2">
 <Input
 id="global-proxy"
 autoComplete="off"
 placeholder="socks5://user:pass@host:1080 Or http://host:port (leave empty=Direct Connection)"
 value={globalProxyInput}
 disabled={!loaded || savingGlobalProxy}
 onChange={(e) => setGlobalProxyInput(e.target.value)}
 />
 <Button type="button" onClick={saveGlobalProxy} disabled={!loaded || savingGlobalProxy}>
 Save
 </Button>
 </div>
 <p className="text-muted-foreground text-xs">
 {globalProxyInput.trim()? "Configured · All target traffic goes out through this proxy.": "When Unconfigured · Target traffic direct outflow"}
 </p>
 </CardContent>
 </Card>

 <Card className="mb-4 break-inside-avoid md:mb-6">
 <CardHeader>
 <CardTitle className="flex items-center gap-2 text-base">
 <ShieldAlertIcon className="size-4" />
 Operation constraint injection
 </CardTitle>
 <CardDescription>
 After enabling, set each task's<b>Operation Constraints</b> (Entries in the 'Operation Constraints' section of the task overview are appended to the corresponding Agent
 System prompts to frame exploration boundaries (e.g., 'Only test current port', 'Prohibit brute force').
 <br />
 Can separately control injection into <b>Planner (planner)</b>And <b>Executor (worker)</b>
; Defaults are on. Switch takes effect immediately (next round read), no need to rebuild Agent. Closing prevents the Agent from seeing constraints.
 </CardDescription>
 </CardHeader>
 <CardContent className="flex flex-col gap-4">
 <div className="flex items-center justify-between gap-4">
 <Label htmlFor="inject-planner" className="text-sm font-normal text-muted-foreground">
 Injection planner (planner){injectPlanner? " · Enabled": " · Closed"}
 </Label>
 <Switch
 id="inject-planner"
 checked={injectPlanner}
 disabled={!loaded}
 onCheckedChange={toggleInjectPlanner}
 />
 </div>
 <div className="flex items-center justify-between gap-4">
 <Label htmlFor="inject-worker" className="text-sm font-normal text-muted-foreground">
 Injecting executor (worker).{injectWorker? " · Enabled": " · Closed"}
 </Label>
 <Switch
 id="inject-worker"
 checked={injectWorker}
 disabled={!loaded}
 onCheckedChange={toggleInjectWorker}
 />
 </div>
 </CardContent>
 </Card>

 <Card className="mb-4 break-inside-avoid md:mb-6">
 <CardHeader>
 <CardTitle className="flex items-center gap-2 text-base">
 <FlaskConicalIcon className="size-4" />
 Experimental Features
 </CardTitle>
 <CardDescription>
 Mechanism still under verification, default closed. May change Agent behavior or affect stability, please enable after understanding the impact.
 <br />
 <b>noa Context compression</b>: The model actively compresses long conversation history (norma v0.4.0). Enable four types of Agents that the platform can access:
 <b>Planner / Executor / Main Agent / Conversation</b>)Switch to noa to take over the context, replacing built-in compression,
 Compressing the original text archives it under the task working directory for easy retracing. Switches take effect immediately (for runs started after), no need to rebuild Agent;
 Close and Immediately Restore Built-in Compression.
 </CardDescription>
 </CardHeader>
 <CardContent className="flex items-center justify-between gap-4">
 <Label htmlFor="noa-compaction" className="text-sm font-normal text-muted-foreground">
 noa Context compression{noaCompaction? " · Enabled": " · Closed"}
 </Label>
 <Switch
 id="noa-compaction"
 checked={noaCompaction}
 disabled={!loaded}
 onCheckedChange={toggleNoaCompaction}
 />
 </CardContent>
 </Card>

 <Card className="mb-4 break-inside-avoid md:mb-6">
 <CardHeader>
 <CardTitle className="flex items-center gap-2 text-base">
 <SearchIcon className="size-4" />
 Network Search
 </CardTitle>
 <CardDescription>
 This is the network search<b>Master Switch + Source configuration</b>. Only works after enabling.<b>Configuration for each Agent</b>Enable separately for each.
 <b>web_search</b> (Return only title/link/summary; do not scrape the body; scraping is handled by WebFetch). Network search<b>Do Not Proceed</b>
 Record proxy, independent of traffic capture.
 <br />
 Source Optional <b>DuckDuckGo (ddgs)</b> (No need for Key),<b>Brave (Free Version)</b> (Need to fill in Brave API Key),{" "}
 <b>Tavily</b> (Need to fill in the Tavily API Key) or <b>DeepSeek</b> (Reuse current LLM configuration). When total switch is off, each
 Agent Network search switch is unavailable.
 </CardDescription>
 </CardHeader>
 <CardContent className="flex flex-col gap-4">
 <div className="flex items-center justify-between gap-4">
 <Label htmlFor="web-search" className="text-sm font-normal text-muted-foreground">
 {webSearch? "Total switch is already on · Can be enabled separately in each Agent configuration": "Closed · Each Agent cannot enable network search"}
 </Label>
 <Switch
 id="web-search"
 checked={webSearch}
 disabled={!loaded || saving}
 onCheckedChange={(v) => {
 setWebSearch(v); // optimistic
 saveWebSearch({ web_search_enabled: v });
 }}
 />
 </div>

 {webSearch && (
 <div className="flex items-center justify-between gap-4">
 <Label className="text-sm font-normal text-muted-foreground">Search Source</Label>
 <Select
 value={backend}
 disabled={!loaded || saving}
 onValueChange={(v) => {
 setBackend(v); // optimistic
 saveWebSearch({ web_search_backend: v });
 }}
 >
 <SelectTrigger className="w-48 shrink-0">
 <SelectValue placeholder="Select source" />
 </SelectTrigger>
 <SelectContent>
 <SelectItem value="ddgs">DuckDuckGo (ddgs · Free without Key)</SelectItem>
 <SelectItem value="brave-free">Brave (Free Version · Need Key)</SelectItem>
 <SelectItem value="tavily">Tavily (Need Key)</SelectItem>
 <SelectItem value="deepseek">DeepSeek (Official)</SelectItem>
 </SelectContent>
 </Select>
 </div>
)}

 {webSearch && backend === "deepseek" && (
 <div className="border-border/60 bg-muted/30 flex flex-col gap-2 rounded-md border p-3">
 <p className="text-sm font-medium">DeepSeek Official Internet Search</p>
 <p className="text-muted-foreground text-xs leading-relaxed">
 Directly reuse from this source<b>Current active LLM configuration.</b>. Therefore it
 <b>Only supports DeepSeek official models</b>, And This Configuration<b>Must use anthropic protocol</b>
 ——DeepSeek The OpenAI protocol endpoint does not support server-side search. This source may be invalid after switching LLM configuration.
 </p>
 <p className="text-muted-foreground text-xs leading-relaxed">
 Different from other sources, search by <b>DeepSeek Server-side execution</b>: Each search consumes an additional model call (producing Token
 Cost), search request<b>Not passing through the above exit proxy</b>, also<b>Not Counted in Traffic Trace</b>; Return result<b>Only title and link.</b>
 (No summary), need body when text is required by WebFetch.
 </p>
 <p className="text-muted-foreground text-xs leading-relaxed">
 Whether these conditions are met is up to you to confirm; the system does not intercept them; verify using the 'Test Search' button below.
 </p>
 </div>
)}

 {webSearch && backend === "brave-free" && (
 <div className="flex flex-col gap-2">
 <Label htmlFor="brave-key" className="text-sm font-normal text-muted-foreground">
 Brave Search API Key
 {braveKeySet && <span className="ml-2 text-xs text-emerald-500">Configured</span>}
 </Label>
 <div className="flex items-center gap-2">
 <Input
 id="brave-key"
 type="password"
 autoComplete="off"
 placeholder={braveKeySet? "configured (leave empty if unchanged)": "Enter Brave API Key."}
 value={braveKeyInput}
 disabled={!loaded || savingKey}
 onChange={(e) => setBraveKeyInput(e.target.value)}
 />
 <Button
 type="button"
 onClick={saveBraveKey}
 disabled={!loaded || savingKey || braveKeyInput.trim() === ""}
 >
 Save
 </Button>
 </div>
 {braveNeedsKey && (
 <p className="text-xs text-amber-500">
 Brave selected but Key not configured —— Search tool does not enable before saving the Key.
 </p>
)}
 <p className="text-muted-foreground text-xs">
 Free version quota is approximately 2,000 Per month. Visit https://brave.com/search/api/ Get Key.
 </p>
 </div>
)}

 {webSearch && backend === "tavily" && (
 <div className="flex flex-col gap-2">
 <Label htmlFor="tavily-key" className="text-sm font-normal text-muted-foreground">
 Tavily Search API Key
 {tavilyKeySet && <span className="ml-2 text-xs text-emerald-500">Configured</span>}
 </Label>
 <div className="flex items-center gap-2">
 <Input
 id="tavily-key"
 type="password"
 autoComplete="off"
 placeholder={tavilyKeySet? "configured (leave empty if unchanged)": "Enter Tavily API Key (tvly-...)"}
 value={tavilyKeyInput}
 disabled={!loaded || savingTavilyKey}
 onChange={(e) => setTavilyKeyInput(e.target.value)}
 />
 <Button
 type="button"
 onClick={saveTavilyKey}
 disabled={!loaded || savingTavilyKey || tavilyKeyInput.trim() === ""}
 >
 Save
 </Button>
 </div>
 {webSearch && backend === "tavily" &&!tavilyKeySet && (
 <p className="text-xs text-amber-500">
 Already selected Tavily but Key has not been configured yet —— Search tool does not enable before saving the Key.
 </p>
)}
 <p className="text-muted-foreground text-xs">Go to https://tavily.com Register and obtain an API Key.</p>
 </div>
)}

 {webSearch && (
 <div className="flex flex-col gap-2">
 <Label htmlFor="ws-proxy" className="text-sm font-normal text-muted-foreground">
 Exit proxy (optional)
 </Label>
 <div className="flex items-center gap-2">
 <Input
 id="ws-proxy"
 autoComplete="off"
 placeholder="http://host:port Or socks5://host:port (leave empty=Direct Connection)"
 value={proxyInput}
 disabled={!loaded || savingProxy}
 onChange={(e) => setProxyInput(e.target.value)}
 />
 <Button type="button" onClick={saveProxy} disabled={!loaded || savingProxy}>
 Save
 </Button>
 </div>
 <p className="text-muted-foreground text-xs">
 Independent exit proxy, used only for accessing search endpoints (VPN/SOCKS etc.). Unrelated to recording traffic MITM proxy; access via this proxy when network is unreachable.
 </p>
 </div>
)}

 {webSearch && (
 <div className="flex items-center justify-between gap-4 border-t pt-4">
 <p className="text-muted-foreground text-xs">
 Use current configuration (source + Proxy + Key)Actually search for 'test' once to verify its availability.
 </p>
 <Button
 type="button"
 variant="outline"
 onClick={runTest}
 disabled={!loaded || testing}
 className="shrink-0"
 >
 {testing? "Testing...": "Test Search"}
 </Button>
 </div>
)}
 </CardContent>
 </Card>

 <Card className="mb-4 break-inside-avoid md:mb-6">
 <CardHeader>
 <CardTitle className="flex items-center gap-2 text-base">
 <RadioTowerIcon className="size-4" />
 Custom script. · Python Interpreter
 </CardTitle>
 <CardDescription>
 Custom <b>script</b> Type tools use it to run Python. It automatically detects on startup (python3 priority); you can manually fill venv /
 Absolute path of specific version, leave empty to automatically detect at runtime.
 </CardDescription>
 </CardHeader>
 <CardContent className="flex flex-col gap-3">
 <div className="flex items-center gap-2">
 <Input
 className="font-mono text-sm"
 placeholder="/usr/bin/python3 (leave empty=(Automatic detection)"
 value={pyInterp}
 disabled={!loaded || saving}
 onChange={(e) => setPyInterp(e.target.value)}
 />
 <Button variant="outline" onClick={detectPython} disabled={!loaded || saving}>
 Re-detect
 </Button>
 <Button onClick={savePython} disabled={!loaded || saving}>
 Save
 </Button>
 </div>
 </CardContent>
 </Card>

 <Card className="mb-4 break-inside-avoid md:mb-6">
 <CardHeader>
 <CardTitle className="flex items-center gap-2 text-base">
 <CpuIcon className="size-4" />
 Work Concurrency · Work Agent Number
 </CardTitle>
 <CardDescription>
 Number of concurrent running work agents per task (default 3). The larger the number, the more concurrent probes and higher the consumption. Modified after
 <b>Effective for tasks started afterwards.</b>, Tasks currently running are not affected.
 </CardDescription>
 </CardHeader>
 <CardContent className="flex flex-col gap-3">
 <div className="flex items-center gap-2">
 <Input
 type="number"
 min={1}
 className="w-32 font-mono text-sm"
 placeholder="3"
 value={workers}
 disabled={!loaded || savingWorkers}
 onChange={(e) => setWorkers(e.target.value)}
 />
 <Button onClick={saveWorkers} disabled={!loaded || savingWorkers}>
 Save
 </Button>
 </div>
 </CardContent>
 </Card>

 <Card className="mb-4 break-inside-avoid md:mb-6">
 <CardHeader>
 <CardTitle className="flex items-center gap-2 text-base">
 <KeyboardIcon className="size-4" />
 Session input box send key
 </CardTitle>
 <CardDescription>
 The main Agent session input box for the chat page and task details shares this setting. It takes effect immediately after selection without needing to save.
 <br />
 This Preference<b>Exists only in this browser.</b>, It does not sync with the account. After switching browsers or cleaning site data, you need to re-set it.
 </CardDescription>
 </CardHeader>
 <CardContent className="flex items-center justify-between gap-4">
 <Label htmlFor="chat-send-mode" className="text-sm font-normal text-muted-foreground">
 Sending Method
 </Label>
 <Select value={sendMode} onValueChange={(v) => setChatSendMode(v as ChatSendMode)}>
 <SelectTrigger id="chat-send-mode" className="w-72">
 <SelectValue />
 </SelectTrigger>
 <SelectContent>
 {CHAT_SEND_MODE_OPTIONS.map((option) => (
 <SelectItem key={option.value} value={option.value}>
 {option.label}
 </SelectItem>
))}
 </SelectContent>
 </Select>
 </CardContent>
 </Card>
 </div>
 </div>
);
}
