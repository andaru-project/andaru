import { fireEvent } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { render, screen, waitFor, within } from '../test/render';
import type { Session } from '../types';
import { RoutesScreen } from './RoutesScreen';

afterEach(() => {
  vi.restoreAllMocks();
});

const adminSession: Session = {
  account: { id: 'usr_1', login: 'admin', displayName: 'Administrator' },
  capabilities: ['configuration.read', 'configuration.write'],
  csrfToken: 'csrf',
  expiresAt: '2026-11-29T00:00:00Z',
  idleTimeoutSeconds: 900,
};

const readOnlySession: Session = {
  ...adminSession,
  capabilities: ['configuration.read'],
};

const sampleSegment = {
  version: '1',
  value: {
    id: 'seg_lan',
    name: 'LAN',
    link: 'lnk_lan',
    zone: 'zon_lan',
    addressing: { ipv4: { mode: 'static', addresses: ['192.168.1.1/24'] }, ipv6: { mode: 'none' } },
  },
};

const sampleRoute = {
  version: '3',
  value: {
    id: 'rte_branch',
    name: 'Branch Office',
    description: 'Remote site prefix',
    destination: '10.20.0.0/24',
    target: { kind: 'segment', id: 'seg_lan' },
    gateway: '192.168.1.254',
    metric: 200,
    enabled: true,
  },
};

