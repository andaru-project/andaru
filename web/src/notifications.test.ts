import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  clearNotificationToasts,
  defaultNotificationPrefs,
  diffNotifications,
  dismissNotificationToast,
  getBrowserNotificationSupport,
  getNotificationPrefs,
  highestSeverity,
  isSafeAppLink,
  pauseNotificationToast,
  pushNotificationToast,
  requestBrowserNotificationPermission,
  resetNotificationPrefsForTests,
  resumeNotificationToast,
  setNotificationPrefs,
  showBrowserNotification,
} from './notifications';
import type { Notification } from './types';

const originalNotification = window.Notification;
const originalIsSecureContext = Object.getOwnPropertyDescriptor(window, 'isSecureContext');

const item = (overrides: Partial<Notification> = {}): Notification => ({
  id: 1,
  source: 'applies',
  key: 'apply/1',
  severity: 'warning',
  subject: 'Apply awaiting confirmation',
  detail: 'Confirm within 60 seconds.',
  link: '/changes',
  raised: '2026-11-27T08:00:00Z',
  updated: '2026-11-27T08:00:00Z',
  ...overrides,
});

function setSecureContext(secure: boolean) {
  Object.defineProperty(window, 'isSecureContext', { configurable: true, value: secure });
}

function stubNotification(options: {
  permission?: NotificationPermission;
  requestResult?: NotificationPermission;
  throws?: boolean;
} = {}) {
  const instances: Array<{
    title: string;
    options?: NotificationOptions;
    close: ReturnType<typeof vi.fn>;
    onclick: ((this: globalThis.Notification, ev: Event) => unknown) | null;
    onclose: ((this: globalThis.Notification, ev: Event) => unknown) | null;
  }> = [];
  let permission: NotificationPermission = options.permission ?? 'default';
  const requestPermission = vi.fn(async () => {
    permission = options.requestResult ?? permission;
    return permission;
  });

  class FakeNotification {
    title: string;
    options?: NotificationOptions;
    close = vi.fn();
    onclick: ((this: globalThis.Notification, ev: Event) => unknown) | null = null;
    onclose: ((this: globalThis.Notification, ev: Event) => unknown) | null = null;

    constructor(title: string, opts?: NotificationOptions) {
      if (options.throws) throw new TypeError('Illegal constructor');
      this.title = title;
      this.options = opts;
      instances.push(this);
    }

    static get permission(): NotificationPermission {
      return permission;
    }

    static requestPermission = requestPermission;
  }

  Object.defineProperty(window, 'Notification', { configurable: true, writable: true, value: FakeNotification });
  return { instances, requestPermission, setPermission: (next: NotificationPermission) => { permission = next; } };
}

afterEach(() => {
  vi.useRealTimers();
  clearNotificationToasts();
  resetNotificationPrefsForTests();
  if (originalIsSecureContext) Object.defineProperty(window, 'isSecureContext', originalIsSecureContext);
  else Object.defineProperty(window, 'isSecureContext', { configurable: true, value: true });
  Object.defineProperty(window, 'Notification', { configurable: true, writable: true, value: originalNotification });
});

