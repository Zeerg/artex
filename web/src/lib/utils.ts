import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
 return twMerge(clsx(inputs));
}

// Drawer/Dialog(Sheet/Dialog)Close the onInteractOutside auxiliary.
//
// Background:Radix popup in the drawer.(Select DropdownMenu, Popover, etc.)Will portal to drawer.
// . When a modal is open, clicking outside the mask/drawer should close it.,This time, pointerdown will be handled by both Select and Sheet.
// DismissableLayer Process simultaneously;Select First close and is discrete event, React will sync flush,So
// When it's time for the Sheet processor, the data-state in the popup layer has already been changed to closed —— In"Current"Check if the modal is open.
// Naturally Unreliable(Verified in practice).
//
// Correct approach:Radix The pointerdown listener on the order interface is in the bubbling phase;We are in the capture phase(Earlier than it)First
// "Is there a modal open right now?"Record It,onInteractOutside Re-read this record value to decide whether to allow closing.
function isRadixOverlayOpenNow(): boolean {
 if (typeof document === "undefined") return false;
 return!!document.querySelector(
 [
 "[data-slot='select-trigger'][data-state='open']",
 "[data-slot='select-content'][data-state='open']",
 "[role='listbox'][data-state='open']",
 "[data-radix-popper-content-wrapper]",
 "[aria-expanded='true'][data-state='open']",
 ].join(","),
);
}

let overlayOpenAtLastPointerDown = false;
if (typeof document!== "undefined") {
 document.addEventListener(
 "pointerdown",
 () => {
 overlayOpenAtLastPointerDown = isRadixOverlayOpenNow();
 },
 true, // capture:Record before the pointerdown handler in Radix's bubble phase
);
}

// radixOverlayWasOpenAtPointerDown Support"Whether there was a Radix popup when the last pointerdown occurred.
// Open". The drawer/dialogue is based on this:Overlay mask when a popup is open → Only receive pop-up, do not close itself.
export function radixOverlayWasOpenAtPointerDown(): boolean {
 return overlayOpenAtLastPointerDown;
}

// copyText Write text to clipboard.,Returns whether successful.
// Background:navigator.clipboard Only in a secure context(HTTPS / localhost)Available;Through IP + HTTP
// It is undefined when accessed,Downgrade to execCommand at this point("copy").
export async function copyText(text: string): Promise<boolean> {
 if (navigator.clipboard && window.isSecureContext) {
 try {
 await navigator.clipboard.writeText(text);
 return true;
 } catch {
 // Continue with downgrade plan
 }
 }
 try {
 const textarea = document.createElement("textarea");
 textarea.value = text;
 textarea.style.position = "fixed";
 textarea.style.left = "-9999px";
 textarea.style.top = "0";
 document.body.appendChild(textarea);
 textarea.focus();
 textarea.select();
 const ok = document.execCommand("copy");
 document.body.removeChild(textarea);
 return ok;
 } catch {
 return false;
 }
}

export const getInitials = (str: string): string => {
 if (typeof str!== "string" ||!str.trim()) return "?";

 return (
 str
 .trim()
 .split(/\s+/)
 .filter(Boolean)
 .map((word) => word[0])
 .join("")
 .toUpperCase() || "?"
);
};

export function formatCurrency(
 amount: number,
 opts?: {
 currency?: string;
 locale?: string;
 minimumFractionDigits?: number;
 maximumFractionDigits?: number;
 noDecimals?: boolean;
 },
) {
 const { currency = "USD", locale = "en-US", minimumFractionDigits, maximumFractionDigits, noDecimals } = opts?? {};

 const formatOptions: Intl.NumberFormatOptions = {
 style: "currency",
 currency,
 minimumFractionDigits: noDecimals? 0: minimumFractionDigits,
 maximumFractionDigits: noDecimals? 0: maximumFractionDigits,
 };

 return new Intl.NumberFormat(locale, formatOptions).format(amount);
}
