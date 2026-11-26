import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { session } from '../test/fixtures';
import { render, screen, waitFor, within } from '../test/render';
import type { NotificationChannel, SavedObject } from '../types';
import { NotificationChannelsPanel } from './NotificationChannelsPanel';
import { NotificationsScreen } from './NotificationsScreen';

const opsChannel: SavedObject<NotificationChannel> = {
  version: '8',
  value: {
    id: 'ntf_ops',
    name: 'ops',
    description: 'Primary on-call webhook',
    enabled: true,
    kind: 'webhook',
    filter: { minSeverity: 'warning', sources: ['apply', 'upgrade'] },
    webhook: {
      url: { secret: 'OPS_URL', isSet: true, version: '8' },
      authorization: { secret: 'OPS_AUTH', isSet: true, version: '8' },
    },
  },
};

const unresolvedChannel: SavedObject<NotificationChannel> = {
  version: '5',
  value: {
    id: 'ntf_restored',
    name: 'restored',
    enabled: false,
    kind: 'webhook',
    filter: { minSeverity: 'critical' },
    webhook: {
      url: { secret: 'RESTORED_URL', isSet: false, version: '0', unresolved: true },
    },
  },
};

describe('NotificationChannelsPanel', () => {
  it('lists channels, hides write actions without notifications.manage, and creates a webhook channel', async () => {
    const user = userEvent.setup();
    const onSaved = vi.fn();
    let createdPayload: unknown;
    let createCsrf: string | null = null;
    let items: Array<SavedObject<NotificationChannel>> = [];

    fetchRoutes({
      'GET /api/v1/config/notifications/channels': () => jsonResponse(200, { state: 'saved', savedVersion: '10', items }),
      'GET /api/v1/config': () => jsonResponse(200, {
        state: 'saved',
        savedVersion: '10',
        unusedSecrets: ['NOTIFY_OPS_URL'],
        value: { links: [], segments: [], zones: [] },
      }),
      'POST /api/v1/config/notifications/channels': (init) => {
        createCsrf = new Headers(init?.headers).get('X-Andaru-CSRF');
        createdPayload = JSON.parse(String(init?.body ?? '{}'));
        items = [opsChannel];
        return jsonResponse(201, { state: 'saved', savedVersion: '11', ...opsChannel });
      },
    });

    const api = new ApiClient('csrf-tok');
    const { rerender } = render(
      <NotificationChannelsPanel api={api} canManage={false} onSaved={onSaved} onSessionLost={vi.fn()} />,
    );
    expect(await screen.findByText('No notification channels')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Add channel' })).not.toBeInTheDocument();

    rerender(<NotificationChannelsPanel api={api} canManage onSaved={onSaved} onSessionLost={vi.fn()} />);
    await user.click(screen.getByRole('button', { name: 'Add channel' }));
    const dialog = await screen.findByRole('dialog', { name: 'Add notification channel' });

    await user.type(within(dialog).getByLabelText(/^Name/, { selector: 'input' }), 'ops');
    await user.type(within(dialog).getByLabelText(/^Description/, { selector: 'input' }), 'On-call alerts');
    await user.type(within(dialog).getByLabelText(/^Webhook URL/, { selector: 'input' }), 'https://hooks.example/T0K3N');
    await user.type(within(dialog).getByLabelText(/^Authorization header/, { selector: 'input' }), 'Bearer tok');
    await user.click(within(dialog).getByRole('button', { name: 'Add channel' }));

    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(createCsrf).toBe('csrf-tok');
    expect(createdPayload).toEqual({
      name: 'ops',
      description: 'On-call alerts',
      enabled: true,
      kind: 'webhook',
      filter: { minSeverity: 'info' },
      webhook: {
        // Avoided collision with unusedSecrets['NOTIFY_OPS_URL'].
        url: { secret: 'NOTIFY_OPS_URL_2', set: 'https://hooks.example/T0K3N', expectedVersion: '0' },
        authorization: { secret: 'NOTIFY_OPS_AUTH', set: 'Bearer tok', expectedVersion: '0' },
      },
    });
    expect(await screen.findByText('Primary on-call webhook')).toBeInTheDocument();
    expect(screen.getByText('warning+')).toBeInTheDocument();
    expect(screen.getByText('apply')).toBeInTheDocument();
  });

  it('edits an existing channel, removes authorization, handles restored unresolved secrets, and deletes with If-Match', async () => {
    const user = userEvent.setup();
    const onSaved = vi.fn();
    const putCalls: Array<{ url: string; ifMatch: string | null; csrf: string | null; body: unknown }> = [];
    const deleteCalls: Array<{ url: string; ifMatch: string | null; csrf: string | null }> = [];
    let items: Array<SavedObject<NotificationChannel>> = [opsChannel, unresolvedChannel];

    fetchRoutes({
      'GET /api/v1/config/notifications/channels': () => jsonResponse(200, { state: 'saved', savedVersion: '10', items }),
      'GET /api/v1/config/notifications/channels/ntf_ops': () => jsonResponse(200, { state: 'saved', savedVersion: '10', ...opsChannel }),
      'GET /api/v1/config/notifications/channels/ntf_restored': () => jsonResponse(200, { state: 'saved', savedVersion: '10', ...unresolvedChannel }),
      'GET /api/v1/config': () => jsonResponse(200, { state: 'saved', savedVersion: '10', value: { links: [], segments: [], zones: [] } }),
      'PUT /api/v1/config/notifications/channels/ntf_ops': (init) => {
        putCalls.push({
          url: '/api/v1/config/notifications/channels/ntf_ops',
          ifMatch: new Headers(init?.headers).get('If-Match'),
          csrf: new Headers(init?.headers).get('X-Andaru-CSRF'),
          body: JSON.parse(String(init?.body ?? '{}')),
        });
        return jsonResponse(200, { state: 'saved', savedVersion: '11', version: '11', value: opsChannel.value });
      },
      'PUT /api/v1/config/notifications/channels/ntf_restored': (init) => {
        putCalls.push({
          url: '/api/v1/config/notifications/channels/ntf_restored',
          ifMatch: new Headers(init?.headers).get('If-Match'),
          csrf: new Headers(init?.headers).get('X-Andaru-CSRF'),
          body: JSON.parse(String(init?.body ?? '{}')),
        });
        return jsonResponse(200, { state: 'saved', savedVersion: '12', version: '12', value: unresolvedChannel.value });
      },
      'DELETE /api/v1/config/notifications/channels/ntf_ops': (init) => {
        deleteCalls.push({
          url: '/api/v1/config/notifications/channels/ntf_ops',
          ifMatch: new Headers(init?.headers).get('If-Match'),
          csrf: new Headers(init?.headers).get('X-Andaru-CSRF'),
        });
        items = [unresolvedChannel];
        return jsonResponse(200, { state: 'saved', savedVersion: '13' });
      },
    });

    render(<NotificationChannelsPanel api={new ApiClient('csrf')} canManage onSaved={onSaved} onSessionLost={vi.fn()} />);
    expect(await screen.findByText('Unresolved secret')).toBeInTheDocument();

    // Edit ops: keep URL unchanged, remove authorization header.
    await user.click(screen.getByRole('button', { name: 'Edit ops' }));
    await screen.findByRole('dialog', { name: 'Edit channel: ops' });
    await user.click(screen.getByLabelText('Remove authorization header'));
    expect(screen.getByLabelText(/^Authorization header/, { selector: 'input' })).toBeDisabled();
    await user.click(screen.getByRole('button', { name: 'Save channel' }));
    await waitFor(() => expect(putCalls).toHaveLength(1));
    expect(putCalls[0]).toEqual({
      url: '/api/v1/config/notifications/channels/ntf_ops',
      ifMatch: '"ntf_ops.8"',
      csrf: 'csrf',
      body: {
        name: 'ops',
        description: 'Primary on-call webhook',
        enabled: true,
        kind: 'webhook',
        filter: { minSeverity: 'warning', sources: ['apply', 'upgrade'] },
        webhook: {
          url: { secret: 'OPS_URL' },
        },
      },
    });

    // Edit restored unresolved channel without resolving URL: echoes unresolved: true.
    await user.click(screen.getByRole('button', { name: 'Edit restored' }));
    const restoredDialog = await screen.findByRole('dialog', { name: 'Edit channel: restored' });
    expect(within(restoredDialog).getByText('Webhook URL needs to be restored')).toBeInTheDocument();
    await user.click(within(restoredDialog).getByRole('switch'));
    await user.click(within(restoredDialog).getByRole('button', { name: 'Save channel' }));
    await waitFor(() => expect(putCalls).toHaveLength(2));
    expect(putCalls[1]).toEqual({
      url: '/api/v1/config/notifications/channels/ntf_restored',
      ifMatch: '"ntf_restored.5"',
      csrf: 'csrf',
      body: {
        name: 'restored',
        enabled: true,
        kind: 'webhook',
        filter: { minSeverity: 'critical' },
        webhook: {
          url: { secret: 'RESTORED_URL', unresolved: true },
        },
      },
    });

    // Delete ops via confirmation modal.
    await user.click(screen.getByRole('button', { name: 'Delete ops' }));
    await screen.findByRole('dialog', { name: 'Delete notification channel' });
    await user.click(screen.getByRole('button', { name: 'Delete channel' }));
    await waitFor(() => expect(deleteCalls).toHaveLength(1));
    expect(deleteCalls[0]).toEqual({ url: '/api/v1/config/notifications/channels/ntf_ops', ifMatch: '"ntf_ops.8"', csrf: 'csrf' });
    expect(onSaved).toHaveBeenCalledTimes(3);
  });

  it('surfaces 422 secret-kind errors with aria-invalid on PasswordInput, handles PUT 404/412 and DELETE 412/404 reloads, and signs out on 401', async () => {
    const user = userEvent.setup();
    const onSessionLost = vi.fn();
    let putMode: 'invalid' | 'missing' | 'conflict' | 'unauth' = 'invalid';
    let deleteStatus = 412;
    let listReads = 0;

    fetchRoutes({
      'GET /api/v1/config/notifications/channels': () => {
        listReads += 1;
        return jsonResponse(200, { state: 'saved', savedVersion: '10', items: [opsChannel] });
      },
      'GET /api/v1/config/notifications/channels/ntf_ops': () => jsonResponse(200, { state: 'saved', savedVersion: '10', ...opsChannel }),
      'GET /api/v1/config': () => jsonResponse(200, { state: 'saved', savedVersion: '10', value: { links: [], segments: [], zones: [] } }),
      'PUT /api/v1/config/notifications/channels/ntf_ops': () => {
        if (putMode === 'invalid') {
          return jsonResponse(422, {
            error: {
              code: 'VALIDATION_FAILED',
              message: 'Invalid channel.',
              details: [{ path: '/webhook/url', message: 'This secret belongs to another kind of object.' }],
            },
          });
        }
        if (putMode === 'missing') {
          return jsonResponse(404, { error: { code: 'NOT_FOUND', message: 'Not Found' } });
        }
        if (putMode === 'conflict') {
          return jsonResponse(412, { error: { code: 'CONFLICT', message: 'Version mismatch.' } });
        }
        return jsonResponse(401, { error: { code: 'UNAUTHENTICATED', message: 'Expired.' } });
      },
      'DELETE /api/v1/config/notifications/channels/ntf_ops': () => jsonResponse(deleteStatus, {
        error: { code: deleteStatus === 412 ? 'CONFLICT' : 'NOT_FOUND', message: deleteStatus === 412 ? 'Precondition Failed' : 'Not Found' },
      }),
    });

    render(<NotificationChannelsPanel api={new ApiClient('csrf')} canManage onSessionLost={onSessionLost} />);
    await screen.findByText('ops');
    await user.click(screen.getByRole('button', { name: 'Edit ops' }));
    const editDialog = await screen.findByRole('dialog', { name: 'Edit channel: ops' });

    let beforeReads = listReads;
    await user.click(within(editDialog).getByRole('button', { name: 'Save channel' }));
    expect(await screen.findByText('This secret belongs to another kind of object.')).toBeInTheDocument();
    await waitFor(() => expect(listReads).toBeGreaterThan(beforeReads));
    const urlInput = within(editDialog).getByLabelText(/^Webhook URL/, { selector: 'input' });
    expect(urlInput).toHaveAttribute('aria-invalid', 'true');
    expect(urlInput).toHaveAttribute('autocomplete', 'new-password');

    putMode = 'missing';
    beforeReads = listReads;
    await user.click(within(editDialog).getByRole('button', { name: 'Save channel' }));
    expect(await screen.findByText(/deleted by someone else/i)).toBeInTheDocument();
    await waitFor(() => expect(listReads).toBeGreaterThan(beforeReads));

    putMode = 'conflict';
    beforeReads = listReads;
    await user.click(within(editDialog).getByRole('button', { name: 'Save channel' }));
    expect(await screen.findByText(/changed by someone else/i)).toBeInTheDocument();
    await waitFor(() => expect(listReads).toBeGreaterThan(beforeReads));

    // Close drawer and verify DELETE 412 and 404 friendly messages + reload before simulating 401 session loss.
    await user.click(within(editDialog).getByRole('button', { name: 'Cancel' }));
    await user.click(screen.getByRole('button', { name: 'Delete ops' }));
    beforeReads = listReads;
    await user.click(screen.getByRole('button', { name: 'Delete channel' }));
    expect(await screen.findByText(/changed elsewhere while the confirmation was open/i)).toBeInTheDocument();
    await waitFor(() => expect(listReads).toBeGreaterThan(beforeReads));

    deleteStatus = 404;
    await user.click(screen.getByRole('button', { name: 'Delete ops' }));
    beforeReads = listReads;
    await user.click(screen.getByRole('button', { name: 'Delete channel' }));
    expect(await screen.findByText(/already deleted elsewhere/i)).toBeInTheDocument();
    await waitFor(() => expect(listReads).toBeGreaterThan(beforeReads));

    putMode = 'unauth';
    await user.click(screen.getByRole('button', { name: 'Edit ops' }));
    const unauthDialog = await screen.findByRole('dialog', { name: 'Edit channel: ops' });
    await user.click(within(unauthDialog).getByRole('button', { name: 'Save channel' }));
    await waitFor(() => expect(onSessionLost).toHaveBeenCalledTimes(1));
  });

  it('shows the Channels tab on NotificationsScreen with notifications.read + configuration.read, and Channels-only with configuration.read alone', async () => {
    const user = userEvent.setup();
    fetchRoutes({
      'GET /api/v1/config/notifications/channels': () => jsonResponse(200, { state: 'saved', savedVersion: '10', items: [opsChannel] }),
    });

    const bothView = render(
      <MemoryRouter>
        <NotificationsScreen
          api={new ApiClient('csrf')}
          session={session(['notifications.read', 'configuration.read', 'notifications.manage'])}
          notifications={{ data: { items: [] }, error: '', loading: false, reload: vi.fn(async () => undefined), refresh: vi.fn() }}
          onSessionLost={vi.fn()}
        />
      </MemoryRouter>,
    );

    expect(screen.getByRole('tab', { name: 'Active' })).toBeInTheDocument();
    await user.click(screen.getByRole('tab', { name: 'Channels' }));
    expect(await screen.findByText('Primary on-call webhook')).toBeInTheDocument();
    bothView.unmount();

    // configuration.read alone renders Channels directly without Active tab or edit actions.
    render(
      <MemoryRouter>
        <NotificationsScreen
          api={new ApiClient('csrf')}
          session={session(['configuration.read'])}
          notifications={{ data: undefined, error: '', loading: false, reload: vi.fn(async () => undefined), refresh: vi.fn() }}
          onSessionLost={vi.fn()}
        />
      </MemoryRouter>,
    );
    expect(screen.queryByRole('tab', { name: 'Active' })).not.toBeInTheDocument();
    expect(await screen.findByText('Primary on-call webhook')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Add channel' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Send test to ops' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Edit ops' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Delete ops' })).not.toBeInTheDocument();
  });

  it('discloses shared URL secrets and saves a separate secret so sibling channels are not overwritten', async () => {
    const user = userEvent.setup();
    const sibling: SavedObject<NotificationChannel> = {
      version: '9',
      value: {
        id: 'ntf_sibling',
        name: 'pager',
        enabled: true,
        kind: 'webhook',
        webhook: { url: { secret: 'OPS_URL', isSet: true, version: '8' } },
      },
    };
    let savedBody: unknown;
    fetchRoutes({
      'GET /api/v1/config/notifications/channels': () => jsonResponse(200, { state: 'saved', savedVersion: '10', items: [opsChannel, sibling] }),
      'GET /api/v1/config/notifications/channels/ntf_ops': () => jsonResponse(200, { state: 'saved', savedVersion: '10', ...opsChannel }),
      'GET /api/v1/config': () => jsonResponse(200, { state: 'saved', savedVersion: '10', value: { links: [], segments: [], zones: [] } }),
      'PUT /api/v1/config/notifications/channels/ntf_ops': (init) => {
        savedBody = JSON.parse(String(init?.body ?? '{}'));
        return jsonResponse(200, { state: 'saved', savedVersion: '11', version: '11', value: opsChannel.value });
      },
    });

    render(<NotificationChannelsPanel api={new ApiClient('csrf')} canManage onSessionLost={vi.fn()} />);
    await screen.findByText('pager');
    await user.click(screen.getByRole('button', { name: 'Edit ops' }));
    const dialog = await screen.findByRole('dialog', { name: 'Edit channel: ops' });
    expect(within(dialog).getByText(/shared with pager/i)).toBeInTheDocument();

    await user.type(within(dialog).getByLabelText(/^Webhook URL/, { selector: 'input' }), 'https://hooks.example/ops-only');
    await user.click(within(dialog).getByRole('button', { name: 'Save channel' }));
    await waitFor(() => expect(savedBody).toBeDefined());
    expect(savedBody).toMatchObject({
      webhook: {
        url: { secret: 'NOTIFY_OPS_URL', set: 'https://hooks.example/ops-only', expectedVersion: '0' },
        authorization: { secret: 'OPS_AUTH' },
      },
    });
  });

  it('queues a test notification (including on disabled channels), locks actions while in flight, and handles 409/404/503/401', async () => {
    const user = userEvent.setup();
    const onSessionLost = vi.fn();
    let listReads = 0;
    let testMode: 'unresolved' | 'missing' | 'unavailable' | 'unauth' = 'unresolved';
    const testCalls: Array<{ url: string; csrf: string | null; body: BodyInit | null | undefined }> = [];
    let resolveDisabledTest!: (res: Response) => void;

    const disabledChannel: SavedObject<NotificationChannel> = {
      version: '9',
      value: {
        ...opsChannel.value,
        id: 'ntf_disabled',
        name: 'backup-hook',
        enabled: false,
      },
    };

    fetchRoutes({
      'GET /api/v1/config/notifications/channels': () => {
        listReads += 1;
        return jsonResponse(200, { state: 'saved', savedVersion: '10', items: [opsChannel, disabledChannel] });
      },
      'POST /api/v1/notifications/channels/ntf_disabled/test': (init) => {
        testCalls.push({ url: '/api/v1/notifications/channels/ntf_disabled/test', csrf: new Headers(init?.headers).get('X-Andaru-CSRF'), body: init?.body });
        return new Promise<Response>((resolve) => { resolveDisabledTest = resolve; });
      },
      'POST /api/v1/notifications/channels/ntf_ops/test': (init) => {
        testCalls.push({ url: '/api/v1/notifications/channels/ntf_ops/test', csrf: new Headers(init?.headers).get('X-Andaru-CSRF'), body: init?.body });
        if (testMode === 'unresolved') {
          return jsonResponse(409, { error: { code: 'CHANNEL_SECRETS_UNRESOLVED', message: "The channel's secrets have no value, as after a restore without secrets; set them, then send a test." } });
        }
        if (testMode === 'missing') {
          return jsonResponse(404, { error: { code: 'NOT_FOUND', message: 'Resource not found.' } });
        }
        if (testMode === 'unavailable') {
          return jsonResponse(503, { error: { code: 'NOTIFICATIONS_UNAVAILABLE', message: 'Notifications are temporarily unavailable.' } });
        }
        return jsonResponse(401, { error: { code: 'UNAUTHENTICATED', message: 'Expired.' } });
      },
    });

    const view = render(<NotificationChannelsPanel api={new ApiClient('csrf-tok')} canManage onSessionLost={onSessionLost} />);
    await screen.findByText('backup-hook');

    // Works on a disabled channel with no body and CSRF header; locks sibling/Add/Edit/Delete controls while in flight.
    await user.click(screen.getByRole('button', { name: 'Send test to backup-hook' }));
    expect(testCalls).toHaveLength(1);
    expect(screen.getByRole('button', { name: 'Add channel' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Send test to ops' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Edit backup-hook' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Delete backup-hook' })).toBeDisabled();

    resolveDisabledTest(jsonResponse(202, { job: 'test:abc-123:ntf_disabled' }));
    expect(await screen.findByText(/Queued test notification for backup-hook \(test:abc-123:ntf_disabled\)/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Dismiss test notification notice' })).toBeInTheDocument();
    expect(testCalls[0]).toEqual({ url: '/api/v1/notifications/channels/ntf_disabled/test', csrf: 'csrf-tok', body: undefined });

    // 409 CHANNEL_SECRETS_UNRESOLVED shows explanation and reloads list.
    testMode = 'unresolved';
    let beforeReads = listReads;
    await user.click(screen.getByRole('button', { name: 'Send test to ops' }));
    expect(await screen.findByText(/secrets have no value/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Dismiss error' })).toBeInTheDocument();
    await waitFor(() => expect(listReads).toBeGreaterThan(beforeReads));

    // 404 shows friendly deleted message and reloads list.
    testMode = 'missing';
    beforeReads = listReads;
    await user.click(screen.getByRole('button', { name: 'Send test to ops' }));
    expect(await screen.findByText(/deleted elsewhere/i)).toBeInTheDocument();
    await waitFor(() => expect(listReads).toBeGreaterThan(beforeReads));

    // 503 surfaces unavailable message.
    testMode = 'unavailable';
    await user.click(screen.getByRole('button', { name: 'Send test to ops' }));
    expect(await screen.findByText('Notifications are temporarily unavailable.')).toBeInTheDocument();

    // Unmounting mid-flight suppresses post-unmount reload side effects.
    beforeReads = listReads;
    await user.click(screen.getByRole('button', { name: 'Send test to backup-hook' }));
    view.unmount();
    resolveDisabledTest(jsonResponse(404, { error: { code: 'NOT_FOUND', message: 'Resource not found.' } }));
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(listReads).toBe(beforeReads);

    // 401 signs out.
    testMode = 'unauth';
    render(<NotificationChannelsPanel api={new ApiClient('csrf-tok')} canManage onSessionLost={onSessionLost} />);
    await screen.findByText('ops');
    await user.click(screen.getByRole('button', { name: 'Send test to ops' }));
    await waitFor(() => expect(onSessionLost).toHaveBeenCalledTimes(1));
  });

  it('creates ntfy and Telegram channels and renders reverse-proxy targets', async () => {
    const user = userEvent.setup();
    const postPayloads: unknown[] = [];

    const ntfyChannel: SavedObject<NotificationChannel> = {
      version: '2',
      value: {
        id: 'ntf_ntfy',
        name: 'ops-ntfy',
        enabled: true,
        kind: 'ntfy',
        filter: { minSeverity: 'info' },
        ntfy: { server: 'https://ntfy.example/proxy/', topic: 'andaru-alerts' },
      },
    };

    fetchRoutes({
      'GET /api/v1/config/notifications/channels': () => jsonResponse(200, { state: 'saved', savedVersion: '10', items: [ntfyChannel] }),
      'GET /api/v1/config': () => jsonResponse(200, { state: 'saved', savedVersion: '10', value: { links: [], segments: [], zones: [] } }),
      'POST /api/v1/config/notifications/channels': (init) => {
        const parsed = JSON.parse(String(init?.body ?? '{}'));
        postPayloads.push(parsed);
        return jsonResponse(201, { state: 'saved', savedVersion: '11', version: '11', value: { id: `ntf_${postPayloads.length}`, ...parsed } });
      },
    });

    render(<NotificationChannelsPanel api={new ApiClient('csrf')} canManage onSessionLost={vi.fn()} />);
    expect(await screen.findByText('https://ntfy.example/proxy/andaru-alerts')).toBeInTheDocument();
    expect(screen.getByText('None')).toBeInTheDocument();

    // Create an ntfy channel with a reverse-proxy path and optional token.
    await user.click(screen.getByRole('button', { name: 'Add channel' }));
    let dialog = await screen.findByRole('dialog', { name: 'Add notification channel' });
    await user.click(within(dialog).getByLabelText(/^Channel kind/, { selector: 'input' }));
    await user.click(await screen.findByRole('option', { name: 'ntfy' }));
    await user.type(within(dialog).getByLabelText(/^Name/, { selector: 'input' }), 'ops-ntfy-2');
    const serverInput = within(dialog).getByLabelText(/^Server URL/, { selector: 'input' });
    await user.clear(serverInput);
    await user.type(serverInput, 'https://ntfy.example/subpath');
    await user.type(within(dialog).getByLabelText(/^Topic/, { selector: 'input' }), 'alerts_1');
    await user.type(within(dialog).getByLabelText(/^Access token/, { selector: 'input' }), 'tk_secret');
    await user.click(within(dialog).getByRole('button', { name: 'Add channel' }));
    await waitFor(() => expect(postPayloads).toHaveLength(1));
    expect(postPayloads[0]).toEqual({
      name: 'ops-ntfy-2',
      enabled: true,
      kind: 'ntfy',
      filter: { minSeverity: 'info' },
      ntfy: {
        server: 'https://ntfy.example/subpath',
        topic: 'alerts_1',
        token: { secret: 'NOTIFY_OPS_NTFY_2_TOKEN', set: 'tk_secret', expectedVersion: '0' },
      },
    });

    // Create a Telegram channel via the kind selector.
    await user.click(screen.getByRole('button', { name: 'Add channel' }));
    dialog = await screen.findByRole('dialog', { name: 'Add notification channel' });
    await user.click(within(dialog).getByLabelText(/^Channel kind/, { selector: 'input' }));
    await user.click(await screen.findByRole('option', { name: 'Telegram' }));
    await user.type(within(dialog).getByLabelText(/^Name/, { selector: 'input' }), 'ops-tg');
    await user.type(within(dialog).getByLabelText(/^Bot token/, { selector: 'input' }), '123456:BOT_SECRET');
    await user.type(within(dialog).getByLabelText(/^Chat ID/, { selector: 'input' }), '-1001234567890');
    await user.click(within(dialog).getByRole('button', { name: 'Add channel' }));
    await waitFor(() => expect(postPayloads).toHaveLength(2));
    expect(postPayloads[1]).toEqual({
      name: 'ops-tg',
      enabled: true,
      kind: 'telegram',
      filter: { minSeverity: 'info' },
      telegram: {
        token: { secret: 'NOTIFY_OPS_TG_TOKEN', set: '123456:BOT_SECRET', expectedVersion: '0' },
        chatId: '-1001234567890',
      },
    });
  });

  it('creates a Pushover channel and edits an unknown future kind read-only without corrupting its sub-object', async () => {
    const user = userEvent.setup();
    let postPayload: unknown;
    let putPayload: unknown;

    const futureEmailChannel: SavedObject<NotificationChannel> = {
      version: '7',
      value: {
        id: 'ntf_email',
        name: 'ops-email',
        enabled: true,
        kind: 'email',
        filter: { minSeverity: 'critical' },
        email: {
          host: 'smtp.example',
          from: 'andaru@example.com',
          password: { secret: 'SMTP_PASS', isSet: true, version: '7' },
        },
      },
    };

    fetchRoutes({
      'GET /api/v1/config/notifications/channels': () => jsonResponse(200, { state: 'saved', savedVersion: '10', items: [futureEmailChannel] }),
      'GET /api/v1/config/notifications/channels/ntf_email': () => jsonResponse(200, { state: 'saved', savedVersion: '10', ...futureEmailChannel }),
      'GET /api/v1/config': () => jsonResponse(200, { state: 'saved', savedVersion: '10', value: { links: [], segments: [], zones: [] } }),
      'POST /api/v1/config/notifications/channels': (init) => {
        postPayload = JSON.parse(String(init?.body ?? '{}'));
        return jsonResponse(201, { state: 'saved', savedVersion: '11', version: '11', value: { id: 'ntf_po', ...(postPayload as object) } });
      },
      'PUT /api/v1/config/notifications/channels/ntf_email': (init) => {
        putPayload = JSON.parse(String(init?.body ?? '{}'));
        return jsonResponse(200, { state: 'saved', savedVersion: '12', version: '12', value: futureEmailChannel.value });
      },
    });

    render(<NotificationChannelsPanel api={new ApiClient('csrf')} canManage onSessionLost={vi.fn()} />);
    expect(await screen.findByText('email')).toBeInTheDocument();
    expect(screen.getByText(/password:\s*set/i)).toBeInTheDocument();

    // Create a Pushover channel.
    await user.click(screen.getByRole('button', { name: 'Add channel' }));
    let dialog = await screen.findByRole('dialog', { name: 'Add notification channel' });
    await user.click(within(dialog).getByLabelText(/^Channel kind/, { selector: 'input' }));
    await user.click(await screen.findByRole('option', { name: 'Pushover' }));
    await user.type(within(dialog).getByLabelText(/^Name/, { selector: 'input' }), 'ops-po');
    await user.type(within(dialog).getByLabelText(/^Application token/, { selector: 'input' }), 'app-secret');
    await user.type(within(dialog).getByLabelText(/^User key/, { selector: 'input' }), 'user-secret');
    await user.click(within(dialog).getByRole('button', { name: 'Add channel' }));
    await waitFor(() => expect(postPayload).toBeDefined());
    expect(postPayload).toEqual({
      name: 'ops-po',
      enabled: true,
      kind: 'pushover',
      filter: { minSeverity: 'info' },
      pushover: {
        token: { secret: 'NOTIFY_OPS_PO_TOKEN', set: 'app-secret', expectedVersion: '0' },
        user: { secret: 'NOTIFY_OPS_PO_USER', set: 'user-secret', expectedVersion: '0' },
      },
    });

    // Edit unknown future kind (email): kind selector disabled, read-only alert shown, disable echoes email sub-object intact.
    await user.click(screen.getByRole('button', { name: 'Edit ops-email' }));
    dialog = await screen.findByRole('dialog', { name: 'Edit channel: ops-email' });
    expect(within(dialog).getByText('Delivery settings are read-only')).toBeInTheDocument();
    expect(within(dialog).getByLabelText(/^Channel kind/, { selector: 'input' })).toBeDisabled();
    await user.click(within(dialog).getByRole('switch'));
    await user.click(within(dialog).getByRole('button', { name: 'Save channel' }));
    await waitFor(() => expect(putPayload).toBeDefined());
    expect(putPayload).toEqual({
      name: 'ops-email',
      enabled: false,
      kind: 'email',
      filter: { minSeverity: 'critical' },
      email: {
        host: 'smtp.example',
        from: 'andaru@example.com',
        password: { secret: 'SMTP_PASS' },
      },
    });
  });

  it('handles ntfy token removal, Pushover shared/unresolved secrets with 422 aria-invalid, and switching kind on edit without focus steal', async () => {
    const user = userEvent.setup();
    const putPayloads: Record<string, unknown> = {};
    let pushoverRejectOnce = true;
    let configReads = 0;

    const ntfyWithToken: SavedObject<NotificationChannel> = {
      version: '4',
      value: {
        id: 'ntf_n1',
        name: 'ntfy-auth',
        enabled: true,
        kind: 'ntfy',
        filter: { minSeverity: 'info' },
        ntfy: { server: 'https://ntfy.sh', topic: 'ops', token: { secret: 'SHARED_TOK', isSet: true, version: '4' } },
      },
    };
    const pushoverChannel: SavedObject<NotificationChannel> = {
      version: '6',
      value: {
        id: 'ntf_po1',
        name: 'po-oncall',
        enabled: true,
        kind: 'pushover',
        filter: { minSeverity: 'warning' },
        pushover: {
          token: { secret: 'SHARED_TOK', isSet: true, version: '4' },
          user: { secret: 'PO_USER', isSet: false, version: '0', unresolved: true },
        },
      },
    };

    fetchRoutes({
      'GET /api/v1/config/notifications/channels': () => jsonResponse(200, { state: 'saved', savedVersion: '10', items: [ntfyWithToken, pushoverChannel] }),
      'GET /api/v1/config/notifications/channels/ntf_n1': () => jsonResponse(200, { state: 'saved', savedVersion: '10', ...ntfyWithToken }),
      'GET /api/v1/config/notifications/channels/ntf_po1': () => jsonResponse(200, { state: 'saved', savedVersion: '10', ...pushoverChannel }),
      'GET /api/v1/config': () => {
        configReads += 1;
        return jsonResponse(200, {
          state: 'saved',
          savedVersion: '10',
          // Simulate a concurrent secret added before the retry after 422.
          unusedSecrets: configReads >= 3 ? ['NOTIFY_PO_ONCALL_TOKEN'] : [],
          value: { links: [], segments: [], zones: [] },
        });
      },
      'PUT /api/v1/config/notifications/channels/ntf_n1': (init) => {
        putPayloads.ntf_n1 = JSON.parse(String(init?.body ?? '{}'));
        return jsonResponse(200, { state: 'saved', savedVersion: '11', version: '11', value: ntfyWithToken.value });
      },
      'PUT /api/v1/config/notifications/channels/ntf_po1': (init) => {
        if (pushoverRejectOnce) {
          pushoverRejectOnce = false;
          return jsonResponse(422, {
            error: {
              code: 'VALIDATION_FAILED',
              message: 'Invalid.',
              details: [{ path: '/pushover/token', message: 'Token rejected by server.' }],
            },
          });
        }
        putPayloads.ntf_po1 = JSON.parse(String(init?.body ?? '{}'));
        return jsonResponse(200, { state: 'saved', savedVersion: '12', version: '12', value: pushoverChannel.value });
      },
    });

    render(<NotificationChannelsPanel api={new ApiClient('csrf')} canManage onSessionLost={vi.fn()} />);
    expect(await screen.findByText(/App token:\s*set/i)).toBeInTheDocument();
    expect(screen.getByText(/User key:/i)).toHaveTextContent(/User key:\s*unresolved/i);

    // Edit ntfy channel and remove optional access token.
    await user.click(screen.getByRole('button', { name: 'Edit ntfy-auth' }));
    let dialog = await screen.findByRole('dialog', { name: 'Edit channel: ntfy-auth' });
    await user.click(within(dialog).getByLabelText('Remove access token'));
    expect(within(dialog).getByLabelText(/^Access token/, { selector: 'input' })).toBeDisabled();
    await user.click(within(dialog).getByRole('button', { name: 'Save channel' }));
    await waitFor(() => expect(putPayloads.ntf_n1).toBeDefined());
    expect(putPayloads.ntf_n1).toEqual({
      name: 'ntfy-auth',
      enabled: true,
      kind: 'ntfy',
      filter: { minSeverity: 'info' },
      ntfy: { server: 'https://ntfy.sh', topic: 'ops' },
    });

    // Edit Pushover channel: shows shared secret disclosure + unresolved user key alert, 422 sets aria-invalid,
    // and switching kind to Telegram clears stale kind error/banner and writes only the telegram sub-object.
    await user.click(screen.getByRole('button', { name: 'Edit po-oncall' }));
    dialog = await screen.findByRole('dialog', { name: 'Edit channel: po-oncall' });
    expect(within(dialog).getByText(/shared with ntfy-auth/i)).toBeInTheDocument();
    expect(within(dialog).getByText('User key needs to be restored')).toBeInTheDocument();

    await user.click(within(dialog).getByRole('button', { name: 'Save channel' }));
    expect(await within(dialog).findByText('Token rejected by server.')).toBeInTheDocument();
    const appTokenInput = within(dialog).getByLabelText(/^Application token/, { selector: 'input' });
    expect(appTokenInput).toHaveAttribute('aria-invalid', 'true');
    expect(appTokenInput).toHaveAttribute('autocomplete', 'new-password');

    // Typing into the last invalid field clears its error without stealing focus to the failure alert.
    await user.type(appTokenInput, 'x');
    expect(appTokenInput).toHaveFocus();
    expect(within(dialog).getByText('Notification channel was not saved')).toBeInTheDocument();

    // Switching kind to Telegram clears the stale field-error banner even after field errors were edited away,
    // and uses the refreshed existingSecrets from the post-failure refresh on retry.
    await user.click(within(dialog).getByLabelText(/^Channel kind/, { selector: 'input' }));
    await user.click(await screen.findByRole('option', { name: 'Telegram' }));
    expect(within(dialog).queryByText('Notification channel was not saved')).not.toBeInTheDocument();
    await user.type(within(dialog).getByLabelText(/^Bot token/, { selector: 'input' }), '999:NEW_BOT');
    await user.type(within(dialog).getByLabelText(/^Chat ID/, { selector: 'input' }), '@oncall_team');
    await user.click(within(dialog).getByRole('button', { name: 'Save channel' }));
    await waitFor(() => expect(putPayloads.ntf_po1).toBeDefined());
    expect(putPayloads.ntf_po1).toEqual({
      name: 'po-oncall',
      enabled: true,
      kind: 'telegram',
      filter: { minSeverity: 'warning' },
      telegram: {
        token: { secret: 'NOTIFY_PO_ONCALL_TOKEN_2', set: '999:NEW_BOT', expectedVersion: '0' },
        chatId: '@oncall_team',
      },
    });
  });
});
