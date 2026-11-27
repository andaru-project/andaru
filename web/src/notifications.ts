import { useSyncExternalStore } from 'react';
import mark from './andaru-icon.svg';
import type { Notification, NotificationSeverity } from './types';

const severityRank: Record<NotificationSeverity, number> = {
  info: 1,
  warning: 2,
  critical: 3,
};

export const severityColor: Record<NotificationSeverity, string> = {
  info: 'blue',
  warning: 'orange',
  critical: 'red',
};

// highestSeverity returns the most severe level among the active notifications, or undefined when empty.
export function highestSeverity(items: readonly Notification[]): NotificationSeverity | undefined {
  let highest: NotificationSeverity | undefined;
  for (const item of items) {
    if (!highest || severityRank[item.severity] > severityRank[highest]) highest = item.severity;
  }
  return highest;
}

// isSafeAppLink allows only in-app paths that a browser cannot read as another site's. The page's own origin must
// come back from the URL parser itself: it drops tabs and new lines and reads a backslash as a slash, so `/\t/host`
// and `/\host` are `//host`. One it cannot parse is no link.
export function isSafeAppLink(link: string | undefined): link is string {
  if (typeof link !== 'string' || !link.startsWith('/')) return false;
  try {
    return new URL(link, window.location.origin).origin === window.location.origin;
  } catch {
    return false;
  }
}

// diffNotifications returns items that are newly raised or whose updated timestamp moved since the previous snapshot.
export function diffNotifications(previous: ReadonlyMap<number, string>, next: readonly Notification[]): Notification[] {
  const changed: Notification[] = [];
  for (const item of next) {
    if (previous.get(item.id) !== item.updated) changed.push(item);
  }
  return changed;
}

// Per-browser alert preferences stored locally: in-page toasts are on by default; OS browser notifications are opt-in.
export interface NotificationPrefs {
  toasts: boolean;
  browser: boolean;
}

export const defaultNotificationPrefs: NotificationPrefs = {
  toasts: true,
  browser: false,
};

const prefsStorageKey = 'andaru.notifications.prefs';
const prefsListeners = new Set<() => void>();

function parsePrefs(raw: string | null): NotificationPrefs {
  if (!raw) return defaultNotificationPrefs;
  try {
    const parsed: unknown = JSON.parse(raw);
    if (typeof parsed !== 'object' || parsed === null) return defaultNotificationPrefs;
    const record = parsed as Record<string, unknown>;
    return {
      toasts: typeof record.toasts === 'boolean' ? record.toasts : defaultNotificationPrefs.toasts,
      browser: typeof record.browser === 'boolean' ? record.browser : defaultNotificationPrefs.browser,
    };
  } catch {
    return defaultNotificationPrefs;
  }
}

function readStoredPrefs(): NotificationPrefs | undefined {
  try {
    return parsePrefs(localStorage.getItem(prefsStorageKey));
  } catch {
    return undefined;
  }
}

let currentPrefs: NotificationPrefs = readStoredPrefs() ?? defaultNotificationPrefs;

function samePrefs(a: NotificationPrefs, b: NotificationPrefs): boolean {
  return a.toasts === b.toasts && a.browser === b.browser;
}

function notifyPrefs() {
  for (const listener of prefsListeners) listener();
}

function onPrefsStorage(event: StorageEvent) {
  if (event.key !== null && event.key !== prefsStorageKey) return;
  const next = readStoredPrefs() ?? defaultNotificationPrefs;
  if (samePrefs(currentPrefs, next)) return;
  currentPrefs = next;
  notifyPrefs();
}

export function getNotificationPrefs(): NotificationPrefs {
  return currentPrefs;
}

export function setNotificationPrefs(patch: Partial<NotificationPrefs>) {
  const next: NotificationPrefs = {
    toasts: patch.toasts ?? currentPrefs.toasts,
    browser: patch.browser ?? currentPrefs.browser,
  };
  if (samePrefs(currentPrefs, next)) return;
  currentPrefs = next;
  try {
    if (samePrefs(next, defaultNotificationPrefs)) localStorage.removeItem(prefsStorageKey);
    else localStorage.setItem(prefsStorageKey, JSON.stringify(next));
  } catch { /* storage may be unavailable; still applies for this visit */ }
  notifyPrefs();
}

export function resetNotificationPrefsForTests() {
  currentPrefs = defaultNotificationPrefs;
  try {
    localStorage.removeItem(prefsStorageKey);
  } catch { /* ignore */ }
  notifyPrefs();
}

function subscribePrefs(listener: () => void) {
  if (prefsListeners.size === 0) {
    const stored = readStoredPrefs();
    if (stored !== undefined && !samePrefs(currentPrefs, stored)) currentPrefs = stored;
    if (typeof window !== 'undefined') window.addEventListener('storage', onPrefsStorage);
  }
  prefsListeners.add(listener);
  return () => {
    prefsListeners.delete(listener);
    if (prefsListeners.size === 0 && typeof window !== 'undefined') window.removeEventListener('storage', onPrefsStorage);
  };
}

export function useNotificationPrefs(): NotificationPrefs {
  return useSyncExternalStore(subscribePrefs, getNotificationPrefs, () => defaultNotificationPrefs);
}

// Native browser Notification API support & permission helpers.
export type BrowserNotificationSupport = 'insecure' | 'unsupported' | 'denied' | 'default' | 'granted';

