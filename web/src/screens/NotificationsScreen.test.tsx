import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { NotificationToaster } from '../NotificationToaster';
import { clearNotificationToasts, defaultNotificationPrefs, setNotificationPrefs } from '../notifications';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { session } from '../test/fixtures';
import { render, screen, within } from '../test/render';
import { setTimeZone } from '../timezone';
import type { Notification, NotificationList } from '../types';
import type { Resource } from '../useResource';
import { NotificationsScreen, NotificationsWidget } from './NotificationsScreen';

const originalNotification = window.Notification;
const originalIsSecureContext = Object.getOwnPropertyDescriptor(window, 'isSecureContext');

const sample = (overrides: Partial<Notification> = {}): Notification => ({
  id: 1,
  source: 'applies',
  key: 'apply/12',
  severity: 'critical',
  subject: 'Apply 12 rolled back automatically',
  detail: 'Line one\n<b>not html</b>',
  link: '/changes',
  raised: '2026-11-23T09:00:00Z',
  updated: '2026-11-23T10:15:00Z',
  ...overrides,
});

const resourceOf = (fields: Partial<Resource<NotificationList | undefined>> = {}): Resource<NotificationList | undefined> => ({
  data: { items: [] },
  error: '',
  loading: false,
  reload: vi.fn(async () => undefined),
  refresh: vi.fn(),
  ...fields,
});

afterEach(() => {
  setTimeZone('local');
  clearNotificationToasts();
  setNotificationPrefs(defaultNotificationPrefs);
  if (originalIsSecureContext) Object.defineProperty(window, 'isSecureContext', originalIsSecureContext);
  else Object.defineProperty(window, 'isSecureContext', { configurable: true, value: true });
  Object.defineProperty(window, 'Notification', { configurable: true, writable: true, value: originalNotification });
});

it('lists subject, severity badge, source, formatted timestamp, plain-text detail and in-app link', () => {
  setTimeZone('UTC');
  const items: Notification[] = [
    sample(),
    sample({ id: 2, source: 'upgrade', key: 'upgrade/pending', severity: 'warning', subject: 'Upgrade decisions waiting', detail: undefined, link: '/system/upgrade', updated: '2026-11-23T08:30:00Z' }),
    sample({ id: 3, source: 'disk', key: 'disk/var', severity: 'info', subject: 'External link ignored', detail: 'Check disk usage', link: '//evil.example/phish', updated: '2026-11-23T07:00:00Z' }),
    sample({ id: 4, severity: 'info', subject: 'Tab link ignored', link: '/\t/evil.example', updated: '2026-11-23T06:00:00Z' }),
    sample({ id: 5, severity: 'info', subject: 'Backslash link ignored', link: '/\\evil.example', updated: '2026-11-23T05:00:00Z' }),
    sample({ id: 6, severity: 'info', subject: 'Unparsable link ignored', link: '//a b', updated: '2026-11-23T04:00:00Z' }),
  ];

  render(
    <MemoryRouter>
      <NotificationsScreen api={new ApiClient('csrf')} session={session(['notifications.read'])} notifications={resourceOf({ data: { items } })} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );

  const articles = screen.getAllByRole('article');
  expect(articles).toHaveLength(6);

  expect(within(articles[0]).getByText('critical')).toBeInTheDocument();
  expect(within(articles[0]).getByText('Apply 12 rolled back automatically')).toBeInTheDocument();
  expect(within(articles[0]).getByText('2026-11-23 10:15:00 +00:00 · applies')).toBeInTheDocument();
  const detail = within(articles[0]).getByText((_, node) => node?.classList.contains('notification-detail') ?? false);
  expect(detail.textContent).toBe('Line one\n<b>not html</b>');
  expect(articles[0].querySelector('b')).toBeNull();
  expect(within(articles[0]).getByRole('link', { name: 'Open Apply 12 rolled back automatically' })).toHaveAttribute('href', '/changes');

  expect(within(articles[1]).getByText('warning')).toBeInTheDocument();
  expect(within(articles[1]).getByRole('link', { name: 'Open Upgrade decisions waiting' })).toHaveAttribute('href', '/system/upgrade');

  expect(within(articles[2]).getByText('info')).toBeInTheDocument();
  expect(within(articles[2]).queryByRole('link')).not.toBeInTheDocument();
  expect(within(articles[3]).queryByRole('link')).not.toBeInTheDocument();
  expect(within(articles[4]).queryByRole('link')).not.toBeInTheDocument();
  expect(within(articles[5]).queryByRole('link')).not.toBeInTheDocument();
});

