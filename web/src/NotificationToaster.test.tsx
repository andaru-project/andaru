import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';
import { NotificationToaster } from './NotificationToaster';
import {
  clearNotificationToasts,
  defaultNotificationPrefs,
  pushNotificationToast,
  setNotificationPrefs,
  useBrowserNotificationsActive,
} from './notifications';
import { act, fireEvent, render, screen, within } from './test/render';
import type { Notification, NotificationList } from './types';

const originalNotification = window.Notification;
const originalIsSecureContext = Object.getOwnPropertyDescriptor(window, 'isSecureContext');

let visibility: DocumentVisibilityState = 'visible';
Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => visibility });

const sample = (overrides: Partial<Notification> = {}): Notification => ({
  id: 1,
  source: 'disk',
  key: 'disk/var',
  severity: 'warning',
  subject: 'Disk /var is 90% full',
  detail: 'Free space dropped below 10%.',
  link: '/system/details',
  raised: '2026-11-27T08:00:00Z',
  updated: '2026-11-27T08:00:00Z',
  ...overrides,
});

function LocationProbe() {
  const { pathname } = useLocation();
  return <div data-testid="location">{pathname}</div>;
}

function BrowserActiveProbe() {
  return <div data-testid="browser-active">{String(useBrowserNotificationsActive())}</div>;
}

function mountToaster(notifications: NotificationList | undefined, hostname = 'fw01') {
  const ui = (list: NotificationList | undefined) => (
    <MemoryRouter initialEntries={['/']}>
      <Routes>
        <Route path="*" element={<LocationProbe />} />
      </Routes>
      <BrowserActiveProbe />
      <NotificationToaster notifications={list} hostname={hostname} />
    </MemoryRouter>
  );
  const view = render(ui(notifications));
  return {
    ...view,
    update: (list: NotificationList | undefined) => view.rerender(ui(list)),
  };
}

function stubNotification(options: { permission?: NotificationPermission; throws?: boolean } = {}) {
  const instances: Array<{
    title: string;
    options?: NotificationOptions;
    close: ReturnType<typeof vi.fn>;
    onclick: ((this: globalThis.Notification, ev: Event) => unknown) | null;
    onclose: ((this: globalThis.Notification, ev: Event) => unknown) | null;
  }> = [];
  let permission: NotificationPermission = options.permission ?? 'granted';

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

    static requestPermission = vi.fn(async () => permission);
  }

  Object.defineProperty(window, 'isSecureContext', { configurable: true, value: true });
  Object.defineProperty(window, 'Notification', { configurable: true, writable: true, value: FakeNotification });
  return { instances, setPermission: (next: NotificationPermission) => { permission = next; } };
}

afterEach(() => {
  visibility = 'visible';
  vi.useRealTimers();
  clearNotificationToasts();
  setNotificationPrefs(defaultNotificationPrefs);
  if (originalIsSecureContext) Object.defineProperty(window, 'isSecureContext', originalIsSecureContext);
  else Object.defineProperty(window, 'isSecureContext', { configurable: true, value: true });
  Object.defineProperty(window, 'Notification', { configurable: true, writable: true, value: originalNotification });
});

it('seeds existing notifications silently, keeps pre-mounted live regions outside CloseButton, and renders valid button HTML', async () => {
  const user = userEvent.setup();
  const initial = sample({ id: 1, subject: 'Existing alert' });
  const view = mountToaster(undefined);

  // Persistent polite and assertive live regions are mounted before any toast arrives; no empty region landmark exists.
  expect(document.querySelector('[aria-live="polite"]')).not.toBeNull();
  expect(document.querySelector('[aria-live="assertive"]')).not.toBeNull();
  expect(screen.queryByRole('region', { name: 'Live notifications' })).not.toBeInTheDocument();

  view.update({ items: [initial] });
  expect(screen.queryByRole('region', { name: 'Live notifications' })).not.toBeInTheDocument();

  // New critical notification announces in the assertive live region and renders in the visible stack.
  const critical = sample({ id: 2, severity: 'critical', subject: 'Apply 9 rolled back', link: '/changes' });
  view.update({ items: [critical, initial] });
  const region = screen.getByRole('region', { name: 'Live notifications' });
  expect(region).toHaveTextContent('Apply 9 rolled back');
  expect(document.querySelector('[aria-live="assertive"]')).toHaveTextContent('critical: Apply 9 rolled back');
  expect(document.querySelector('[aria-live="assertive"]')?.querySelector('.notification-toast-close')).toBeNull();

  // Toast body button contains only phrasing elements (no <div> or <p> inside <button>).
  const bodyButton = within(region).getByRole('button', { name: /^critical Apply 9 rolled back/ });
  expect(bodyButton.querySelector('div, p')).toBeNull();

  // Clicking the toast navigates to its safe in-app link and dismisses it.
  await user.click(bodyButton);
  expect(screen.getByTestId('location')).toHaveTextContent('/changes');
  expect(screen.queryByRole('region', { name: 'Live notifications' })).not.toBeInTheDocument();

  // Updating an existing notification's timestamp announces politely; unsafe links fall back to /system/notifications.
  view.update({ items: [critical, sample({ id: 1, subject: 'Existing alert escalated', link: '//evil.example', updated: '2026-11-27T08:05:00Z' })] });
  expect(document.querySelector('[aria-live="polite"]')).toHaveTextContent('warning: Existing alert escalated');
  await user.click(screen.getByRole('button', { name: /^warning Existing alert escalated/ }));
  expect(screen.getByTestId('location')).toHaveTextContent('/system/notifications');
});

