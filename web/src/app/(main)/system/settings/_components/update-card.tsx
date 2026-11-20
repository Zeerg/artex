"use client";

import * as React from "react";

import {
 CheckCircle2Icon,
 DownloadIcon,
 ExternalLinkIcon,
 RefreshCwIcon,
 RotateCcwIcon,
 TriangleAlertIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import { api, sseUrl } from "@/lib/api";
import type { UpdateCheck, UpdateProgress } from "@/lib/types";

/** 'The longest time to wait for the new version to go live. One upgrade goes through three process starts (temporarily stored → High-risk → New Version),
 * Each is second-level; three minutes are enough to cover slow disks and Docker container rebuilds. */
const RESTART_TIMEOUT_MS = 180_000;

function humanSize(n?: number): string {
 if (!n || n <= 0) return "";
 const units = ["B", "KB", "MB", "GB"];
 let v = n;
 let i = 0;
 while (v >= 1024 && i < units.length - 1) {
 v /= 1024;
 i++;
 }
 return `${v.toFixed(i === 0? 0: 1)} ${units[i]}`;
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

export function UpdateCard() {
 const [info, setInfo] = React.useState<UpdateCheck | null>(null);
 const [checking, setChecking] = React.useState(true);
 const [progress, setProgress] = React.useState<UpdateProgress | null>(null);
 // Separate from progress: after staging completes, the process ends, SSE breaks, switch to polling /api/health.
 const [restarting, setRestarting] = React.useState(false);
 const [busy, setBusy] = React.useState(false);

 // quiet At the same time decide whether to bypass the backend cache: Automatic checks when entering a page use caching (top bar just checked),
 // If the user manually clicks 'Check for Updates', force a re-fetch from source; otherwise, wait for the cache to expire before viewing the newly published version.
 const check = React.useCallback((quiet = false) => {
 setChecking(true);
 api
 .checkUpdate(!quiet)
 .then((r) => {
 setInfo(r);
 if (!quiet) {
 if (r.error) toast.error("Update check failed:" + r.error);
 else if (r.has_update) toast.success(`New version discovered ${r.latest}`);
 else if (r.comparable) toast.success("Already at the latest version");
 }
 })
 .catch((e) => {
 if (!quiet) toast.error("Update check failed:" + (e as Error).message);
 })
 .finally(() => setChecking(false));
 }, []);

 React.useEffect(() => {
 check(true);
 }, [check]);

 // Poll /api/health until the version number changes.
 //
 // Criteria must be"Version Changed"Rather than"Connected successfully": During the upgrade process, the old version will briefly restart once.
 // (This time, it is only responsible for injecting artex.new Switch it up then immediately exit), only checking connectivity may misjudge success.
 const waitForNewVersion = React.useCallback(async (fromVersion: string) => {
 setRestarting(true);
 const deadline = Date.now() + RESTART_TIMEOUT_MS;
 while (Date.now() < deadline) {
 await sleep(2000);
 try {
 const r = await fetch("/api/health", { cache: "no-store" });
 if (r.ok) {
 const j = (await r.json()) as { version?: string };
 if (j.version && j.version!== fromVersion) {
 toast.success(`Updated to ${j.version}, Reloading page`);
 await sleep(800);
 window.location.reload();
 return;
 }
 }
 } catch {
 // Restarting the window should not affect ongoing polling. Continue polling.
 }
 }
 setRestarting(false);
 toast.error("Wait for service restart timeout. Please check the backend logs or confirm that artex was started through start.sh / start.bat Started.");
 }, []);

 // Subscribe to update progress. SSE does not use Next's /api rewrite (that layer buffers events and cannot push them out).
 const openStream = React.useCallback(
 (fromVersion: string) => {
 const es = new EventSource(sseUrl("/api/update/stream"));
 es.onmessage = (ev) => {
 let p: UpdateProgress;
 try {
 p = JSON.parse(ev.data) as UpdateProgress;
 } catch {
 return;
 }
 setProgress(p);
 if (p.phase === "failed") {
 es.close();
 setBusy(false);
 toast.error("Update failed: " + (p.error || p.message));
 return;
 }
 if (p.phase === "staged") {
 es.close();
 void waitForNewVersion(fromVersion);
 }
 };
 es.onerror = () => {
 // SSE will definitely break when the process exits. If already waiting to restart, this is normal,
 // Delegate to /api/health polling for continued judgment.
 es.close();
 };
 return es;
 },
 [waitForNewVersion],
);

 const doUpdate = () => {
 if (!info) return;
 const from = info.current;
 const ok = window.confirm(
 `Confirm update to ${info.latest}? \n\n` +
 "Updates will restart the program. Running tasks will be interrupted.\n" +
 (info.mode === "docker"
? "\nNote: Container updates only replace the program itself, not update the image's playwright / nmap etc. toolchain;" +
 "If the new version depends on a new tool, use docker compose pull."
: ""),
);
 if (!ok) return;

 setBusy(true);
 setProgress({ phase: "downloading", percent: 0, message: "Preparing..." });
 const es = openStream(from);
 api.applyUpdate().catch((e) => {
 es.close();
 setBusy(false);
 setProgress(null);
 toast.error("Update start failed:" + (e as Error).message);
 });
 };

 const doRollback = () => {
 if (!info) return;
 if (
!window.confirm(
 "Rollback to previous version?\n\nThe program will restart, and running tasks will be interrupted.\nNote: The database structure will not roll back. Older versions may not recognize data written by new versions.",
)
)
 return;
 const from = info.current;
 setBusy(true);
 api
 .rollbackUpdate()
 .then(() => {
 toast.success("Switched to previous version, restarting,...");
 void waitForNewVersion(from);
 })
 .catch((e) => {
 setBusy(false);
 toast.error("Rollback failed:" + (e as Error).message);
 });
 };

 const phase = progress?.phase;
 const showProgress = busy || restarting;
 // Only the download phase can get the real percentage (calculated by Content-Length). Verification/decryption/waiting for restart are
 // Indeterminate phase, progress bar fills and adds a pulsating animation to indicate"Busy but unsure how long it will take.".
 const downloading =!restarting && phase === "downloading";
 const pct = downloading? Math.max(progress?.percent?? 0, 0): 100;

 return (
 // The settings page is a multi-column waterfall layout, with cards responsible for row spacing and prohibiting column breaks (see page.tsx Comments).
 <Card className="mb-4 break-inside-avoid md:mb-6">
 <CardHeader>
 <CardTitle className="flex items-center gap-2 text-base">
 <DownloadIcon className="size-4" />
 Version and updates
 </CardTitle>
 <CardDescription>Check and install new versions from GitHub. The update will restart the program, interrupting running tasks.</CardDescription>
 </CardHeader>
 <CardContent className="space-y-4">
 <div className="flex flex-wrap items-center gap-2 text-sm">
 <span className="text-muted-foreground">current version</span>
 <Badge variant="secondary" className="font-mono">
 {info?.current?? "..."}
 </Badge>
 {info && (
 <>
 <Badge variant="outline" className="font-mono">
 {info.os}/{info.arch}
 </Badge>
 <Badge variant="outline">{info.mode === "docker"? "Docker": "Independent Program"}</Badge>
 </>
)}
 {info?.latest && (
 <>
 <span className="text-muted-foreground">Latest Version</span>
 <Badge variant={info.has_update? "default": "secondary"} className="font-mono">
 {info.latest}
 </Badge>
 </>
)}
 {info?.html_url && (
 <a
 href={info.html_url}
 target="_blank"
 rel="noreferrer"
 className="inline-flex items-center gap-1 text-xs text-muted-foreground underline-offset-4 hover:underline"
 >
 Update log <ExternalLinkIcon className="size-3" />
 </a>
)}
 </div>

 {info?.boot_notice && (
 <p className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 p-2 text-xs text-amber-700 dark:text-amber-400">
 <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
 {info.boot_notice}
 </p>
)}

 {info?.error && (
 <p className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
 <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
 Unable to connect to GitHub:{info.error}
 {"　"}Retry after configuring global proxy.
 </p>
)}

 {info &&!info.comparable && info.reason && <p className="text-xs text-muted-foreground">{info.reason}</p>}

 {info?.has_update && info.asset_available === false && (
 <p className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
 <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
 {info.latest} Not provided {info.os}/{info.arch} The release package (missing {info.asset}), Automatic update is unavailable.
 </p>
)}

 {info?.has_update && info.asset_available!== false && (
 <p className="text-xs text-muted-foreground">
 Will download <span className="font-mono">{info.asset}</span>
 {info.size? ` (${humanSize(info.size)})`: ""}, Replaced after SHA256 validation and smoke test, failed retains current version.
 </p>
)}

 {info &&!info.has_update && info.comparable &&!info.error && (
 <p className="flex items-center gap-2 text-xs text-muted-foreground">
 <CheckCircle2Icon className="size-3.5 text-emerald-600" />
 Already on the latest version.
 </p>
)}

 {info?.mode === "docker" && info.has_update && (
 <p className="text-xs text-muted-foreground">
 Docker Updates only replace the program itself, not the image's playwright / nmap etc. toolchain, and
 <span className="font-mono"> docker compose up -d </span>
 After rebuilding the container, it will revert to the versionbundled image. To upgrade along with the image, execute
 <span className="font-mono"> docker compose pull artex &amp;&amp; docker compose up -d artex</span>.
 </p>
)}

 {showProgress && (
 <div className="space-y-1.5">
 <Progress value={pct} className={downloading? undefined: "animate-pulse"} />
 <p className="text-xs text-muted-foreground">
 {restarting? "Restarting and applying the new version. Please wait (the page will automatically refresh)....": progress?.message}
 </p>
 </div>
)}

 <div className="flex flex-wrap gap-2">
 <Button variant="outline" size="sm" onClick={() => check(false)} disabled={checking || busy || restarting}>
 <RefreshCwIcon className={checking? "size-4 animate-spin": "size-4"} />
 Check for Updates
 </Button>
 <Button
 size="sm"
 onClick={doUpdate}
 disabled={busy || restarting ||!info?.has_update || info?.asset_available === false}
 >
 <DownloadIcon className="size-4" />
 {info?.has_update? `Update to ${info.latest}`: "Immediate Update"}
 </Button>
 {info?.has_backup && (
 <Button variant="ghost" size="sm" onClick={doRollback} disabled={busy || restarting}>
 <RotateCcwIcon className="size-4" />
 Rollback to previous version
 </Button>
)}
 </div>

 <p className="text-xs text-muted-foreground">
 One-click update of dependency guard script restarts the program. Please use this <span className="font-mono">start.sh</span> (Windows For
 <span className="font-mono"> start.bat</span>)Start ARTEX; when running artex directly, the program will not be automatically restarted after exiting.
 </p>
 </CardContent>
 </Card>
);
}
