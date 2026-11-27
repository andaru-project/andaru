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

// isSafeAppLink allows only single-slash in-app paths that a browser cannot read as another site's. The page's own
// origin must come back from the URL parser itself: it drops tabs and new lines and reads a backslash as a slash, so
// `/\t/host` and `/\host` are `//host`. Leading `//` is also rejected so same-origin protocol-relative URLs never reach
// the SPA router. One it cannot parse is no link.
export function isSafeAppLink(link: string | undefined): link is string {
  if (typeof link !== 'string' || !link.startsWith('/') || link.startsWith('//')) return false;
  try {
    return new URL(link, window.location.origin).origin === window.location.origin;
  } catch {
    return false;
  }
}

// diffNotifications returns items that are newly raised or whose updated timestamp moved since the previous snapshot.
export function diffNotifications(previous: ReadonlyMap<number, string>, next: readonly Notification[]): Notification[] {
  return next.filter((item) => previous.get(item.id) !== item.updated);
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
let permissionStatus: PermissionStatus | null = null;
let permissionQueryGeneration = 0;

function notifySupport() {
  for (const listener of supportListeners) listener();
}

function subscribeSupport(listener: () => void) {
  if (supportListeners.size === 0 && typeof window !== 'undefined') {
    window.addEventListener('focus', notifySupport);
    document.addEventListener('visibilitychange', notifySupport);
    // Watch live permission changes (e.g. site-settings toggle in the address bar) when supported.
    const generation = ++permissionQueryGeneration;
    if (typeof navigator !== 'undefined' && typeof navigator.permissions?.query === 'function') {
      void navigator.permissions.query({ name: 'notifications' as PermissionName }).then((status) => {
        if (generation !== permissionQueryGeneration || supportListeners.size === 0) return;
        permissionStatus = status;
        status.addEventListener('change', notifySupport);
      }).catch(() => {});
    }
  }
  supportListeners.add(listener);
  return () => {
    supportListeners.delete(listener);
    if (supportListeners.size === 0 && typeof window !== 'undefined') {
      permissionQueryGeneration++;
      window.removeEventListener('focus', notifySupport);
      document.removeEventListener('visibilitychange', notifySupport);
      if (permissionStatus) {
        permissionStatus.removeEventListener('change', notifySupport);
        permissionStatus = null;
      }
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

// ToastMessage is the shared payload for both in-page toasts and native browser notifications.
export interface ToastMessage {
  id: number | string;
  severity: NotificationSeverity;
  subject: string;
  detail?: string;
  source?: string;
  link?: string;
}

export function showBrowserNotification(
  item: ToastMessage,
  options: { hostname?: string; onOpen?: (link: string) => void } = {},
): globalThis.Notification | null {
  if (getBrowserNotificationSupport() !== 'granted') {
    // Sync subscribers immediately if permission was revoked while the tab stayed focused or hidden.
    notifySupport();
    return null;
  }
  const title = options.hostname ? `${item.subject} — ${options.hostname}` : item.subject;
  const fallbackBody = item.source ? `${item.severity} · ${item.source}` : item.severity;
  const body = item.detail?.trim() ? item.detail : fallbackBody;
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
export const maxVisibleToasts = 4;

const toastDurationMs: Record<NotificationSeverity, number> = {
  info: 6_000,
  warning: 8_000,
  critical: 12_000,
};

const emptyToasts: readonly ToastMessage[] = [];
const toastListeners = new Set<() => void>();
const toastTimers = new Map<number | string, ReturnType<typeof setTimeout>>();
const pausedToasts = new Set<number | string>();
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
  const duration = toastDurationMs[severity] ?? toastDurationMs.info;
  toastTimers.set(id, setTimeout(() => {
    toastTimers.delete(id);
    dismissNotificationToast(id);
  }, duration));
}

export function pushNotificationToast(message: ToastMessage) {
  const combined = [...currentToasts.filter((item) => item.id !== message.id), message];
  if (combined.length > maxVisibleToasts) {
    for (const evicted of combined.slice(0, -maxVisibleToasts)) {
      clearToastTimer(evicted.id);
      pausedToasts.delete(evicted.id);
    }
    currentToasts = combined.slice(-maxVisibleToasts);
  } else {
    currentToasts = combined;
  }
  // Keep timers paused while the toast is hovered/focused or while the tab is hidden (so fallback toasts wait to be seen).
  const hidden = typeof document !== 'undefined' && document.visibilityState === 'hidden';
  if (!pausedToasts.has(message.id) && !hidden) {
    scheduleToastDismiss(message.id, message.severity);
  }
  notifyToasts();
}

export function dismissNotificationToast(id: number | string) {
  clearToastTimer(id);
  pausedToasts.delete(id);
  if (!currentToasts.some((item) => item.id === id)) return;
  const next = currentToasts.filter((item) => item.id !== id);
  currentToasts = next.length === 0 ? emptyToasts : next;
  notifyToasts();
}

export function pauseNotificationToast(id: number | string) {
  pausedToasts.add(id);
  clearToastTimer(id);
}

export function resumeNotificationToast(id: number | string) {
  pausedToasts.delete(id);
  const toast = currentToasts.find((item) => item.id === id);
  if (!toast) return;
  scheduleToastDismiss(toast.id, toast.severity);
}

export function resumeHiddenNotificationToasts() {
  for (const toast of currentToasts) {
    if (!pausedToasts.has(toast.id) && !toastTimers.has(toast.id)) {
      scheduleToastDismiss(toast.id, toast.severity);
    }
  }
}

export function clearNotificationToasts() {
  for (const timer of toastTimers.values()) clearTimeout(timer);
  toastTimers.clear();
  pausedToasts.clear();
  if (currentToasts.length === 0) return;
  currentToasts = emptyToasts;
  notifyToasts();
}

function subscribeToasts(listener: () => void): () => void {
  toastListeners.add(listener);
  return () => { toastListeners.delete(listener); };
}

export function useNotificationToasts(): readonly ToastMessage[] {
  return useSyncExternalStore(subscribeToasts, () => currentToasts, () => emptyToasts);
}
