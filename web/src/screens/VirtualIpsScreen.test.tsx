import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { session } from '../test/fixtures';
import { render } from '../test/render';
import type { SavedObject, Snapshot, VirtualAddress } from '../types';
import { ipFamily, maxVirtualAddresses, sameIpAddress } from '../virtualIps';
import { VirtualIpsScreen } from './VirtualIpsScreen';

afterEach(() => {
  vi.restoreAllMocks();
});

const fixtureVips: Array<SavedObject<VirtualAddress>> = [
  { value: { id: 'vip_dns4', name: 'DNS v4', description: 'Internal resolver', address: '172.16.53.53' }, version: '1' },
  { value: { id: 'vip_dns6', name: 'DNS v6', description: 'IPv6 resolver', address: 'fd00:53::53' }, version: '2' },
  { value: { id: 'vip_spare', name: 'Spare', address: '172.16.53.99' }, version: '3' },
];

const sampleSnapshot: Snapshot = {
  state: 'saved',
  savedVersion: '9',
  value: {
    links: [{ id: 'lnk_1', name: 'eth0', kind: 'ethernet', enabled: true, mtu: 1500 }],
    segments: [
      { id: 'seg_lan', name: 'LAN', link: 'lnk_1', zone: 'zon_lan', addressing: { ipv4: { mode: 'static', addresses: ['192.168.1.1/24'] }, ipv6: { mode: 'none' } } },
    ],
    zones: [{ id: 'zon_lan', name: 'LAN' }, { id: 'zon_wan', name: 'WAN' }],
    dns: {
      mode: 'recursive',
      segments: ['seg_lan'],
      virtualAddresses: ['vip_dns4'],
    },
    virtualAddresses: fixtureVips.map((v) => v.value),
    nat: {
      portForwards: [
        {
          id: 'pfw_v6',
          name: 'DNS6 forward',
          enabled: true,
          family: 'ipv6',
          ingressZones: ['zon_wan'],
          protocol: 'udp',
          from: [],
          to: { self: true },
          ports: '53',
          target: { address: 'FD00:0053::0053' },
        },
      ],
    },
  },
};

function renderVirtualIps(caps = ['configuration.read', 'configuration.write']) {
  const onSaved = vi.fn();
  const onSessionLost = vi.fn();
  render(
    <MemoryRouter>
      <VirtualIpsScreen api={new ApiClient('csrf')} session={session(caps)} onSaved={onSaved} onSessionLost={onSessionLost} />
    </MemoryRouter>,
  );
  return { onSaved, onSessionLost };
}

describe('VirtualIpsScreen helpers', () => {
  it('classifies IPv4 and IPv6 addresses and matches canonical IPv6 forms', () => {
    expect(ipFamily('172.16.53.53')).toBe('IPv4');
    expect(ipFamily('fd00:53::53')).toBe('IPv6');
    expect(ipFamily('172.16.53.53/32')).toBeUndefined();
    expect(ipFamily('fe80::1%eth0')).toBeUndefined();
    expect(ipFamily('010.0.0.1')).toBeUndefined();
    expect(ipFamily('::ffff:1.2.3.4')).toBeUndefined();

    expect(sameIpAddress('fd00:53::53', 'FD00:0053:0:0:0:0:0:0053')).toBe(true);
    expect(sameIpAddress('172.16.53.53', '172.16.53.53')).toBe(true);
    expect(sameIpAddress('172.16.53.53', '172.16.53.54')).toBe(false);
    expect(sameIpAddress('fd00:53::53/128', 'fd00:53::53')).toBe(false);
  });
});