it('reloads the list when the notification is already gone, and hands a lost session back without reloading', async () => {
  let status = 404;
  fetchRoutes({
    'POST /api/v1/notifications/1/dismiss': () => jsonResponse(status, { error: { code: 'NOT_FOUND', message: 'Not found.' } }),
  });
  const reload = vi.fn(async () => undefined);
  const onSessionLost = vi.fn();
  render(
    <MemoryRouter>
      <NotificationsScreen api={new ApiClient('csrf')} session={session(['notifications.read', 'notifications.manage'])} notifications={resourceOf({ data: { items: [sample()] }, reload })} onSessionLost={onSessionLost} />
    </MemoryRouter>,
  );
  const button = screen.getByRole('button', { name: 'Dismiss Apply 12 rolled back automatically' });
  await userEvent.click(button);
  expect(reload).toHaveBeenCalledTimes(1);
  expect(screen.queryByRole('alert')).not.toBeInTheDocument();

  status = 401;
  await userEvent.click(button);
  expect(onSessionLost).toHaveBeenCalledTimes(1);
  expect(reload).toHaveBeenCalledTimes(1);
});

it('shows Dismiss only with notifications.manage and reloads the list after dismissing', async () => {
  const reload = vi.fn(async () => undefined);
  const fetchMock = fetchRoutes({
    'POST /api/v1/notifications/1/dismiss': () => new Response(null, { status: 204 }),
  });

  const readerView = render(
    <MemoryRouter>
      <NotificationsScreen api={new ApiClient('csrf')} session={session(['notifications.read'])} notifications={resourceOf({ data: { items: [sample()] }, reload })} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );
  expect(screen.queryByRole('button', { name: /Dismiss/ })).not.toBeInTheDocument();
  readerView.unmount();

  render(
    <MemoryRouter>
      <NotificationsScreen api={new ApiClient('csrf')} session={session(['notifications.read', 'notifications.manage'])} notifications={resourceOf({ data: { items: [sample()] }, reload })} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );
  await userEvent.click(screen.getByRole('button', { name: 'Dismiss Apply 12 rolled back automatically' }));

  const call = fetchMock.mock.calls.find(([url, init]) => url === '/api/v1/notifications/1/dismiss' && init?.method === 'POST');
  expect(call).toBeDefined();
  expect(call?.[1]?.body).toBeUndefined();
  expect(new Headers(call?.[1]?.headers).get('X-Andaru-CSRF')).toBe('csrf');
  expect(reload).toHaveBeenCalledTimes(1);
});

it('shows the 503 unavailable alert and the empty state', async () => {
  const unavailableView = render(
    <MemoryRouter>
      <NotificationsScreen
        api={new ApiClient('csrf')}
        session={session(['notifications.read'])}
        notifications={resourceOf({ data: undefined, error: 'Notifications are temporarily unavailable.' })}
        onSessionLost={vi.fn()}
      />
    </MemoryRouter>,
  );
  const alert = screen.getByRole('alert');
  expect(alert).toHaveTextContent('The notifications could not be read');
  expect(alert).toHaveTextContent('Notifications are temporarily unavailable.');
  expect(screen.queryByText('No active notifications.')).not.toBeInTheDocument();
  unavailableView.unmount();

  render(
    <MemoryRouter>
      <NotificationsScreen
        api={new ApiClient('csrf')}
        session={session(['notifications.read'])}
        notifications={resourceOf({ data: { items: [] } })}
        onSessionLost={vi.fn()}
      />
    </MemoryRouter>,
  );
  expect(screen.getByRole('status')).toHaveTextContent('No active notifications.');
});

it('says the dismiss failed, with the message of the server, when it fails with 503', async () => {
  fetchRoutes({
    'POST /api/v1/notifications/1/dismiss': () => jsonResponse(503, { error: { code: 'NOTIFICATIONS_UNAVAILABLE', message: 'Notifications are temporarily unavailable.' } }),
  });
  render(
    <MemoryRouter>
      <NotificationsScreen
        api={new ApiClient('csrf')}
        session={session(['notifications.read', 'notifications.manage'])}
        notifications={resourceOf({ data: { items: [sample()] } })}
        onSessionLost={vi.fn()}
      />
    </MemoryRouter>,
  );

  await userEvent.click(screen.getByRole('button', { name: 'Dismiss Apply 12 rolled back automatically' }));
  const alert = await screen.findByRole('alert');
  expect(alert).toHaveTextContent('Could not dismiss the notification');
  expect(alert).toHaveTextContent('Notifications are temporarily unavailable.');
});

it('renders empty and unavailable states in NotificationsWidget', () => {
  const emptyView = render(<NotificationsWidget resource={resourceOf({ data: { items: [] } })} />);
  expect(screen.getByText('No active notifications.')).toBeInTheDocument();
  emptyView.unmount();

  render(<NotificationsWidget resource={resourceOf({ data: undefined, error: 'Notifications are temporarily unavailable.' })} />);
  expect(screen.getByRole('alert')).toHaveTextContent('Notifications are temporarily unavailable.');
});