describe('RoutesScreen', () => {
  it('lists static routes, creates and edits a route (including clearing optional fields), and deletes with If-Match', async () => {
    const user = userEvent.setup();
    const onSaved = vi.fn();
    let createdBody: unknown;
    let updatedBody: unknown;
    let updatedIfMatch: string | null = null;
    let deletedIfMatch: string | null = null;

    fetchRoutes({
      '/api/v1/config/routes': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleRoute] }),
      '/api/v1/config/segments': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleSegment] }),
      '/api/v1/config/vpn/wireguard': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [] }),
      '/api/v1/config/vpn/ipsec': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [] }),
      'POST /api/v1/config/routes': (init) => {
        createdBody = JSON.parse(String(init?.body ?? '{}'));
        return jsonResponse(201, { state: 'saved', savedVersion: '6', ...sampleRoute });
      },
      'PUT /api/v1/config/routes/rte_branch': (init) => {
        updatedBody = JSON.parse(String(init?.body ?? '{}'));
        updatedIfMatch = new Headers(init?.headers).get('If-Match');
        return jsonResponse(200, { state: 'saved', savedVersion: '7', ...sampleRoute });
      },
      'DELETE /api/v1/config/routes/rte_branch': (init) => {
        deletedIfMatch = new Headers(init?.headers).get('If-Match');
        return jsonResponse(200, { state: 'saved', savedVersion: '8' });
      },
    });

    render(
      <RoutesScreen
        api={new ApiClient('csrf')}
        session={adminSession}
        onSaved={onSaved}
        onSessionLost={vi.fn()}
      />,
    );

    expect(await screen.findByText('Branch Office')).toBeInTheDocument();
    expect(screen.getByText('10.20.0.0/24')).toBeInTheDocument();
    expect(screen.getByText('LAN')).toBeInTheDocument();
    expect(screen.getByText('192.168.1.254')).toBeInTheDocument();
    expect(screen.getByText('200')).toBeInTheDocument();

    // Create a new IPv6 direct route.
    await user.click(screen.getByRole('button', { name: 'Add route' }));
    const addDialog = await screen.findByRole('dialog', { name: 'Add static route' });
    fireEvent.change(within(addDialog).getByLabelText(/^Name/), { target: { value: 'Lab IPv6' } });
    fireEvent.change(within(addDialog).getByLabelText(/^Destination CIDR/), { target: { value: '2001:db8:9::/48' } });
    fireEvent.change(within(addDialog).getByLabelText(/^Metric/), { target: { value: '50' } });
    await user.click(within(addDialog).getByRole('button', { name: 'Add route' }));
    await waitFor(() => expect(createdBody).toEqual({
      name: 'Lab IPv6',
      destination: '2001:db8:9::/48',
      target: { kind: 'segment', id: 'seg_lan' },
      metric: 50,
      enabled: true,
    }));

    // Edit existing route with If-Match and clear optional gateway/metric/description.
    await user.click(screen.getByRole('button', { name: 'Edit route Branch Office' }));
    const editDialog = await screen.findByRole('dialog', { name: 'Edit static route' });
    fireEvent.change(within(editDialog).getByLabelText(/^Description/), { target: { value: '' } });
    fireEvent.change(within(editDialog).getByLabelText(/^Gateway/), { target: { value: '' } });
    fireEvent.change(within(editDialog).getByLabelText(/^Metric/), { target: { value: '' } });
    await user.click(within(editDialog).getByRole('button', { name: 'Save route' }));
    await waitFor(() => expect(updatedIfMatch).toBe('"rte_branch.3"'));
    expect(updatedBody).toEqual({
      name: 'Branch Office',
      destination: '10.20.0.0/24',
      target: { kind: 'segment', id: 'seg_lan' },
      enabled: true,
    });

    // Delete with If-Match.
    await user.click(screen.getByRole('button', { name: 'Delete route Branch Office' }));
    const delDialog = await screen.findByRole('dialog', { name: 'Delete static route' });
    await user.click(within(delDialog).getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(deletedIfMatch).toBe('"rte_branch.3"'));
    expect(onSaved).toHaveBeenCalledTimes(3);
  });

  it('validates client inputs, surfaces server 422/412 errors, triggers onSessionLost on 401, and hides write controls for read-only users', async () => {
    const user = userEvent.setup();
    const onSessionLost = vi.fn();
    let routeFetches = 0;
    let putStatus = 412;

    fetchRoutes({
      '/api/v1/config/routes': () => {
        routeFetches += 1;
        return jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleRoute] });
      },
      '/api/v1/config/segments': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleSegment] }),
      '/api/v1/config/vpn/wireguard': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [] }),
      '/api/v1/config/vpn/ipsec': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [] }),
      'POST /api/v1/config/routes': () => jsonResponse(422, {
        error: {
          code: 'VALIDATION_FAILED',
          message: 'Validation failed.',
          details: [{ path: '/gateway', message: "Expected a gateway of the destination's address family." }],
        },
      }),
      'PUT /api/v1/config/routes/rte_branch': () => {
        if (putStatus === 401) {
          return jsonResponse(401, { error: { code: 'UNAUTHORIZED', message: 'Session expired.', details: [] } });
        }
        return jsonResponse(412, { error: { code: 'PRECONDITION_FAILED', message: 'Modified elsewhere.', details: [] } });
      },
    });

    const { rerender } = render(
      <RoutesScreen
        api={new ApiClient('csrf')}
        session={adminSession}
        onSaved={vi.fn()}
        onSessionLost={onSessionLost}
      />,
    );

    expect(await screen.findByText('Branch Office')).toBeInTheDocument();

    // Client-side validation catches empty name and missing CIDR slash.
    await user.click(screen.getByRole('button', { name: 'Add route' }));
    const dialog = await screen.findByRole('dialog', { name: 'Add static route' });
    fireEvent.change(within(dialog).getByLabelText(/^Destination CIDR/), { target: { value: '10.30.0.0' } });
    await user.click(within(dialog).getByRole('button', { name: 'Add route' }));
    expect(await within(dialog).findByText('Enter a route name.')).toBeInTheDocument();
    expect(within(dialog).getByText('Enter an IPv4 or IPv6 destination in CIDR notation.')).toBeInTheDocument();

    // Server-side 422 maps to /gateway.
    fireEvent.change(within(dialog).getByLabelText(/^Name/), { target: { value: 'Family Mismatch' } });
    fireEvent.change(within(dialog).getByLabelText(/^Destination CIDR/), { target: { value: '10.30.0.0/24' } });
    fireEvent.change(within(dialog).getByLabelText(/^Gateway/), { target: { value: 'fe80::1' } });
    await user.click(within(dialog).getByRole('button', { name: 'Add route' }));
    expect(await within(dialog).findByText("Expected a gateway of the destination's address family.")).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));

    // 412 conflict reloads routes, and 401 calls onSessionLost.
    const fetchesBefore = routeFetches;
    await user.click(screen.getByRole('button', { name: 'Edit route Branch Office' }));
    const editDialog = await screen.findByRole('dialog', { name: 'Edit static route' });
    await user.click(within(editDialog).getByRole('button', { name: 'Save route' }));
    await waitFor(() => expect(routeFetches).toBeGreaterThan(fetchesBefore));

    putStatus = 401;
    await user.click(within(editDialog).getByRole('button', { name: 'Save route' }));
    await waitFor(() => expect(onSessionLost).toHaveBeenCalledTimes(1));
    await user.click(within(editDialog).getByRole('button', { name: 'Cancel' }));

    rerender(
      <RoutesScreen
        api={new ApiClient('csrf')}
        session={readOnlySession}
        onSaved={vi.fn()}
        onSessionLost={onSessionLost}
      />,
    );
    expect(screen.queryByRole('button', { name: 'Add route' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Edit route Branch Office' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Delete route Branch Office' })).not.toBeInTheDocument();
  });

  it('recovers from an initial 503 read failure via Refresh', async () => {
    const user = userEvent.setup();
    let failReads = true;

    fetchRoutes({
      '/api/v1/config/routes': () => {
        if (failReads) {
          return jsonResponse(503, { error: { code: 'UNAVAILABLE', message: 'Temporarily unavailable.', details: [] } });
        }
        return jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleRoute] });
      },
      '/api/v1/config/segments': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleSegment] }),
      '/api/v1/config/vpn/wireguard': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [] }),
      '/api/v1/config/vpn/ipsec': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [] }),
    });

    render(
      <RoutesScreen
        api={new ApiClient('csrf')}
        session={adminSession}
        onSaved={vi.fn()}
        onSessionLost={vi.fn()}
      />,
    );

    expect(await screen.findByRole('alert')).toHaveTextContent('Temporarily unavailable.');
    expect(screen.getByRole('button', { name: 'Add route' })).toBeDisabled();

    failReads = false;
    await user.click(screen.getByRole('button', { name: 'Refresh' }));
    expect(await screen.findByText('Branch Office')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Add route' })).toBeEnabled();
  });

  it('supports route-based IPsec XFRM targets and hides the gateway input', async () => {
    const user = userEvent.setup();
    let createdBody: unknown;

    fetchRoutes({
      '/api/v1/config/routes': () => jsonResponse(200, {
        state: 'saved',
        savedVersion: '5',
        items: [{
          version: '1',
          value: {
            id: 'rte_ipsec',
            name: 'HQ Tunnel',
            destination: '10.88.0.0/16',
            target: { kind: 'ipsec', id: 'ips_hq' },
            enabled: true,
          },
        }],
      }),
      '/api/v1/config/segments': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [] }),
      '/api/v1/config/vpn/wireguard': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [] }),
      '/api/v1/config/vpn/ipsec': () => jsonResponse(200, {
        state: 'saved',
        savedVersion: '5',
        items: [
          {
            version: '1',
            value: {
              id: 'ips_hq',
              name: 'HQ XFRM',
              enabled: true,
              mode: 'route-based',
              ifname: 'ipsec0',
              xfrmIfId: 1,
              zone: 'zon_vpn',
              remoteAddress: '198.51.100.1',
              auth: { localType: 'psk', remoteType: 'psk', localId: 'gw.example.com' },
            },
          },
          {
            version: '1',
            value: {
              id: 'ips_policy',
              name: 'Policy Only',
              enabled: true,
              mode: 'policy-based',
              zone: 'zon_vpn',
              remoteAddress: '198.51.100.2',
              auth: { localType: 'psk', remoteType: 'psk', localId: 'gw.example.com' },
            },
          },
        ],
      }),
      'POST /api/v1/config/routes': (init) => {
        createdBody = JSON.parse(String(init?.body ?? '{}'));
        return jsonResponse(201, {
          state: 'saved',
          savedVersion: '6',
          version: '1',
          value: { id: 'rte_new', ...(createdBody as Record<string, unknown>) },
        });
      },
    });

    render(
      <RoutesScreen
        api={new ApiClient('csrf')}
        session={adminSession}
        onSaved={vi.fn()}
        onSessionLost={vi.fn()}
      />,
    );

    expect(await screen.findByText('HQ XFRM (ipsec0)')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Add route' }));
    const dialog = await screen.findByRole('dialog', { name: 'Add static route' });
    expect(within(dialog).queryByLabelText(/^Gateway/)).not.toBeInTheDocument();
    fireEvent.change(within(dialog).getByLabelText(/^Name/), { target: { value: 'Remote Branch' } });
    fireEvent.change(within(dialog).getByLabelText(/^Destination CIDR/), { target: { value: '10.99.0.0/24' } });
    await user.click(within(dialog).getByRole('button', { name: 'Add route' }));

    await waitFor(() => expect(createdBody).toEqual({
      name: 'Remote Branch',
      destination: '10.99.0.0/24',
      target: { kind: 'ipsec', id: 'ips_hq' },
      enabled: true,
    }));
  });
});
