import { fireEvent } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { render, screen, waitFor, within } from '../test/render';
import type { Session } from '../types';
import { IPsecScreen } from './IPsecScreen';

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

const readOnlyConfigSession: Session = {
  ...adminSession,
  capabilities: ['configuration.read', 'vpn.read'],
};

const statusOnlySession: Session = {
  ...adminSession,
  capabilities: ['vpn.read'],
};

const sampleZone = {
  version: '1',
  value: { id: 'zon_vpn', name: 'VPN', color: 'blue' },
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

const sampleCA = {
  version: '1',
  value: {
    id: 'ca_corp',
    name: 'Corp Root CA',
    subject: 'CN=Corp Root CA',
    serial: '01',
    fingerprintSha256: 'aa',
    notBefore: '2026-01-01T00:00:00Z',
    notAfter: '2036-01-01T00:00:00Z',
    certPem: '-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----',
  },
};

const sampleCert = {
  version: '1',
  value: {
    id: 'crt_gw',
    name: 'Gateway Server Cert',
    caId: 'ca_corp',
    subject: 'CN=gw.example.com',
    serial: '02',
    fingerprintSha256: 'bb',
    notBefore: '2026-01-01T00:00:00Z',
    notAfter: '2027-01-01T00:00:00Z',
    certPem: '-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----',
    keyRef: { secret: 'pki_cert_gw', isSet: true, version: '1' },
    keyAlgorithm: 'ecdsa-p256',
  },
};

const sampleConnection = {
  version: '4',
  value: {
    id: 'ips_hq',
    name: 'HQ Tunnel',
    description: 'Route-based XFRM to HQ',
    enabled: true,
    mode: 'route-based',
    ifname: 'ipsec0',
    xfrmIfId: 10,
    zone: 'zon_vpn',
    tunnelAddresses: ['169.254.100.1/30'],
    localSegment: 'seg_wan',
    remoteAddress: '198.51.100.10',
    ikeVersion: 2,
    auth: {
      localType: 'psk',
      remoteType: 'psk',
      localId: 'branch.example.com',
      remoteId: 'hq.example.com',
      pskRef: { secret: 'ipsec_psk_hq', isSet: true, version: '2' },
    },
    dpdDelaySeconds: 30,
    startAction: 'trap',
    natTraversal: true,
  },
};

const sampleStatus = {
  serviceActive: true,
  items: [
    {
      id: 'ips_hq',
      name: 'HQ Tunnel',
      mode: 'route-based',
      applied: true,
      active: true,
      ikeSAs: [
        {
          uniqueId: '1',
          state: 'ESTABLISHED',
          localHost: '203.0.113.2',
          remoteHost: '198.51.100.10',
          establishedSec: 3660,
          childSAs: [
            {
              name: 'ips_hq',
              state: 'INSTALLED',
              localTs: ['0.0.0.0/0'],
              remoteTs: ['0.0.0.0/0'],
              bytesIn: 2048,
              bytesOut: 4096,
              rekeySec: 1800,
            },
          ],
        },
      ],
    },
  ],
};

describe('IPsecScreen', () => {
  it('renders live IKE/CHILD SAs, initiates/terminates, creates, edits (keeping PSK), and deletes with If-Match', async () => {
    const user = userEvent.setup();
    const onSaved = vi.fn();
    let createdBody: unknown;
    let updatedBody: unknown;
    let updatedIfMatch: string | null = null;
    let deletedIfMatch: string | null = null;
    let initiated = 0;
    let terminated = 0;

    fetchRoutes({
      '/api/v1/config/vpn/ipsec': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleConnection] }),
      '/api/v1/config/zones': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleZone] }),
      '/api/v1/config/segments': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleSegment] }),
      '/api/v1/config/pki/cas': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleCA] }),
      '/api/v1/config/pki/certificates': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleCert] }),
      '/api/v1/status/vpn/ipsec': () => jsonResponse(200, sampleStatus),
      'POST /api/v1/vpn/ipsec/ips_hq/initiate': () => {
        initiated += 1;
        return jsonResponse(200, { ok: true });
      },
      'POST /api/v1/vpn/ipsec/ips_hq/terminate': () => {
        terminated += 1;
        return jsonResponse(200, { ok: true });
      },
      'POST /api/v1/config/vpn/ipsec': (init) => {
        createdBody = JSON.parse(String(init?.body ?? '{}'));
        return jsonResponse(201, {
          state: 'saved',
          savedVersion: '6',
          version: '1',
          value: { ...sampleConnection.value, id: 'ips_new', name: 'DR Site' },
        });
      },
      'PUT /api/v1/config/vpn/ipsec/ips_hq': (init) => {
        updatedBody = JSON.parse(String(init?.body ?? '{}'));
        updatedIfMatch = new Headers(init?.headers).get('If-Match');
        return jsonResponse(200, { state: 'saved', savedVersion: '7', ...sampleConnection });
      },
      'DELETE /api/v1/config/vpn/ipsec/ips_hq': (init) => {
        deletedIfMatch = new Headers(init?.headers).get('If-Match');
        return jsonResponse(200, { state: 'saved', savedVersion: '8' });
      },
    });

    render(
      <IPsecScreen
        api={new ApiClient('csrf')}
        session={adminSession}
        onSaved={onSaved}
        onSessionLost={vi.fn()}
      />,
    );

    expect(await screen.findByText('HQ Tunnel')).toBeInTheDocument();
    expect(screen.getByText('strongSwan Active')).toBeInTheDocument();
    expect(screen.getByText('1 SA established')).toBeInTheDocument();
    expect(screen.getByText('ESTABLISHED')).toBeInTheDocument();
    expect(screen.getByText('INSTALLED')).toBeInTheDocument();
    expect(screen.getByText(/RX 2\.0 KiB \/ TX 4\.0 KiB/)).toBeInTheDocument();

    // Initiate and terminate controls
    await user.click(screen.getByRole('button', { name: 'Initiate HQ Tunnel' }));
    await waitFor(() => expect(initiated).toBe(1));
    await user.click(screen.getByRole('button', { name: 'Terminate HQ Tunnel' }));
    await waitFor(() => expect(terminated).toBe(1));

    // Create a new route-based connection
    await user.click(screen.getByRole('button', { name: 'Add connection' }));
    const addDialog = await screen.findByRole('dialog', { name: 'Add IPsec connection' });
    fireEvent.change(within(addDialog).getByLabelText(/^Name/), { target: { value: 'DR Site' } });
    fireEvent.change(within(addDialog).getByLabelText(/^Remote peer address/), { target: { value: '198.51.100.20' } });
    fireEvent.change(within(addDialog).getByLabelText(/^Tunnel addresses/), { target: { value: '169.254.200.1/30' } });
    fireEvent.change(within(addDialog).getByLabelText(/^Local IKE identity/), { target: { value: 'gw.example.com' } });
    fireEvent.change(within(addDialog).getByLabelText(/^Pre-shared key/), { target: { value: 'super-secret-psk' } });
    await user.click(within(addDialog).getByRole('button', { name: 'Add connection' }));

    await waitFor(() => expect(createdBody).toMatchObject({
      name: 'DR Site',
      mode: 'route-based',
      ifname: 'ipsec1',
      xfrmIfId: 1,
      zone: 'zon_vpn',
      remoteAddress: '198.51.100.20',
      tunnelAddresses: ['169.254.200.1/30'],
      ikeVersion: 2,
      auth: {
        localType: 'psk',
        remoteType: 'psk',
        localId: 'gw.example.com',
        pskRef: { set: 'super-secret-psk', expectedVersion: '0' },
      },
    }));

    // Edit existing connection without re-entering PSK (must send {} to preserve)
    await user.click(screen.getByRole('button', { name: 'Edit IPsec connection HQ Tunnel' }));
    const editDialog = await screen.findByRole('dialog', { name: 'Edit IPsec connection' });
    fireEvent.change(within(editDialog).getByLabelText(/^Description/), { target: { value: 'Updated HQ' } });
    await user.click(within(editDialog).getByRole('button', { name: 'Save connection' }));
    await waitFor(() => expect(updatedIfMatch).toBe('"ips_hq.4"'));
    expect(updatedBody).toMatchObject({
      name: 'HQ Tunnel',
      description: 'Updated HQ',
      auth: {
        pskRef: {},
      },
    });

    // Delete with If-Match
    await user.click(screen.getByRole('button', { name: 'Delete IPsec connection HQ Tunnel' }));
    const delDialog = await screen.findByRole('dialog', { name: 'Delete IPsec connection' });
    await user.click(within(delDialog).getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(deletedIfMatch).toBe('"ips_hq.4"'));
    expect(onSaved).toHaveBeenCalledTimes(3);
  });

  it('creates a road-warrior cert + EAP-MSCHAPv2 connection with dynamic EAP users', async () => {
    const user = userEvent.setup();
    let createdBody: unknown;

    fetchRoutes({
      '/api/v1/config/vpn/ipsec': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [] }),
      '/api/v1/config/zones': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleZone] }),
      '/api/v1/config/segments': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleSegment] }),
      '/api/v1/config/pki/cas': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleCA] }),
      '/api/v1/config/pki/certificates': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleCert] }),
      '/api/v1/status/vpn/ipsec': () => jsonResponse(200, { serviceActive: false, items: [] }),
      'POST /api/v1/config/vpn/ipsec': (init) => {
        createdBody = JSON.parse(String(init?.body ?? '{}'));
        return jsonResponse(201, {
          state: 'saved',
          savedVersion: '6',
          version: '1',
          value: { id: 'ips_rw', ...(createdBody as Record<string, unknown>) },
        });
      },
    });

    render(
      <IPsecScreen
        api={new ApiClient('csrf')}
        session={adminSession}
        onSaved={vi.fn()}
        onSessionLost={vi.fn()}
      />,
    );

    expect(await screen.findByText('No IPsec connections configured')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Add connection' }));
    const dialog = await screen.findByRole('dialog', { name: 'Add IPsec connection' });

    fireEvent.change(within(dialog).getByLabelText(/^Name/), { target: { value: 'Mobile Clients' } });
    await user.click(within(dialog).getByLabelText(/^Connection mode/, { selector: 'input' }));
    await user.click(await screen.findByRole('option', { name: 'Road-warrior server' }));

    fireEvent.change(within(dialog).getByLabelText(/^Virtual IP pool/), { target: { value: '10.99.0.0/24' } });

    await user.click(within(dialog).getByLabelText(/^Local authentication/, { selector: 'input' }));
    await user.click(await screen.findByRole('option', { name: 'Certificate' }));
    await user.click(within(dialog).getByLabelText(/^Remote authentication/, { selector: 'input' }));
    await user.click(await screen.findByRole('option', { name: 'EAP-MSCHAPv2 (username & password)' }));

    fireEvent.change(within(dialog).getByLabelText(/^Local IKE identity/), { target: { value: 'vpn.example.com' } });
    await user.click(within(dialog).getByRole('button', { name: 'Add EAP user' }));
    fireEvent.change(within(dialog).getByLabelText('EAP username 1'), { target: { value: 'alice' } });
    fireEvent.change(within(dialog).getByLabelText('EAP password 1'), { target: { value: 'alice-secret' } });

    await user.click(within(dialog).getByRole('button', { name: 'Add connection' }));

    await waitFor(() => expect(createdBody).toMatchObject({
      name: 'Mobile Clients',
      mode: 'road-warrior',
      ifname: 'ipsec0',
      xfrmIfId: 1,
      remoteAddress: '%any',
      virtualPool: ['10.99.0.0/24'],
      startAction: 'none',
      auth: {
        localType: 'cert',
        remoteType: 'eap-mschapv2',
        localId: 'vpn.example.com',
        localCertId: 'crt_gw',
        eapUsers: [
          {
            username: 'alice',
            passwordRef: { set: 'alice-secret', expectedVersion: '0' },
          },
        ],
      },
    }));
  });

  it('handles 412 conflict keeping Close clickable, triggers onSessionLost on 401, and respects read-only / vpn.read-only permissions', async () => {
    const user = userEvent.setup();
    const onSessionLost = vi.fn();
    let putStatus = 412;

    fetchRoutes({
      '/api/v1/config/vpn/ipsec': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleConnection] }),
      '/api/v1/config/zones': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleZone] }),
      '/api/v1/config/segments': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleSegment] }),
      '/api/v1/config/pki/cas': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleCA] }),
      '/api/v1/config/pki/certificates': () => jsonResponse(200, { state: 'saved', savedVersion: '5', items: [sampleCert] }),
      '/api/v1/status/vpn/ipsec': () => jsonResponse(200, sampleStatus),
      'PUT /api/v1/config/vpn/ipsec/ips_hq': () => {
        if (putStatus === 401) {
          return jsonResponse(401, { error: { code: 'UNAUTHORIZED', message: 'Expired', details: [] } });
        }
        return jsonResponse(412, { error: { code: 'PRECONDITION_FAILED', message: 'Modified elsewhere.', details: [] } });
      },
    });

    const { rerender } = render(
      <IPsecScreen
        api={new ApiClient('csrf')}
        session={adminSession}
        onSaved={vi.fn()}
        onSessionLost={onSessionLost}
      />,
    );

    expect(await screen.findByText('HQ Tunnel')).toBeInTheDocument();

    // 412 disables Save while keeping Close enabled
    await user.click(screen.getByRole('button', { name: 'Edit IPsec connection HQ Tunnel' }));
    const editDialog = await screen.findByRole('dialog', { name: 'Edit IPsec connection' });
    await user.click(within(editDialog).getByRole('button', { name: 'Save connection' }));
    await waitFor(() => expect(within(editDialog).getByRole('button', { name: 'Save connection' })).toBeDisabled());
    const closeButtons = within(editDialog).getAllByRole('button', { name: 'Close' });
    expect(closeButtons.length).toBe(2);
    for (const btn of closeButtons) expect(btn).toBeEnabled();
    await user.click(closeButtons[closeButtons.length - 1]);

    // 401 calls onSessionLost
    putStatus = 401;
    await user.click(screen.getByRole('button', { name: 'Edit IPsec connection HQ Tunnel' }));
    const editDialog2 = await screen.findByRole('dialog', { name: 'Edit IPsec connection' });
    await user.click(within(editDialog2).getByRole('button', { name: 'Save connection' }));
    await waitFor(() => expect(onSessionLost).toHaveBeenCalledTimes(1));
    await user.click(within(editDialog2).getByRole('button', { name: 'Cancel' }));

    // Read-only config session hides write and control buttons
    rerender(
      <IPsecScreen
        api={new ApiClient('csrf')}
        session={readOnlyConfigSession}
        onSaved={vi.fn()}
        onSessionLost={onSessionLost}
      />,
    );
    expect(screen.queryByRole('button', { name: 'Add connection' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Edit IPsec connection HQ Tunnel' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Initiate HQ Tunnel' })).not.toBeInTheDocument();

    // vpn.read-only status view still shows full IKE/CHILD SA telemetry
    rerender(
      <IPsecScreen
        api={new ApiClient('csrf')}
        session={statusOnlySession}
        onSaved={vi.fn()}
        onSessionLost={onSessionLost}
      />,
    );
    expect(await screen.findByText('HQ Tunnel')).toBeInTheDocument();
    expect(screen.getByText('ESTABLISHED')).toBeInTheDocument();
    expect(screen.getByText(/RX 2\.0 KiB \/ TX 4\.0 KiB/)).toBeInTheDocument();
  });
});