it('lets the user configure in-page alerts, opt into browser notifications, and send a test alert', async () => {
  const user = userEvent.setup();
  let permission: NotificationPermission = 'default';
  const shown: Array<{ title: string; body?: string }> = [];
  class FakeNotification {
    close = vi.fn();
    onclick: (() => void) | null = null;
    constructor(title: string, opts?: NotificationOptions) {
      shown.push({ title, body: opts?.body });
    }
    static get permission() { return permission; }
    static requestPermission = vi.fn(async () => {
      permission = 'granted';
      return permission;
    });
  }
  Object.defineProperty(window, 'isSecureContext', { configurable: true, value: true });
  Object.defineProperty(window, 'Notification', { configurable: true, writable: true, value: FakeNotification });

  render(
    <MemoryRouter>
      <NotificationsScreen api={new ApiClient('csrf')} session={session(['notifications.read'])} notifications={resourceOf({ data: { items: [] } })} hostname="gw01" onSessionLost={vi.fn()} />
      <NotificationToaster notifications={{ items: [] }} hostname="gw01" />
    </MemoryRouter>,
  );

  const inPageSwitch = screen.getByRole('switch', { name: /In-page alerts/ });
  const browserSwitch = screen.getByRole('switch', { name: /Browser notifications/ });
  const testButton = screen.getByRole('button', { name: 'Send test' });

  expect(inPageSwitch).toBeChecked();
  expect(browserSwitch).not.toBeChecked();
  expect(testButton).toBeEnabled();

  // Send test while only in-page alerts are enabled pops an in-page toast.
  await user.click(testButton);
  expect(screen.getByText('Test notification')).toBeInTheDocument();
  expect(shown).toHaveLength(0);

  // Enabling browser notifications asks permission and turns the switch on.
  await user.click(browserSwitch);
  expect(FakeNotification.requestPermission).toHaveBeenCalledTimes(1);
  expect(browserSwitch).toBeChecked();

  // Send test now dispatches a native OS notification.
  await user.click(testButton);
  expect(shown).toHaveLength(1);
  expect(shown[0].title).toBe('Test notification — gw01');

  // Turning both switches off disables the Send test button.
  await user.click(browserSwitch);
  await user.click(inPageSwitch);
  expect(testButton).toBeDisabled();
});

it('explains why browser notifications are unavailable on plain HTTP or when denied', () => {
  Object.defineProperty(window, 'isSecureContext', { configurable: true, value: false });
  const insecure = render(
    <MemoryRouter>
      <NotificationsScreen api={new ApiClient('csrf')} session={session(['notifications.read'])} notifications={resourceOf({ data: { items: [] } })} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );
  expect(screen.getByRole('switch', { name: /Browser notifications/ })).toBeDisabled();
  expect(screen.getByText(/require HTTPS/)).toBeInTheDocument();
  insecure.unmount();

  Object.defineProperty(window, 'isSecureContext', { configurable: true, value: true });
  Object.defineProperty(window, 'Notification', {
    configurable: true,
    writable: true,
    value: { permission: 'denied', requestPermission: vi.fn() },
  });
  render(
    <MemoryRouter>
      <NotificationsScreen api={new ApiClient('csrf')} session={session(['notifications.read'])} notifications={resourceOf({ data: { items: [] } })} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );
  expect(screen.getByRole('switch', { name: /Browser notifications/ })).toBeDisabled();
  expect(screen.getByText(/blocked for this site/)).toBeInTheDocument();
});

it('explains accurately when native notifications fail during Send test with in-page alerts off or on', async () => {
  const user = userEvent.setup();
  class ThrowingNotification {
    constructor() { throw new TypeError('Illegal constructor'); }
    static get permission(): NotificationPermission { return 'granted'; }
    static requestPermission = vi.fn(async () => 'granted' as NotificationPermission);
  }
  Object.defineProperty(window, 'isSecureContext', { configurable: true, value: true });
  Object.defineProperty(window, 'Notification', { configurable: true, writable: true, value: ThrowingNotification });
  setNotificationPrefs({ toasts: false, browser: true });

  render(
    <MemoryRouter>
      <NotificationsScreen api={new ApiClient('csrf')} session={session(['notifications.read'])} notifications={resourceOf({ data: { items: [] } })} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );

  await user.click(screen.getByRole('button', { name: 'Send test' }));
  expect(screen.getByText(/Enable in-page alerts to see notifications in this tab/)).toBeInTheDocument();

  await user.click(screen.getByRole('switch', { name: /In-page alerts/ }));
  await user.click(screen.getByRole('button', { name: 'Send test' }));
  expect(screen.getByText(/In-page alerts will be used instead/)).toBeInTheDocument();
});
