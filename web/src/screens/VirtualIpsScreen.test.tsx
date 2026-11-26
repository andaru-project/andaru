import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { session } from '../test/fixtures';
import { render } from '../test/render';
import type { SavedObject, Snapshot, VirtualAddress } from '../types';
import { checkVirtualIp, ipFamily, maxVirtualAddresses, sameIpAddress } from '../virtualIps';
import { VirtualIpsScreen } from './VirtualIpsScreen';

afterEach(() => {
  vi.restoreAllMocks();
});

const fixtureVips: Array<SavedObject<VirtualAddress>> = [
  { value: { id: 'vip_dns4', name: 'DNS v4', description: 'Internal resolver', address: '172.16.53.53' }, version: '1' },
  { value: { id: 'vip_dns6', name: 'DNS v6', description: 'Secondary resolver', address: 'fd00:53::53' }, version: '2' },
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
  it('classifies IPv4 and IPv6 addresses, rejects IPv4-mapped forms, and matches canonical IPv6', () => {
    expect(ipFamily('172.16.53.53')).toBe('IPv4');
    expect(ipFamily('fd00:53::53')).toBe('IPv6');
    expect(ipFamily('172.16.53.53/32')).toBeUndefined();
    expect(ipFamily('fe80::1%eth0')).toBeUndefined();
    expect(ipFamily('010.0.0.1')).toBeUndefined();
    expect(ipFamily('::ffff:1.2.3.4')).toBeUndefined();
    expect(ipFamily('::ffff:102:304')).toBeUndefined();
    expect(ipFamily('0:0:0:0:0:ffff:c000:280')).toBeUndefined();

    expect(sameIpAddress('fd00:53::53', 'FD00:0053:0:0:0:0:0:0053')).toBe(true);
    expect(sameIpAddress('172.16.53.53', '172.16.53.53')).toBe(true);
    expect(sameIpAddress('172.16.53.53', '172.16.53.54')).toBe(false);
    expect(sameIpAddress('fd00:53::53/128', 'fd00:53::53')).toBe(false);
  });

  it('rejects non-unicast, loopback, link-local, multicast, broadcast, unspecified, and zone-scoped inputs', () => {
    for (const bad of ['0.0.0.0', '127.0.0.1', '169.254.1.1', '224.0.0.1', '255.255.255.255', '::', '::1', 'fe80::1', 'ff02::1', '::ffff:102:304']) {
      expect(checkVirtualIp('test', bad, []).errors.get('address')).toMatch(/unicast/);
    }
    expect(checkVirtualIp('test', 'fe80::1%eth0', []).errors.get('address')).toMatch(/interface zone/);
    expect(checkVirtualIp('test', '172.16.53.1/32', []).errors.get('address')).toMatch(/prefix length/);
    expect(checkVirtualIp('', '', []).errors.get('name')).toMatch(/needs a name/);
    expect(checkVirtualIp('', '', []).errors.get('address')).toMatch(/needs an address/);
  });
});