describe('VirtualIpsScreen', () => {
  it('lists virtual IPs with family badges and DNS/NAT uses, and filters rows', async () => {
    const user = userEvent.setup();
    fetchRoutes({
      '/api/v1/config/virtualAddresses': { state: 'saved', savedVersion: '9', items: fixtureVips },
      '/api/v1/config': sampleSnapshot,
    });
    renderVirtualIps();

    expect(await screen.findByText('Internal resolver')).toBeInTheDocument();
    expect(screen.getByText('172.16.53.53')).toBeInTheDocument();
    expect(screen.getByText('fd00:53::53')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'DNS service' })).toHaveAttribute('href', '/network/dns');
    expect(screen.getByRole('link', { name: 'Port forward 1 (DNS6 forward)' })).toHaveAttribute('href', '/firewall/nat');
    expect(screen.getByText('Not used')).toBeInTheDocument();

    await user.type(screen.getByLabelText('Filter virtual IPs'), 'ipv6');
    expect(screen.getByText('fd00:53::53')).toBeInTheDocument();
    expect(screen.queryByText('172.16.53.53')).not.toBeInTheDocument();
  });

  it('validates inputs, adds a virtual IP, and edits an existing one with If-Match', async () => {
    const user = userEvent.setup();
    let postedBody: Record<string, unknown> | undefined;
    let putBody: Record<string, unknown> | undefined;
    let putHeaders: HeadersInit | undefined;
    fetchRoutes({
      '/api/v1/config/virtualAddresses': { state: 'saved', savedVersion: '9', items: fixtureVips },
      '/api/v1/config': sampleSnapshot,
      'POST /api/v1/config/virtualAddresses': (init) => {
        postedBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
        return jsonResponse(201, { value: { id: 'vip_new', ...postedBody }, version: '10' });
      },
      'PUT /api/v1/config/virtualAddresses/vip_dns6': (init) => {
        putHeaders = init?.headers;
        putBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
        return jsonResponse(200, { value: { id: 'vip_dns6', ...putBody }, version: '11' });
      },
    });
    const { onSaved } = renderVirtualIps();

    await user.click(await screen.findByRole('button', { name: 'Add virtual IP' }));
    const addDrawer = await screen.findByRole('dialog', { name: 'Add virtual IP' });

    // Rejects CIDR prefix and duplicate name.
    await user.type(within(addDrawer).getByLabelText(/^Name/), 'DNS v4');
    await user.type(within(addDrawer).getByLabelText(/^Address/), '172.16.53.10/32');
    await user.click(within(addDrawer).getByRole('button', { name: 'Add virtual IP' }));
    expect(await within(addDrawer).findByText('Another virtual IP has this name.')).toBeInTheDocument();
    expect(within(addDrawer).getByText('Enter a single address without a prefix length.')).toBeInTheDocument();

    // Rejects canonical IPv6 duplicate.
    await user.clear(within(addDrawer).getByLabelText(/^Name/));
    await user.type(within(addDrawer).getByLabelText(/^Name/), 'NTP v6');
    await user.clear(within(addDrawer).getByLabelText(/^Address/));
    await user.type(within(addDrawer).getByLabelText(/^Address/), 'FD00:0053::0053');
    await user.click(within(addDrawer).getByRole('button', { name: 'Add virtual IP' }));
    expect(await within(addDrawer).findByText('Another virtual IP already has this address.')).toBeInTheDocument();

    // Valid creation.
    await user.clear(within(addDrawer).getByLabelText(/^Address/));
    await user.type(within(addDrawer).getByLabelText(/^Address/), 'fd00:123::1');
    await user.type(within(addDrawer).getByLabelText('Description'), 'Local NTP');
    await user.click(within(addDrawer).getByRole('button', { name: 'Add virtual IP' }));

    await waitFor(() => expect(postedBody).toEqual({
      name: 'NTP v6',
      address: 'fd00:123::1',
      description: 'Local NTP',
    }));
    expect(onSaved).toHaveBeenCalledTimes(1);

    // Edit targeted VIP: displays port-forward warning and sends PUT with If-Match.
    await user.click(screen.getByRole('button', { name: 'Edit virtual IP DNS v6' }));
    const editDrawer = await screen.findByRole('dialog', { name: 'Edit virtual IP: DNS v6' });
    expect(within(editDrawer).getByText('Address in use by port forward')).toBeInTheDocument();
    await user.clear(within(editDrawer).getByLabelText(/^Name/));
    await user.type(within(editDrawer).getByLabelText(/^Name/), 'Resolver v6');
    await user.click(within(editDrawer).getByRole('button', { name: 'Save virtual IP' }));

    await waitFor(() => expect(putBody).toEqual({
      name: 'Resolver v6',
      address: 'fd00:53::53',
      description: 'IPv6 resolver',
    }));
    expect(new Headers(putHeaders).get('If-Match')).toBe('"vip_dns6.2"');
  });

  it('surfaces server-side /id refusal when re-addressing a targeted virtual IP', async () => {
    const user = userEvent.setup();
    fetchRoutes({
      '/api/v1/config/virtualAddresses': { state: 'saved', savedVersion: '9', items: fixtureVips },
      '/api/v1/config': sampleSnapshot,
      'PUT /api/v1/config/virtualAddresses/vip_dns6': () => jsonResponse(422, {
        error: {
          code: 'VALIDATION_FAILED',
          message: 'Validation failed.',
          details: [{ path: '/id', message: 'Port forward "DNS6 forward" targets this address; change or delete it first.' }],
        },
      }),
    });
    renderVirtualIps();

    await user.click(await screen.findByRole('button', { name: 'Edit virtual IP DNS v6' }));
    const editDrawer = await screen.findByRole('dialog', { name: 'Edit virtual IP: DNS v6' });
    const addressInput = within(editDrawer).getByRole('textbox', { name: /^Address/ });
    await user.clear(addressInput);
    await user.type(addressInput, 'fd00:53::54');
    await user.click(within(editDrawer).getByRole('button', { name: 'Save virtual IP' }));

    const alert = await within(editDrawer).findByRole('alert');
    expect(alert).toHaveTextContent('Port forward "DNS6 forward" targets this address; change or delete it first.');
  });

  it('blocks deletion while referenced by DNS or port forwards, and deletes an unused virtual IP with If-Match', async () => {
    const user = userEvent.setup();
    let deleteHeaders: HeadersInit | undefined;
    fetchRoutes({
      '/api/v1/config/virtualAddresses': { state: 'saved', savedVersion: '9', items: fixtureVips },
      '/api/v1/config': sampleSnapshot,
      'DELETE /api/v1/config/virtualAddresses/vip_spare': (init) => {
        deleteHeaders = init?.headers;
        return jsonResponse(200, { state: 'saved', savedVersion: '10' });
      },
    });
    const { onSaved } = renderVirtualIps();

    // DNS blocker.
    await user.click(await screen.findByRole('button', { name: 'Delete virtual IP DNS v4' }));
    const blocked = await screen.findByRole('dialog', { name: 'Delete virtual IP' });
    expect(blocked).toHaveTextContent('cannot be deleted while these use it');
    expect(blocked).toHaveTextContent('DNS service');
    await user.click(within(blocked).getByRole('button', { name: 'Keep it' }));

    // Unused VIP deletes cleanly.
    await user.click(screen.getByRole('button', { name: 'Delete virtual IP Spare' }));
    const confirm = await screen.findByRole('dialog', { name: 'Delete virtual IP' });
    await user.click(within(confirm).getByRole('button', { name: 'Delete virtual IP' }));

    await waitFor(() => expect(deleteHeaders).toBeDefined());
    expect(new Headers(deleteHeaders).get('If-Match')).toBe('"vip_spare.3"');
    expect(onSaved).toHaveBeenCalledTimes(1);
  });

  it('hides write actions for read-only users and disables Add when 64 virtual IPs exist', async () => {
    const fullList: Array<SavedObject<VirtualAddress>> = Array.from({ length: maxVirtualAddresses }, (_, i) => ({
      value: { id: `vip_${i}`, name: `VIP ${i}`, address: `172.16.10.${i + 1}` },
      version: '1',
    }));
    fetchRoutes({
      '/api/v1/config/virtualAddresses': { state: 'saved', savedVersion: '9', items: fullList },
      '/api/v1/config': { ...sampleSnapshot, value: { ...sampleSnapshot.value, virtualAddresses: fullList.map((v) => v.value) } },
    });

    const first = render(
      <MemoryRouter>
        <VirtualIpsScreen api={new ApiClient('csrf')} session={session(['configuration.read'])} onSaved={vi.fn()} onSessionLost={vi.fn()} />
      </MemoryRouter>,
    );
    expect(await screen.findByText('VIP 0')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Add virtual IP' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Edit virtual IP/ })).not.toBeInTheDocument();
    first.unmount();

    renderVirtualIps();
    expect(await screen.findByText('Virtual IP limit reached')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Add virtual IP' })).toBeDisabled();
  });
});
