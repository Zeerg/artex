"use client";

import * as React from "react";

import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
 Dialog,
 DialogContent,
 DialogDescription,
 DialogFooter,
 DialogHeader,
 DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";

export function ChangePasswordDialog({
 open,
 onOpenChange,
}: {
 readonly open: boolean;
 readonly onOpenChange: (open: boolean) => void;
}) {
 const [oldPassword, setOldPassword] = React.useState("");
 const [newPassword, setNewPassword] = React.useState("");
 const [confirmPassword, setConfirmPassword] = React.useState("");
 const [saving, setSaving] = React.useState(false);

 // reset fields whenever the dialog closes
 React.useEffect(() => {
 if (!open) {
 setOldPassword("");
 setNewPassword("");
 setConfirmPassword("");
 }
 }, [open]);

 function submit(e: React.FormEvent) {
 e.preventDefault();
 if (!oldPassword ||!newPassword) {
 toast.error("Please enter the current password and new password");
 return;
 }
 if (newPassword!== confirmPassword) {
 toast.error("The two newly input passwords do not match.");
 return;
 }
 setSaving(true);
 api
 .changePassword(oldPassword, newPassword)
 .then(() => {
 toast.success("Password changed");
 onOpenChange(false);
 })
 .catch((err) => toast.error(`Modification failed: ${(err as Error).message}`))
 .finally(() => setSaving(false));
 }

 return (
 <Dialog open={open} onOpenChange={onOpenChange}>
 <DialogContent className="sm:max-w-md">
 <form onSubmit={submit}>
 <DialogHeader>
 <DialogTitle>Change Password,</DialogTitle>
 <DialogDescription>
 Username Fixed As <b>ARTEX</b>. Need to enter the current password for verification; even after modification, already issued login tokens remain valid until expiration.
 </DialogDescription>
 </DialogHeader>
 <div className="flex flex-col gap-3 py-4">
 <div className="flex flex-col gap-1.5">
 <Label htmlFor="cp-old">Current password.</Label>
 <Input
 id="cp-old"
 type="password"
 autoComplete="current-password"
 value={oldPassword}
 disabled={saving}
 onChange={(e) => setOldPassword(e.target.value)}
 />
 </div>
 <div className="flex flex-col gap-1.5">
 <Label htmlFor="cp-new">New Password</Label>
 <Input
 id="cp-new"
 type="password"
 autoComplete="new-password"
 value={newPassword}
 disabled={saving}
 onChange={(e) => setNewPassword(e.target.value)}
 />
 </div>
 <div className="flex flex-col gap-1.5">
 <Label htmlFor="cp-confirm">Confirm new password.</Label>
 <Input
 id="cp-confirm"
 type="password"
 autoComplete="new-password"
 value={confirmPassword}
 disabled={saving}
 onChange={(e) => setConfirmPassword(e.target.value)}
 />
 </div>
 </div>
 <DialogFooter>
 <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={saving}>
 Real-time
 </Button>
 <Button type="submit" disabled={saving}>
 {saving? "In progress...": "Confirm modification."}
 </Button>
 </DialogFooter>
 </form>
 </DialogContent>
 </Dialog>
);
}
