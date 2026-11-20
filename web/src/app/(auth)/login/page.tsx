"use client";

import { useEffect, useRef, useState } from "react";

import { useRouter } from "next/navigation";

import { AlertTriangle, ShieldCheck } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogClose, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import { auth } from "@/lib/auth";

export default function LoginPage() {
 const router = useRouter();
 const [password, setPassword] = useState("");
 const [error, setError] = useState("");
 const [loading, setLoading] = useState(false);
 const [checking, setChecking] = useState(true);
 const [agreed, setAgreed] = useState(false);
 const [termsOpen, setTermsOpen] = useState(false);
 const [readToEnd, setReadToEnd] = useState(false);
 const termsBodyRef = useRef<HTMLDivElement>(null);

 // Scroll to the bottom of the terms (including cases where complete display is possible without scrolling) to click 'Agree'.
 function handleTermsScroll() {
 const el = termsBodyRef.current;
 if (!el) return;
 if (el.scrollTop + el.clientHeight >= el.scrollHeight - 8) setReadToEnd(true);
 }

 useEffect(() => {
 if (!termsOpen) return;
 // Reset when opening and handle scenarios where the content is not enough to trigger scrolling.
 setReadToEnd(false);
 const el = termsBodyRef.current;
 if (el && el.scrollHeight <= el.clientHeight + 8) setReadToEnd(true);
 }, [termsOpen]);

 useEffect(() => {
 // Log in directly to the main interface (no middleware assists this redirect during static export).
 const token = auth.getToken();
 if (token) {
 // localStorage There may still be credentials but the cookie has been lost. First sync, then initiate a new request,
 // Avoid service-side guards or route caching sending redirects back to a login page that is still in checking status.
 auth.setToken(token);
 window.location.replace("/function/tasks");
 return;
 }
 api
 .authStatus()
 .then(({ initialized }) => {
 if (!initialized) router.replace("/setup");
 })
 .catch(() => setError("Unable to connect to the backend service."))
 .finally(() => setChecking(false));
 }, [router]);

 async function handleSubmit(e: React.FormEvent) {
 e.preventDefault();
 if (!agreed) {
 setError("Please read and agree to the 'Terms of Use'");
 return;
 }
 setLoading(true);
 setError("");
 try {
 const { token } = await api.login("ARTEX", password);
 auth.setToken(token);
 window.location.replace("/function/tasks");
 } catch {
 setError("Username or password error");
 } finally {
 setLoading(false);
 }
 }

 if (checking) {
 return (
 <div role="status" className="flex min-h-dvh items-center justify-center text-muted-foreground">
 Checking login status......
 </div>
);
 }

 return (
 <div className="flex h-dvh">
 {/* Left panel */}
 <div className="hidden flex-col items-center justify-center bg-primary p-12 text-center lg:flex lg:w-1/3">
 <div className="relative flex items-center justify-center">
 <div className="absolute size-80 rounded-full border border-primary-foreground/10" />
 <div className="absolute size-60 rounded-full border border-primary-foreground/15" />
 <div className="absolute size-40 rounded-full border border-primary-foreground/20" />
 {/* eslint-disable-next-line @next/next/no-img-element */}
 <img src="/logo.png" alt="ARTEX" width={160} height={160} className="relative brightness-0 invert" />
 </div>
 </div>

 {/* Right panel */}
 <div className="flex w-full items-center justify-center bg-background p-8 lg:w-2/3">
 <div className="w-full max-w-md space-y-10 py-24 lg:py-32">
 <div className="space-y-4 text-center">
 <h2 className="text-2xl font-medium tracking-tight">Login</h2>
 <p className="mx-auto max-w-xl text-muted-foreground">Welcome back, please enter your password to continue using ARTEX</p>
 </div>
 <form onSubmit={handleSubmit} className="flex flex-col gap-4">
 <div className="space-y-1.5">
 <Label htmlFor="username">Username</Label>
 <Input id="username" value="ARTEX" readOnly className="bg-muted text-muted-foreground" />
 </div>
 <div className="space-y-1.5">
 <Label htmlFor="password">Password</Label>
 <Input
 id="password"
 type="password"
 value={password}
 onChange={(e) => setPassword(e.target.value)}
 placeholder="Please enter password;"
 autoFocus
 autoComplete="current-password"
 />
 </div>
 <div className="flex items-start gap-2">
 <Checkbox
 id="agree-terms"
 checked={agreed}
 onCheckedChange={(v) => setAgreed(v === true)}
 className="mt-0.5"
 />
 <Label htmlFor="agree-terms" className="text-sm font-normal leading-relaxed text-muted-foreground">
 I have read and agreed to
 <button
 type="button"
 onClick={() => setTermsOpen(true)}
 className="mx-0.5 font-medium text-primary underline-offset-4 hover:underline"
 >
 "User manual"
 </button>
 </Label>
 </div>
 {error && <p className="text-sm text-destructive">{error}</p>}
 <Button type="submit" className="w-full" disabled={loading ||!password ||!agreed}>
 {loading? "Logging in...": "Login"}
 </Button>
 </form>
 </div>
 </div>

 <Dialog open={termsOpen} onOpenChange={setTermsOpen}>
 <DialogContent className="gap-0 p-0 sm:max-w-2xl">
 <DialogHeader className="flex-row items-center gap-3 border-b px-6 py-4">
 <div className="flex size-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
 <ShieldCheck className="size-5" />
 </div>
 <div className="space-y-0.5">
 <DialogTitle className="text-base">ARTEX Terms of Use and Disclaimer</DialogTitle>
 <p className="text-xs text-muted-foreground">
 Version v1.0 · Effective date 2026-09-18. · Please read all terms and conditions thoroughly before logging in
 </p>
 </div>
 </DialogHeader>

 <div
 ref={termsBodyRef}
 onScroll={handleTermsScroll}
 className="max-h-[60vh] space-y-5 overflow-y-auto px-6 py-5 text-sm leading-relaxed text-muted-foreground"
 >
 <p className="rounded-lg border bg-muted/40 p-3 text-foreground/80">
 This 'User Agreement and Disclaimer' (hereinafter referred to as"This declaration")Is this you interacting with ARTEX?
 'Agreement between project authors and contributors. Please carefully read and thoroughly understand the terms of each clause, especially those exempted, limited in liability, or prohibited.'
 <span className="font-medium text-foreground">
 {" "}
 Once you download, install, access, or use this software in any way, you are deemed to have read, understood, and agreed to the full constraints of this declaration.
 </span>
 </p>

 <section className="space-y-1.5">
 <h4 className="flex items-center gap-2 font-medium text-foreground">
 <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
 1
 </span>
 First item · Define with open-source license
 </h4>
 <p className="pl-7">
 This software (ARTEX) is a product based on GNU Affero General Public License
 v3.0 (AGPL-3.0)Open-source software. You may use, copy, modify, and distribute this software freely under the terms of the license; but any derivative works (including online services provided to third parties via network) must also be distributed under
 AGPL-3.0 Protocol open-sourced and publicly disclosing the complete source code. AGPL-3.0 The full terms and conditions are subject to the accompanying LICENSE file.
 </p>
 </section>

 <section className="space-y-1.5">
 <h4 className="flex items-center gap-2 font-medium text-foreground">
 <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
 2
 </span>
 Second time · Authorization usage scope
 </h4>
 <p className="pl-7">
 This software is for personal learning, code research, discussions on security technology principles, and technical validation in a locally isolated environment you set up. It is suitable for non-attackive, non-destructive purposes such as learning, academic research, and code review. Except where explicitly permitted by this clause, you may not use this software for any other purpose.
 </p>
 </section>

 <section className="space-y-2">
 <h4 className="flex items-center gap-2 font-medium text-destructive">
 <span className="flex size-5 items-center justify-center rounded-md bg-destructive/10 text-xs font-semibold text-destructive">
 3
 </span>
 <AlertTriangle className="size-4" />
 Third · Prohibited behavior.
 </h4>
 <ul className="ml-7 list-decimal space-y-1.5 rounded-lg border border-destructive/20 bg-destructive/5 p-3 pl-8 text-foreground/80 marker:text-destructive/70">
 <li>
 This is the <b>total switch + source configuration</b> for network search. Only after enabling can you select whether to enable it in each Agent's configuration.
 </li>
 <li>Strictly prohibit using this software for any actual penetration testing, defensive operations, red-blue exercises, or production environments;</li>
 <li>Prohibit using this software for illegal intrusion, data theft, ransomware, denial of service (DoS/DDoS), or any destructive/criminal activities;</li>
 <li>Strictly prohibit removing, altering, or circumventing any copyright, license, or security notice information in this software and its output;</li>
 <li>Prohibited from engaging in any activities that violate the laws, regulations, and regulatory provisions of your country or region.</li>
 </ul>
 </section>

 <section className="space-y-1.5">
 <h4 className="flex items-center gap-2 font-medium text-foreground">
 <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
 4
 </span>
 Article 4 · Intellectual property.
 </h4>
 <p className="pl-7">
 The copyright and related intellectual property of this software belong to the project author and contributors, and are licensed under AGPL-3.0
 Within the scope of the protocol agreement, grants you the corresponding rights. Except for rights explicitly granted by this agreement, no other rights are granted to you either explicitly or implicitly.
 </p>
 </section>

 <section className="space-y-1.5">
 <h4 className="flex items-center gap-2 font-medium text-foreground">
 <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
 5
 </span>
 Fifth item · Data and privacy
 </h4>
 <p className="pl-7">
 This software is an open-source program that you can deploy yourself. The author does not operate any centralized services and will not collect or upload your usage data. All data generated, processed, or encountered during use is under your control and responsibility for its legality and security; any consequences resulting from improper data handling are borne by you.
 </p>
 </section>

 <section className="space-y-1.5">
 <h4 className="flex items-center gap-2 font-medium text-foreground">
 <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
 6
 </span>
 Sixth time · Compliance and legal responsibility.
 </h4>
 <p className="pl-7">
 You should comply with all laws and regulations of your country or region regarding cybersecurity, data security, personal information protection, computer crime, etc. (In China, this includes but is not limited to the Cybersecurity Law, Data Security Law, Personal Information Protection Law, and relevant judicial interpretations).
 <span className="font-medium text-foreground">
 {" "}
 All legal responsibilities and consequences resulting from your violation of the aforementioned laws and regulations or this declaration are borne solely by you, without any liability to the software author and contributors.
 </span>
 </p>
 </section>

 <section className="space-y-1.5">
 <h4 className="flex items-center gap-2 font-medium text-foreground">
 <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
 7
 </span>
 Seventh · Disclaimer and Liability Limitation
 </h4>
 <p className="pl-7">
 This software is based on"Current state (AS IS)"And"Current (AS).
 AVAILABLE)"The software is provided 'as is' without any express or implied warranty, including but not limited to the warranties of merchantability, fitness for a particular purpose, accuracy, and non-infringement. To the extent permitted by law, the author and contributors of this software disclaim all liability for any direct, indirect, incidental, special, or consequential damages arising from the use or inability to use this software (whether used in a proper manner or not), including but not limited to data loss, system damage, business interruption, profit loss, or legal disputes.
 </p>
 </section>

 <section className="space-y-1.5">
 <h4 className="flex items-center gap-2 font-medium text-foreground">
 <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
 8
 </span>
 Article 8 · Terms of Change and Final Interpretation
 </h4>
 <p className="pl-7">
 The author reserves the right to update this declaration from time to time based on laws and regulations or project development needs. The updated version will be released with the project and take effect from the date of publication. Continuing to use this software constitutes acceptance of the revised terms. Within the limits of the law, the final interpretation rights of this declaration belong to the author of the project. If any clause of this declaration is deemed invalid, it does not affect the validity of the other clauses.
 </p>
 </section>
 </div>

 <DialogFooter className="mx-0 mb-0 flex-col items-stretch gap-2 rounded-b-xl px-6 sm:flex-row sm:items-center sm:justify-between">
 <p className="text-xs text-muted-foreground">
 {readToEnd? "You have browsed all terms": "Please scroll to the bottom of the terms before confirming."}
 </p>
 <DialogClose asChild>
 <Button
 type="button"
 disabled={!readToEnd}
 onClick={() => {
 setAgreed(true);
 setError("");
 }}
 >
 I have read and agreed to all terms
 </Button>
 </DialogClose>
 </DialogFooter>
 </DialogContent>
 </Dialog>
 </div>
);
}
