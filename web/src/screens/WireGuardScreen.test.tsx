import { fireEvent } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { render, screen, waitFor, within } from '../test/render';
import type { Session, WireGuardInterface } from '../types';
import { WireGuardScreen } from './WireGuardScreen';

afterEach(() => {
  vi.restoreAllMocks();
});

const adminSession: Session = {
  account: { id: 'usr_1', login: 'admin', displayName: 'Administrator' },
  capabilities: ['configuration.read', 'configuration.write', 'vpn.read', 'vpn.control'],
  csrfToken: 'csrf',
  expiresAt: '2026-11-29T00:00:00Z',
  idleTimeoutSeconds: 900,
};

const readOnlySession: Session = {
  ...adminSession,
  capabilities: ['configuration.read', 'vpn.read'],
};

const sampleZone = {
  version: '1',
  value: { id: 'zon_vpn', name: 'VPN' },
};

const sampleSegment = {
  version: '1',
  value: {
    id: 'seg_wan',
    name: 'WAN',
    link: 'lnk_wan',
    zone: 'zon_wan',
    addressing: { ipv4: { mode: 'dhcp' }, ipv6: { mode: 'none' } },
  },
};

const samplePeer = {
  id: 'wgp_00000000-0000-4000-8000-000000000010',
  name: 'Workstation',
  enabled: true,
  publicKey: 'PEER_PUB_KEY_12345678901234567890123456789=',
  presharedKeyRef: { secret: 'wg_psk_01', isSet: true, version: '1' },
  allowedIps: ['10.200.0.2/32'],
  persistentKeepalive: 25,
  routeAllowedIps: true,
};

const sampleIface: WireGuardInterface = {
  id: 'wg_00000000-0000-4000-8000-000000000001',
  name: 'Remote Access',
  description: 'Laptops and mobile devices',
  ifname: 'wg0',
  enabled: true,
  zone: 'zon_vpn',
  listenPort: 51820,
  allowListenPort: true,
  mtu: 1420,
  addresses: ['10.200.0.1/24'],
  privateKeyRef: { secret: 'wg_key_01', isSet: true, version: '1' },
  publicKey: 'SERVER_PUB_KEY_123456789012345678901234567=',
  peers: [samplePeer],
};

