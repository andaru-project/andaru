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
});