export function getBrowserNotificationSupport(): BrowserNotificationSupport {
  if (typeof window === 'undefined') return 'unsupported';
  if (!window.isSecureContext) return 'insecure';
  if (typeof window.Notification === 'undefined') return 'unsupported';
  const permission = window.Notification.permission;
  if (permission === 'granted' || permission === 'denied') return permission;
  return 'default';
}

const supportListeners = new Set<() => void>();

function notifySupport() {
  for (const listener of supportListeners) listener();
}

function subscribeSupport(listener: () => void) {
  if (supportListeners.size === 0 && typeof window !== 'undefined') {
    window.addEventListener('focus', notifySupport);
    document.addEventListener('visibilitychange', notifySupport);
  }
  supportListeners.add(listener);
  return () => {
    supportListeners.delete(listener);
    if (supportListeners.size === 0 && typeof window !== 'undefined') {
      window.removeEventListener('focus', notifySupport);
      document.removeEventListener('visibilitychange', notifySupport);
    }
  };
}

export function useBrowserNotificationSupport(): BrowserNotificationSupport {
  return useSyncExternalStore(subscribeSupport, getBrowserNotificationSupport, () => 'unsupported');
}

export function useBrowserNotificationsActive(): boolean {
  const prefs = useNotificationPrefs();
  const support = useBrowserNotificationSupport();
  return prefs.browser && support === 'granted';
}

// Must be called directly from a user gesture so Safari allows the permission prompt.
export async function requestBrowserNotificationPermission(): Promise<BrowserNotificationSupport> {
  const support = getBrowserNotificationSupport();
  if (support !== 'default') return support;
  try {
    await window.Notification.requestPermission();
  } catch { /* permission request rejected or unsupported */ }
  notifySupport();
  return getBrowserNotificationSupport();
}

export function showBrowserNotification(
  item: Pick<Notification, 'id' | 'severity' | 'source' | 'subject' | 'detail' | 'link'>,
  options: { hostname?: string; onOpen?: (link: string) => void } = {},
): globalThis.Notification | null {
  if (getBrowserNotificationSupport() !== 'granted') return null;
  const title = options.hostname ? `${item.subject} — ${options.hostname}` : item.subject;
  const body = item.detail?.trim() ? item.detail : `${item.severity} · ${item.source}`;
  const target = isSafeAppLink(item.link) ? item.link : '/system/notifications';
  try {
    const notice = new window.Notification(title, {
      body,
      icon: mark,
      tag: `andaru-notification-${item.id}`,
    });
    notice.onclick = () => {
      notice.close();
      window.focus();
      options.onOpen?.(target);
    };
    return notice;
  } catch {
    // Chrome on Android throws on `new Notification()` without a ServiceWorker; callers fall back to an in-page toast.
    return null;
  }
}

// Imperative toast queue subscribed by NotificationToaster so both live SSE diffs and explicit test actions share one stack.
export interface ToastMessage {
  id: number | string;
  severity: NotificationSeverity;
  subject: string;
  detail?: string;
  source?: string;
  link?: string;
}

const maxVisibleToasts = 4;
const toastDurationMs: Record<NotificationSeverity, number> = {
  info: 6_000,
  warning: 8_000,
  critical: 12_000,
};

const emptyToasts: readonly ToastMessage[] = [];
const toastListeners = new Set<() => void>();
const toastTimers = new Map<number | string, ReturnType<typeof setTimeout>>();
let currentToasts: readonly ToastMessage[] = emptyToasts;

function notifyToasts() {
  for (const listener of toastListeners) listener();
}

function clearToastTimer(id: number | string) {
  const timer = toastTimers.get(id);
  if (timer !== undefined) {
    clearTimeout(timer);
    toastTimers.delete(id);
  }
}

function scheduleToastDismiss(id: number | string, severity: NotificationSeverity) {
  clearToastTimer(id);
  toastTimers.set(id, setTimeout(() => {
    toastTimers.delete(id);
    dismissNotificationToast(id);
  }, toastDurationMs[severity]));
}

export function pushNotificationToast(message: ToastMessage) {
  const combined = [...currentToasts.filter((item) => item.id !== message.id), message];
  if (combined.length > maxVisibleToasts) {
    for (const evicted of combined.slice(0, combined.length - maxVisibleToasts)) {
      clearToastTimer(evicted.id);
    }
    currentToasts = combined.slice(-maxVisibleToasts);
  } else {
    currentToasts = combined;
  }
  scheduleToastDismiss(message.id, message.severity);
  notifyToasts();
}

export function dismissNotificationToast(id: number | string) {
  clearToastTimer(id);
  if (!currentToasts.some((item) => item.id === id)) return;
  const next = currentToasts.filter((item) => item.id !== id);
  currentToasts = next.length === 0 ? emptyToasts : next;
  notifyToasts();
}

export function pauseNotificationToast(id: number | string) {
  clearToastTimer(id);
}

export function resumeNotificationToast(id: number | string) {
  const toast = currentToasts.find((item) => item.id === id);
  if (!toast) return;
  scheduleToastDismiss(toast.id, toast.severity);
}

export function clearNotificationToasts() {
  for (const timer of toastTimers.values()) clearTimeout(timer);
  toastTimers.clear();
  if (currentToasts.length === 0) return;
  currentToasts = emptyToasts;
  notifyToasts();
}

function getNotificationToasts(): readonly ToastMessage[] {
  return currentToasts;
}

function subscribeToasts(listener: () => void): () => void {
  toastListeners.add(listener);
  return () => { toastListeners.delete(listener); };
}

export function useNotificationToasts(): readonly ToastMessage[] {
  return useSyncExternalStore(subscribeToasts, getNotificationToasts, () => emptyToasts);
}