describe('WireGuardScreen', () => {
  it('renders interfaces and live peer status, creates an interface with keypair generation, adds a peer with client-side key injection into QR/config export, and deletes with If-Match', async () => {
    const user = userEvent.setup();
    const onSaved = vi.fn();
    let createdIfaceBody: unknown;
    let updatedPeerBody: unknown;
    let exportBody: unknown;
    let deletedIfMatch: string | null = null;
    let currentIface: WireGuardInterface = sampleIface;
    let currentVersion = '2';

    fetchRoutes({
      '/api/v1/config/vpn/wireguard': () => jsonResponse(200, {
        state: 'saved',
        savedVersion: '5',
        items: [{ version: currentVersion, value: currentIface }],
      }),
      '/api/v1/config/zones': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleZone] }),
      '/api/v1/config/segments': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleSegment] }),
      '/api/v1/status/vpn/wireguard': () => jsonResponse(200, {
        items: [
          {
            id: sampleIface.id,
            ifname: 'wg0',
            publicKey: sampleIface.publicKey,
            listenPort: 51820,
            active: true,
            peers: [
              {
                id: samplePeer.id,
                name: 'Workstation',
                publicKey: samplePeer.publicKey,
                endpoint: '198.51.100.10:53120',
                latestHandshakeAt: '2026-11-28T06:50:00Z',
                rxBytes: 2048,
                txBytes: 4096,
                online: true,
                applied: true,
              },
            ],
          },
        ],
      }),
      'POST /api/v1/vpn/wireguard/keypair': (init) => {
        const req = JSON.parse(String(init?.body ?? '{}')) as { includePsk?: boolean };
        return jsonResponse(200, {
          privateKey: req.includePsk ? 'CLIENT_PRIV_KEY_SECRET_44_CHARS_1234567890=' : 'IFACE_PRIV_KEY_SECRET_44_CHARS_12345678901=',
          publicKey: req.includePsk ? 'CLIENT_PUB_KEY_123456789012345678901234567=' : 'IFACE_PUB_KEY_1234567890123456789012345678=',
          ...(req.includePsk ? { presharedKey: 'GENERATED_PSK_1234567890123456789012345678=' } : {}),
        });
      },
      'POST /api/v1/config/vpn/wireguard': (init) => {
        createdIfaceBody = JSON.parse(String(init?.body ?? '{}'));
        return jsonResponse(201, {
          state: 'saved',
          savedVersion: '6',
          version: '6',
          value: { ...sampleIface, id: 'wg_new', name: 'Site B', ifname: 'wg1', peers: [] },
        });
      },
      [`PUT /api/v1/config/vpn/wireguard/${sampleIface.id}`]: (init) => {
        updatedPeerBody = JSON.parse(String(init?.body ?? '{}'));
        currentVersion = '7';
        currentIface = {
          ...sampleIface,
          peers: [
            samplePeer,
            {
              id: 'wgp_phone',
              name: 'Phone',
              enabled: true,
              publicKey: 'CLIENT_PUB_KEY_123456789012345678901234567=',
              presharedKeyRef: { secret: 'wg_psk_02', isSet: true, version: '1' },
              allowedIps: ['10.200.0.3/32'],
              routeAllowedIps: true,
            },
          ],
        };
        return jsonResponse(200, {
          state: 'saved',
          savedVersion: '7',
          version: '7',
          value: currentIface,
        });
      },
      [`POST /api/v1/vpn/wireguard/${sampleIface.id}/peers/wgp_phone/config`]: (init) => {
        exportBody = JSON.parse(String(init?.body ?? '{}'));
        const text = [
          '[Interface]',
          'PrivateKey = <PRIVATE_KEY>',
          'Address = 10.200.0.3/32',
          '',
          '[Peer]',
          `PublicKey = ${sampleIface.publicKey}`,
          'PresharedKey = GENERATED_PSK_1234567890123456789012345678=',
          'AllowedIPs = 0.0.0.0/0',
          'Endpoint = vpn.example.com:51820',
          '',
        ].join('\n');
        return new Response(text, {
          status: 200,
          headers: {
            'Content-Type': 'text/plain; charset=utf-8',
            'Content-Disposition': 'attachment; filename="wg0-phone.conf"',
          },
        });
      },
      [`DELETE /api/v1/config/vpn/wireguard/${sampleIface.id}`]: (init) => {
        deletedIfMatch = new Headers(init?.headers).get('If-Match');
        if (deletedIfMatch !== `"${sampleIface.id}.${currentVersion}"`) {
          return jsonResponse(412, { error: { code: 'PRECONDITION_FAILED', message: 'Stale version.', details: [] } });
        }
        return jsonResponse(200, { state: 'saved', savedVersion: '8' });
      },
    });

    render(
      <WireGuardScreen
        api={new ApiClient('csrf')}
        session={adminSession}
        onSaved={onSaved}
        onSessionLost={vi.fn()}
      />,
    );

    expect(await screen.findByText('Remote Access')).toBeInTheDocument();
    expect(screen.getByText('Workstation')).toBeInTheDocument();
    expect(screen.getByText('Online')).toBeInTheDocument();
    expect(screen.getByText('1/1 peers online')).toBeInTheDocument();
    expect(screen.getByText(/2\.0 KiB/)).toBeInTheDocument();

    // Create new interface with inline keypair generation.
    await user.click(screen.getByRole('button', { name: 'Add interface' }));
    const ifaceDialog = await screen.findByRole('dialog', { name: 'Add WireGuard interface' });
    fireEvent.change(within(ifaceDialog).getByLabelText(/^Name/), { target: { value: 'Site B' } });
    fireEvent.change(within(ifaceDialog).getByLabelText(/^Tunnel addresses/), { target: { value: '10.201.0.1/24' } });
    await user.click(within(ifaceDialog).getByRole('button', { name: 'Generate keypair' }));
    expect(await within(ifaceDialog).findByText('IFACE_PUB_KEY_1234567890123456789012345678=')).toBeInTheDocument();
    await user.click(within(ifaceDialog).getByRole('button', { name: 'Add interface' }));

    await waitFor(() => expect(createdIfaceBody).toMatchObject({
      name: 'Site B',
      ifname: 'wg1',
      listenPort: 51821,
      addresses: ['10.201.0.1/24'],
      privateKeyRef: { set: 'IFACE_PRIV_KEY_SECRET_44_CHARS_12345678901=', expectedVersion: '0' },
    }));

    // Add a peer with generated client keys (client private key must NOT be sent in PUT body).
    await user.click(screen.getByRole('button', { name: 'Add peer to Remote Access' }));
    const peerDialog = await screen.findByRole('dialog', { name: 'Add peer to wg0' });
    fireEvent.change(within(peerDialog).getByLabelText(/^Peer name/), { target: { value: 'Phone' } });
    await user.click(within(peerDialog).getByRole('button', { name: 'Generate client keys' }));
    expect(await within(peerDialog).findByText(/Client private key kept in browser memory/)).toBeInTheDocument();
    fireEvent.change(within(peerDialog).getByLabelText(/^Allowed IPs/), { target: { value: '10.200.0.3/32' } });
    await user.click(within(peerDialog).getByRole('button', { name: 'Add peer' }));

    await waitFor(() => expect(updatedPeerBody).toBeTruthy());
    expect(JSON.stringify(updatedPeerBody)).not.toContain('CLIENT_PRIV_KEY_SECRET');
    expect(await screen.findByText('Phone')).toBeInTheDocument();

    // Open Config / QR export: client private key is pre-filled from browser memory and replaces <PRIVATE_KEY>.
    await user.click(screen.getByRole('button', { name: 'Export config for peer Phone' }));
    const exportDialog = await screen.findByRole('dialog', { name: 'Client configuration — Phone (wg0)' });
    fireEvent.change(within(exportDialog).getByLabelText(/^Appliance endpoint/), { target: { value: 'vpn.example.com:51820' } });
    await user.click(within(exportDialog).getByRole('button', { name: 'Generate config' }));

    await waitFor(() => expect(exportBody).toEqual({
      endpoint: 'vpn.example.com:51820',
      includePsk: true,
    }));
    expect(await within(exportDialog).findByText(/PrivateKey = CLIENT_PRIV_KEY_SECRET_44_CHARS_1234567890=/)).toBeInTheDocument();
    expect(within(exportDialog).getByRole('img', { name: 'WireGuard QR code for Phone' })).toBeInTheDocument();
    await user.click(within(exportDialog).getByRole('button', { name: 'Done' }));

    // Delete interface with current If-Match version (7, after the peer PUT).
    await user.click(screen.getByRole('button', { name: 'Delete interface Remote Access' }));
    const deleteDialog = await screen.findByRole('dialog', { name: 'Delete WireGuard interface' });
    await user.click(within(deleteDialog).getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(deletedIfMatch).toBe(`"${sampleIface.id}.7"`));
    expect(onSaved).toHaveBeenCalled();
  });

  it('hides write and control actions for read-only users, renders Not applied and omitted peers cleanly, and triggers onSessionLost on 401', async () => {
    const user = userEvent.setup();
    const onSessionLost = vi.fn();
    let fail401 = false;

    const bareDisabledIface: WireGuardInterface = {
      id: 'wg_00000000-0000-4000-8000-000000000002',
      name: 'Backup Tunnel',
      ifname: 'wg1',
      enabled: false,
      listenPort: 51821,
      privateKeyRef: { secret: 'wg_key_02', isSet: false, version: '0', unresolved: true },
    };

    fetchRoutes({
      '/api/v1/config/vpn/wireguard': () => {
        if (fail401) {
          return jsonResponse(401, { error: { code: 'UNAUTHORIZED', message: 'Session expired.', details: [] } });
        }
        return jsonResponse(200, {
          state: 'saved',
          savedVersion: '5',
          items: [
            { version: '2', value: sampleIface },
            { version: '2', value: bareDisabledIface },
          ],
        });
      },
      '/api/v1/config/zones': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleZone] }),
      '/api/v1/config/segments': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleSegment] }),
      '/api/v1/status/vpn/wireguard': () => jsonResponse(200, {
        items: [
          {
            id: sampleIface.id,
            ifname: 'wg0',
            publicKey: sampleIface.publicKey ?? '',
            listenPort: 51820,
            active: false,
            peers: [
              {
                id: samplePeer.id,
                name: 'Workstation',
                publicKey: samplePeer.publicKey,
                rxBytes: 0,
                txBytes: 0,
                online: false,
                applied: false,
              },
            ],
          },
        ],
      }),
    });

    render(
      <WireGuardScreen
        api={new ApiClient('csrf')}
        session={readOnlySession}
        onSaved={vi.fn()}
        onSessionLost={onSessionLost}
      />,
    );

    expect(await screen.findByText('Remote Access')).toBeInTheDocument();
    expect(screen.getByText('Backup Tunnel')).toBeInTheDocument();
    expect(screen.getByText('Key unresolved')).toBeInTheDocument();
    expect(screen.getByText('Not applied')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Add interface' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Add peer to Remote Access' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Export config for peer Workstation' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Delete interface Remote Access' })).not.toBeInTheDocument();

    fail401 = true;
    await user.click(screen.getByRole('button', { name: 'Refresh' }));
    await waitFor(() => expect(onSessionLost).toHaveBeenCalledTimes(1));
  });
});