it('dismisses toasts via close button, auto-dismiss timeout (pausing on hover/focus), and when cleared from the active list', () => {
  vi.useFakeTimers();
  const view = mountToaster({ items: [] });

  view.update({ items: [sample({ id: 10, severity: 'info', subject: 'Update ready' })] });
  const region = screen.getByRole('region', { name: 'Live notifications' });
  const toastCard = region.querySelector('.notification-toast') as HTMLElement;
  expect(toastCard).toHaveTextContent('Update ready');

  // Hover pauses auto-dismiss past the 6s timeout.
  fireEvent.mouseEnter(toastCard);
  act(() => { vi.advanceTimersByTime(10_000); });
  expect(screen.getByRole('region', { name: 'Live notifications' })).toBeInTheDocument();

  // Focus inside toast prevents mouseLeave from resuming the timer prematurely.
  const bodyButton = screen.getByRole('button', { name: /^info Update ready/ });
  bodyButton.focus();
  fireEvent.mouseLeave(toastCard);
  act(() => { vi.advanceTimersByTime(10_000); });
  expect(screen.getByRole('region', { name: 'Live notifications' })).toBeInTheDocument();

  // Blurring outside resumes timeout and removes the toast after 6s.
  fireEvent.blur(bodyButton, { relatedTarget: document.body });
  act(() => { vi.advanceTimersByTime(6_000); });
  expect(screen.queryByRole('region', { name: 'Live notifications' })).not.toBeInTheDocument();

  // A notification cleared from the backend list immediately closes its open toast.
  view.update({ items: [sample({ id: 11, severity: 'warning', subject: 'Link down' })] });
  expect(screen.getByRole('region', { name: 'Live notifications' })).toHaveTextContent('Link down');
  view.update({ items: [] });
  expect(screen.queryByRole('region', { name: 'Live notifications' })).not.toBeInTheDocument();

  // Close button dismisses without navigating.
  act(() => { pushNotificationToast({ id: 'test-1', severity: 'info', subject: 'Manual close test', link: '/changes' }); });
  fireEvent.click(screen.getByRole('button', { name: 'Close notification: Manual close test' }));
  expect(screen.queryByRole('region', { name: 'Live notifications' })).not.toBeInTheDocument();
  expect(screen.getByTestId('location')).toHaveTextContent('/');
});

it('caps bursts to 4 visible toasts and stays silent when in-page toasts are disabled', () => {
  const view = mountToaster({ items: [] });
  const burst = [6, 5, 4, 3, 2, 1].map((id) => sample({ id, subject: `Burst ${id}` }));
  view.update({ items: burst });
  const region = screen.getByRole('region', { name: 'Live notifications' });
  const cards = region.querySelectorAll('.notification-toast');
  expect(cards).toHaveLength(4);
  expect(Array.from(cards).map((el) => el.textContent)).toEqual(expect.arrayContaining([
    expect.stringContaining('Burst 6'),
    expect.stringContaining('Burst 5'),
    expect.stringContaining('Burst 4'),
    expect.stringContaining('Burst 3'),
  ]));

  act(() => {
    clearNotificationToasts();
    setNotificationPrefs({ toasts: false });
  });
  view.update({ items: [sample({ id: 99, subject: 'Muted alert' }), ...burst] });
  expect(screen.queryByRole('region', { name: 'Live notifications' })).not.toBeInTheDocument();
});