describe('VirtualIpsScreen', () => {
  it('lists virtual IPs with family badges and DNS/NAT uses, and filters rows including family badge', async () => {
    const user = userEvent.setup();
    fetchRoutes({
      '/api/v1/config/virtualAddresses': { state: 'saved', savedVersion: '9', items: fixtureVips },
      '/api/v1/config': sampleSnapshot,
    });
    renderVirtualIps();

    expect(await screen.findByText('Internal resolver')).toBeInTheDocument();
    expect(screen.getByText('172.16.53.53')).toBeInTheDocument();
    expect(screen.getByText('IPv6')).toBeInTheDocument();
    expect(screen.getAllByText('IPv4')).toHaveLength(2);
    expect(screen.getByRole('link', { name: 'DNS service' })).toHaveAttribute('href', '/network/dns');
    expect(screen.getByRole('link', { name: 'Port forward 1 (DNS6 forward)' })).toHaveAttribute('href', '/firewall/nat');
    expect(screen.getByText('Not used')).toBeInTheDocument();

    // Filter by "ipv6" (not present in vip_dns6's description "Secondary resolver").
    const filter = screen.getByLabelText('Filter virtual IPs');
    await user.type(filter, 'ipv6');
    expect(screen.getByText('Secondary resolver')).toBeInTheDocument();
    expect(screen.queryByText('Internal resolver')).not.toBeInTheDocument();

    await user.clear(filter);
    await user.type(filter, 'nonexistent');
    expect(screen.getByText('No virtual IP matches the filter.')).toBeInTheDocument();
  });

  it('shows empty state when no virtual IPs exist', async () => {
    fetchRoutes({
      '/api/v1/config/virtualAddresses': { state: 'saved', savedVersion: '1', items: [] },
      '/api/v1/config': { ...sampleSnapshot, savedVersion: '1', value: { ...sampleSnapshot.value, virtualAddresses: [] } },
    });
    renderVirtualIps();
    expect(await screen.findByText('No virtual IPs')).toBeInTheDocument();
    expect(screen.queryByLabelText('Filter virtual IPs')).not.toBeInTheDocument();
  });

  it('validates inputs, adds a virtual IP, and edits an existing one with If-Match (clearing description)', async () => {
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

    // Rejects CIDR prefix and duplicate name, focusing first invalid input.
    const nameInput = within(addDrawer).getByLabelText(/^Name/);
    const addrInput = within(addDrawer).getByRole('textbox', { name: /^Address/ });
    await user.type(nameInput, 'DNS v4');
    await user.type(addrInput, '172.16.53.10/32');
    await user.click(within(addDrawer).getByRole('button', { name: 'Add virtual IP' }));
    expect(await within(addDrawer).findByText('Another virtual IP has this name.')).toBeInTheDocument();
    expect(within(addDrawer).getByText('Enter a single address without a prefix length.')).toBeInTheDocument();
    expect(nameInput).toHaveFocus();

    // Rejects canonical IPv6 duplicate.
    await user.clear(nameInput);
    await user.type(nameInput, 'NTP v6');
    await user.clear(addrInput);
    await user.type(addrInput, 'FD00:0053::0053');
    await user.click(within(addDrawer).getByRole('button', { name: 'Add virtual IP' }));
    expect(await within(addDrawer).findByText('Another virtual IP already has this address.')).toBeInTheDocument();

    // Valid creation.
    await user.clear(addrInput);
    await user.type(addrInput, 'fd00:123::1');
    await user.type(within(addDrawer).getByLabelText('Description'), 'Local NTP');
    await user.click(within(addDrawer).getByRole('button', { name: 'Add virtual IP' }));

    await waitFor(() => expect(postedBody).toEqual({
      name: 'NTP v6',
      address: 'fd00:123::1',
      description: 'Local NTP',
    }));
    expect(onSaved).toHaveBeenCalledTimes(1);

    // Edit targeted VIP: displays port-forward warning, clears description, and sends PUT with If-Match.
    await user.click(screen.getByRole('button', { name: 'Edit virtual IP DNS v6' }));
    const editDrawer = await screen.findByRole('dialog', { name: 'Edit virtual IP: DNS v6' });
    expect(within(editDrawer).getByText('Address in use by port forward')).toBeInTheDocument();
    await user.clear(within(editDrawer).getByLabelText(/^Name/));
    await user.type(within(editDrawer).getByLabelText(/^Name/), 'Resolver v6');
    await user.clear(within(editDrawer).getByLabelText('Description'));
    await user.click(within(editDrawer).getByRole('button', { name: 'Save virtual IP' }));

    await waitFor(() => expect(putBody).toEqual({
      name: 'Resolver v6',
      address: 'fd00:53::53',
    }));
    expect(new Headers(putHeaders).get('If-Match')).toBe('"vip_dns6.2"');
    expect(onSaved).toHaveBeenCalledTimes(2);
  });

  it('maps server-side field 422s, /id refusals, 412 conflicts, and 401 session loss on save', async () => {
    const user = userEvent.setup();
    let mode: 'field' | 'id' | '412' | '401' = 'field';
    fetchRoutes({
      '/api/v1/config/virtualAddresses': { state: 'saved', savedVersion: '9', items: fixtureVips },
      '/api/v1/config': sampleSnapshot,
      'PUT /api/v1/config/virtualAddresses/vip_dns6': () => {
        if (mode === 'field') {
          return jsonResponse(422, {
            error: {
              code: 'VALIDATION_FAILED',
              message: 'Validation failed.',
              details: [{ path: '/virtualAddresses/1/address', message: 'This address is on a segment\'s subnet.' }],
            },
          });
        }
        if (mode === 'id') {
          return jsonResponse(422, {
            error: {
              code: 'VALIDATION_FAILED',
              message: 'Validation failed.',
              details: [{ path: '/id', message: 'Port forward "DNS6 forward" targets this address; change or delete it first.' }],
            },
          });
        }
        if (mode === '412') {
          return jsonResponse(412, { error: { code: 'CONFLICT', message: 'Precondition failed.' } });
        }
        return jsonResponse(401, { error: { code: 'UNAUTHENTICATED', message: 'Session expired.' } });
      },
    });
    const { onSessionLost } = renderVirtualIps();

    await user.click(await screen.findByRole('button', { name: 'Edit virtual IP DNS v6' }));
    const editDrawer = await screen.findByRole('dialog', { name: 'Edit virtual IP: DNS v6' });
    const addressInput = within(editDrawer).getByRole('textbox', { name: /^Address/ });
    await user.clear(addressInput);
    await user.type(addressInput, 'fd00:53::54');
    await user.click(within(editDrawer).getByRole('button', { name: 'Save virtual IP' }));

    expect(await within(editDrawer).findByText("This address is on a segment's subnet.")).toBeInTheDocument();
    expect(addressInput).toHaveFocus();

    mode = 'id';
    await user.click(within(editDrawer).getByRole('button', { name: 'Save virtual IP' }));
    expect(await within(editDrawer).findByRole('alert')).toHaveTextContent('Port forward "DNS6 forward" targets this address; change or delete it first.');

    mode = '412';
    await user.click(within(editDrawer).getByRole('button', { name: 'Save virtual IP' }));
    expect(await within(editDrawer).findByRole('alert')).toHaveTextContent('changed by someone else');

    mode = '401';
    await user.click(within(editDrawer).getByRole('button', { name: 'Save virtual IP' }));
    await waitFor(() => expect(onSessionLost).toHaveBeenCalledTimes(1));
  });

  it('blocks deletion for DNS and port-forward references, handles 412/404/422/401, and deletes unused VIPs', async () => {
    const user = userEvent.setup();
    let deleteMode: 'ok' | '412' | '404' | '422' | '401' = '412';
    let deleteHeaders: HeadersInit | undefined;
    fetchRoutes({
      '/api/v1/config/virtualAddresses': { state: 'saved', savedVersion: '9', items: fixtureVips },
      '/api/v1/config': sampleSnapshot,
      'DELETE /api/v1/config/virtualAddresses/vip_spare': (init) => {
        deleteHeaders = init?.headers;
        if (deleteMode === '412') return jsonResponse(412, { error: { code: 'CONFLICT', message: 'Stale.' } });
        if (deleteMode === '404') return jsonResponse(404, { error: { code: 'NOT_FOUND', message: 'Missing.' } });
        if (deleteMode === '422') {
          return jsonResponse(422, { error: { code: 'VALIDATION_FAILED', message: 'Validation failed.', details: [{ path: '/dns/virtualAddresses/0', message: 'Expected an existing virtual address.' }] } });
        }
        if (deleteMode === '401') return jsonResponse(401, { error: { code: 'UNAUTHENTICATED', message: 'Expired.' } });
        return jsonResponse(200, { state: 'saved', savedVersion: '10' });
      },
    });
    const { onSaved, onSessionLost } = renderVirtualIps();

    // DNS blocker.
    await user.click(await screen.findByRole('button', { name: 'Delete virtual IP DNS v4' }));
    const dnsBlocked = await screen.findByRole('dialog', { name: 'Delete virtual IP' });
    expect(dnsBlocked).toHaveTextContent('DNS service');
    await user.click(within(dnsBlocked).getByRole('button', { name: 'Keep it' }));

    // Port-forward blocker.
    await user.click(screen.getByRole('button', { name: 'Delete virtual IP DNS v6' }));
    const pfBlocked = await screen.findByRole('dialog', { name: 'Delete virtual IP' });
    expect(pfBlocked).toHaveTextContent('Port forward 1 (DNS6 forward)');
    await user.click(within(pfBlocked).getByRole('button', { name: 'Keep it' }));

    // 412 conflict on delete.
    await user.click(screen.getByRole('button', { name: 'Delete virtual IP Spare' }));
    await user.click(within(await screen.findByRole('dialog', { name: 'Delete virtual IP' })).getByRole('button', { name: 'Delete virtual IP' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Virtual IP Spare changed elsewhere.');

    // 404 already deleted.
    deleteMode = '404';
    await user.click(screen.getByRole('button', { name: 'Delete virtual IP Spare' }));
    await user.click(within(await screen.findByRole('dialog', { name: 'Delete virtual IP' })).getByRole('button', { name: 'Delete virtual IP' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('already deleted elsewhere');

    // 422 concurrent reference added.
    deleteMode = '422';
    await user.click(screen.getByRole('button', { name: 'Delete virtual IP Spare' }));
    await user.click(within(await screen.findByRole('dialog', { name: 'Delete virtual IP' })).getByRole('button', { name: 'Delete virtual IP' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Virtual IP Spare is in use and cannot be deleted.');

    // 401 session lost on delete.
    deleteMode = '401';
    await user.click(screen.getByRole('button', { name: 'Delete virtual IP Spare' }));
    await user.click(within(await screen.findByRole('dialog', { name: 'Delete virtual IP' })).getByRole('button', { name: 'Delete virtual IP' }));
    await waitFor(() => expect(onSessionLost).toHaveBeenCalledTimes(1));

    // Successful delete with If-Match.
    deleteMode = 'ok';
    await user.click(screen.getByRole('button', { name: 'Delete virtual IP Spare' }));
    await user.click(within(await screen.findByRole('dialog', { name: 'Delete virtual IP' })).getByRole('button', { name: 'Delete virtual IP' }));
    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(new Headers(deleteHeaders).get('If-Match')).toBe('"vip_spare.3"');
  });

  it('hides write actions and limit alert for read-only users, and disables Add when 64 virtual IPs exist', async () => {
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
    expect(screen.queryByText('Virtual IP limit reached')).not.toBeInTheDocument();
    first.unmount();

    renderVirtualIps();
    expect(await screen.findByText('Virtual IP limit reached')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Add virtual IP' })).toBeDisabled();
  });
});
