"use client";

import * as React from "react";

import Link from "next/link";

import { ArrowUpCircleIcon } from "lucide-react";

import { api } from "@/lib/api";

/**
 * Top bar's"There is a new version."Hint: Check once when loading the entire page, and highlight next to the version number if there is an update.
 * Click on the 'Version & Update' card to reach the system configuration page.
 *
 * The backend caches GitHub query results for 30 minutes, so checking again on each mount is safe.
 * ——Unauthenticated GitHub API has only 60 times/hour/IP. Without that layer of caching, opening multiple tabs would exceed this limit.
 * This will exhaust the quota, making it difficult to update afterward.
 *
 * Query failures should be silent: the top bar is not an error reporting place; when users go to the settings page and click 'Check for Updates', they will see the reason.
 */
export function UpdateBadge() {
 const [latest, setLatest] = React.useState("");

 React.useEffect(() => {
 let alive = true;
 api
 .checkUpdate()
 .then((r) => {
 // has_update Already included;"Version numbers can be compared"Judgments, development builds will not show this prompt.
 if (alive && r.has_update && r.latest) setLatest(r.latest.replace(/^v(?=\d)/, ""));
 })
 .catch(() => {
 // Silent mode: no internet / GitHub rate limiting shouldn't show errors at the top bar.
 });
 return () => {
 alive = false;
 };
 }, []);

 if (!latest) return null;

 return (
 <Link
 href="/system/settings"
 title={`New version discovered ${latest}, Click to go to update`}
 className="inline-flex items-center gap-1.5 rounded-full bg-primary px-2.5 py-1 font-medium text-primary-foreground text-xs transition-opacity hover:opacity-90"
 >
 {/* Breath point: top bar elements are many, plain text is easy to ignore, animation makes it immediately visible. */}
 <span className="relative flex size-1.5">
 <span className="absolute inline-flex size-full animate-ping rounded-full bg-primary-foreground opacity-75" />
 <span className="relative inline-flex size-1.5 rounded-full bg-primary-foreground" />
 </span>
 <ArrowUpCircleIcon className="size-3.5" />
 <span className="hidden sm:inline">New Version {latest}</span>
 <span className="sm:hidden">New Version</span>
 </Link>
);
}