it('routes background/idle alerts to native notifications, caps bursts at 3, syncs revoked permission while hidden, and holds hidden fallback toasts until visible', () => {
  vi.useFakeTimers();
  const focusSpy = vi.spyOn(window, 'focus').mockImplementation(() => {});
  const stub = stubNotification({ permission: 'granted' });
  setNotificationPrefs({ toasts: true, browser: true });

  const view = mountToaster({ items: [] }, 'edge-gw');
  expect(screen.getByTestId('browser-active')).toHaveTextContent('true');

  // Active visible tab still gets an in-page toast instead of an OS notification.
  view.update({ items: [sample({ id: 1, subject: 'Active tab alert' })] });
  expect(stub.instances).toHaveLength(0);
  expect(screen.getByRole('region', { name: 'Live notifications' })).toHaveTextContent('Active tab alert');

  // Updating alert 1 while hidden delivers a native notification and dismisses its earlier stale in-page toast.
  visibility = 'hidden';
  view.update({ items: [sample({ id: 1, subject: 'Active tab alert escalated', updated: '2026-11-27T08:02:00Z' })] });
  expect(stub.instances).toHaveLength(1);
  expect(stub.instances[0].title).toBe('Active tab alert escalated — edge-gw');
  expect(screen.queryByRole('region', { name: 'Live notifications' })).not.toBeInTheDocument();

  // Another hidden-tab alert routes to a native browser notification; clicking it focuses and navigates.
  view.update({ items: [sample({ id: 2, subject: 'Background alert', link: '/changes' }), sample({ id: 1, subject: 'Active tab alert escalated', updated: '2026-11-27T08:02:00Z' })] });
  expect(stub.instances).toHaveLength(2);
  expect(stub.instances[1].title).toBe('Background alert — edge-gw');
  expect(screen.queryByRole('region', { name: 'Live notifications' })).not.toBeInTheDocument();

  act(() => { stub.instances[1].onclick?.call(stub.instances[1] as unknown as globalThis.Notification, new Event('click')); });
  expect(focusSpy).toHaveBeenCalledTimes(1);
  expect(screen.getByTestId('location')).toHaveTextContent('/changes');

  // Updating alert 2 once back in the active tab closes its old native handle before popping a toast.
  visibility = 'visible';
  act(() => { document.dispatchEvent(new Event('visibilitychange')); });
  view.update({ items: [sample({ id: 2, subject: 'Background alert updated', updated: '2026-11-27T08:05:00Z' }), sample({ id: 1, subject: 'Active tab alert escalated', updated: '2026-11-27T08:02:00Z' })] });
  expect(stub.instances[1].close).toHaveBeenCalledTimes(2);
  expect(screen.getByRole('region', { name: 'Live notifications' })).toHaveTextContent('Background alert updated');
  act(() => { clearNotificationToasts(); });

  // Visible but idle (> 3 min without interaction) caps native bursts at 3.
  act(() => { vi.advanceTimersByTime(3 * 60_000 + 1_000); });
  const idleBurst = [10, 11, 12, 13].map((id) => sample({ id, subject: `Idle ${id}` }));
  view.update({ items: [...idleBurst, sample({ id: 2, subject: 'Background alert updated', updated: '2026-11-27T08:05:00Z' }), sample({ id: 1, subject: 'Active tab alert escalated', updated: '2026-11-27T08:02:00Z' })] });
  expect(stub.instances).toHaveLength(5); // 2 earlier + 3 capped

  // Revoking permission while hidden immediately syncs useBrowserNotificationsActive() to false and queues a fallback toast.
  visibility = 'hidden';
  stub.setPermission('denied');
  view.update({ items: [sample({ id: 20, severity: 'info', subject: 'Hidden fallback alert' }), ...idleBurst] });
  expect(screen.getByTestId('browser-active')).toHaveTextContent('false');
  act(() => { vi.advanceTimersByTime(20_000); });
  expect(screen.getByRole('region', { name: 'Live notifications' })).toHaveTextContent('Hidden fallback alert');

  visibility = 'visible';
  act(() => { document.dispatchEvent(new Event('visibilitychange')); });
  act(() => { vi.advanceTimersByTime(6_000); });
  expect(screen.queryByRole('region', { name: 'Live notifications' })).not.toBeInTheDocument();

  // Unmounting closes remaining open native handles.
  view.unmount();
  expect(stub.instances[2].close).toHaveBeenCalledTimes(1);
  expect(stub.instances[3].close).toHaveBeenCalledTimes(1);
  expect(stub.instances[4].close).toHaveBeenCalledTimes(1);

  focusSpy.mockRestore();
});