describe('notifications helpers', () => {
  it('picks highestSeverity and validates in-app links', () => {
    expect(highestSeverity([])).toBeUndefined();
    expect(highestSeverity([item({ severity: 'info' }), item({ id: 2, severity: 'critical' }), item({ id: 3, severity: 'warning' })])).toBe('critical');

    expect(isSafeAppLink('/changes')).toBe(true);
    expect(isSafeAppLink('/system/notifications?tab=active')).toBe(true);
    expect(isSafeAppLink(undefined)).toBe(false);
    expect(isSafeAppLink('https://example.com')).toBe(false);
    expect(isSafeAppLink('//evil.example/path')).toBe(false);
    expect(isSafeAppLink('/\\evil.example')).toBe(false);
    expect(isSafeAppLink('/\t/evil.example')).toBe(false);
  });

  it('diffs newly raised and updated notifications against a previous snapshot', () => {
    const previous = new Map<number, string>([
      [1, '2026-11-27T08:00:00Z'],
      [2, '2026-11-27T08:01:00Z'],
    ]);
    const next = [
      item({ id: 3, updated: '2026-11-27T08:05:00Z' }),
      item({ id: 2, updated: '2026-11-27T08:04:00Z' }),
      item({ id: 1, updated: '2026-11-27T08:00:00Z' }),
    ];
    expect(diffNotifications(previous, next).map((n) => n.id)).toEqual([3, 2]);
  });

  it('stores notification preferences in localStorage, removes the key on default reset, and syncs across storage events', () => {
    const store = new Map<string, string>();
    const memoryStorage = {
      getItem: (key: string) => store.get(key) ?? null,
      setItem: (key: string, value: string) => { store.set(key, value); },
      removeItem: (key: string) => { store.delete(key); },
      clear: () => { store.clear(); },
    };
    const originalStorage = Object.getOwnPropertyDescriptor(globalThis, 'localStorage');
    Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: memoryStorage });
    try {
      expect(getNotificationPrefs()).toEqual(defaultNotificationPrefs);
      setNotificationPrefs({ browser: true });
      expect(getNotificationPrefs()).toEqual({ toasts: true, browser: true });
      expect(store.get('andaru.notifications.prefs')).toBe(JSON.stringify({ toasts: true, browser: true }));

      setNotificationPrefs({ browser: false });
      expect(getNotificationPrefs()).toEqual(defaultNotificationPrefs);
      expect(store.has('andaru.notifications.prefs')).toBe(false);
    } finally {
      if (originalStorage) Object.defineProperty(globalThis, 'localStorage', originalStorage);
    }
  });

  it('reports browser notification support across insecure, unsupported, denied, default and granted states', async () => {
    setSecureContext(false);
    expect(getBrowserNotificationSupport()).toBe('insecure');

    setSecureContext(true);
    Object.defineProperty(window, 'Notification', { configurable: true, writable: true, value: undefined });
    expect(getBrowserNotificationSupport()).toBe('unsupported');

    const stub = stubNotification({ permission: 'denied' });
    expect(getBrowserNotificationSupport()).toBe('denied');
    expect(await requestBrowserNotificationPermission()).toBe('denied');
    expect(stub.requestPermission).not.toHaveBeenCalled();

    const promptStub = stubNotification({ permission: 'default', requestResult: 'granted' });
    expect(getBrowserNotificationSupport()).toBe('default');
    expect(await requestBrowserNotificationPermission()).toBe('granted');
    expect(promptStub.requestPermission).toHaveBeenCalledTimes(1);
    expect(getBrowserNotificationSupport()).toBe('granted');
  });

  it('shows native browser notifications with focus/navigation on click and returns null when blocked or throwing', () => {
    setSecureContext(true);
    const focusSpy = vi.spyOn(window, 'focus').mockImplementation(() => {});
    const onOpen = vi.fn();

    stubNotification({ permission: 'default' });
    expect(showBrowserNotification(item(), { hostname: 'gw01', onOpen })).toBeNull();

    const granted = stubNotification({ permission: 'granted' });
    const notice = showBrowserNotification(item({ detail: undefined, link: '//evil.example' }), { hostname: 'gw01', onOpen });
    expect(notice).not.toBeNull();
    expect(granted.instances).toHaveLength(1);
    expect(granted.instances[0].title).toBe('Apply awaiting confirmation — gw01');
    expect(granted.instances[0].options?.body).toBe('warning · applies');
    expect(granted.instances[0].options?.tag).toBe('andaru-notification-1');

    granted.instances[0].onclick?.call(notice!, new Event('click'));
    expect(granted.instances[0].close).toHaveBeenCalledTimes(1);
    expect(focusSpy).toHaveBeenCalledTimes(1);
    expect(onOpen).toHaveBeenCalledWith('/system/notifications');

    stubNotification({ permission: 'granted', throws: true });
    expect(showBrowserNotification(item(), { onOpen })).toBeNull();
    focusSpy.mockRestore();
  });

  it('auto-dismisses queued toasts and respects pause/resume', () => {
    vi.useFakeTimers();
    pushNotificationToast({ id: 1, severity: 'info', subject: 'Disk almost full' });
    pauseNotificationToast(1);
    vi.advanceTimersByTime(20_000);
    // Still dismissible because pause cleared the timer.
    resumeNotificationToast(1);
    vi.advanceTimersByTime(5_999);
    // Still present until full 6s elapses, then cleared.
    vi.advanceTimersByTime(1);
    // Calling dismiss after auto-dismiss is a safe no-op.
    dismissNotificationToast(1);
  });
});
