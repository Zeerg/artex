"use client";

import * as React from "react";

import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";

// The key for sending or new line in the session input box. Pure front-end preference: only store in localStorage, not in the database and not synchronized with the account.
// Therefore, switching browsers requires re-setting. See issue #39——0.3.2 Press Ctrl+Enter Sending changed to Enter for sending,
// Bring back the old key positions as options.
export type ChatSendMode = "enter" | "ctrl-enter";

export const CHAT_SEND_MODE_KEY = "artex_chat_send_mode";
export const DEFAULT_CHAT_SEND_MODE: ChatSendMode = "enter";

export const CHAT_SEND_MODE_OPTIONS: { value: ChatSendMode; label: string }[] = [
  { value: "enter", label: "Enter Send, Shift+Enter Line Break" },
  { value: "ctrl-enter", label: "Ctrl+Enter Send, Enter for newline" },
];

function parseMode(raw: string | null): ChatSendMode {
  return raw === "ctrl-enter" || raw === "enter" ? raw : DEFAULT_CHAT_SEND_MODE;
}

// Collection of subscribers within the same tab. The localStorage storage event only triggers in 'other' tabs,
// This page needs to notify the same-page input box through emit after changing in settings, otherwise it needs to refresh for it to take effect.
const listeners = new Set<() => void>();

function subscribe(listener: () => void) {
  listeners.add(listener);
  window.addEventListener("storage", listener);
  return () => {
    listeners.delete(listener);
    window.removeEventListener("storage", listener);
  };
}

// Returns a string literal, Object.is Value comparison prevents useSyncExternalStore from falling into a loop.
function getSnapshot(): ChatSendMode {
  return parseMode(getLocalStorageValue(CHAT_SEND_MODE_KEY));
}

// The server does not have localStorage, so it first renders default values, hydrates, and then corrects with getSnapshot.
function getServerSnapshot(): ChatSendMode {
  return DEFAULT_CHAT_SEND_MODE;
}

export function useChatSendMode(): ChatSendMode {
  return React.useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}

export function setChatSendMode(mode: ChatSendMode) {
  setLocalStorageValue(CHAT_SEND_MODE_KEY, mode);
  for (const listener of listeners) listener();
}

// shouldSubmitOnKey Determine if a single key press should be sent.
// isComposing / keyCode 229 It is a Chinese input method selecting characters, must be allowed, otherwise pressing Enter to select words will send incorrectly.
// enter Pattern only excludes Shift with 0..3.2 Behavior kept consistent word-for-word——Users will have the same feel without changing settings.
// ctrl-enter Mode accepts Ctrl and Cmd (macOS) simultaneously.
export function shouldSubmitOnKey(e: React.KeyboardEvent, mode: ChatSendMode): boolean {
  if (e.key !== "Enter") return false;
  if (e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) return false;
  if (mode === "ctrl-enter") return e.ctrlKey || e.metaKey;
  return !e.shiftKey;
}
