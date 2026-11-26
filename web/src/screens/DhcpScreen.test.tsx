import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter } from 'react-router';
import { ApiClient } from '../api';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { session } from '../test/fixtures';
import { render } from '../test/render';
import { setTimeZone } from '../timezone';
import type { Dhcp4Entry, Dhcp6Entry, LeaseView, Snapshot } from '../types';
import {
  checkDhcp4, dhcp4Payload, ipv6SuffixOf, leaseMatchesStatic4, leaseMatchesStatic6, newRowKey, suggestedIpv4Range,
} from '../dhcpEdit';
import { DhcpScreen } from './DhcpScreen';

const fullCaps = ['configuration.read', 'configuration.write', 'status.read'];

const lanSeg = {
  id: 'seg_lan',
  name: 'lan',
  link: 'lnk_lan',
  zone: 'zon_lan',
  addressing: {
    ipv4: { mode: 'static', addresses: ['192.168.10.1/24'] },
    ipv6: { mode: 'static', addresses: ['fd00:10::1/64'], ra: { mode: 'unmanaged' } },
  },
};

const guestSeg = {
  id: 'seg_guest',
  name: 'guest',
  link: 'lnk_guest',
  zone: 'zon_guest',
  addressing: {
    ipv4: { mode: 'static', addresses: ['192.168.20.1/24'] },
    ipv6: { mode: 'delegated', from: 'seg_wan', subnetId: '2', ra: { mode: 'managed' } },
  },
};

const wanSeg = {
  id: 'seg_wan',
  name: 'wan',
  link: 'lnk_wan',
  zone: 'zon_wan',
  addressing: {
    ipv4: { mode: 'dhcp' },
    ipv6: { mode: 'dhcp6', requestPrefixLength: 56 },
  },
};

const existingDhcp4: Dhcp4Entry = {
  id: 'dhp_lan',
  name: 'lan',
  segment: 'seg_lan',
  ranges: [{ first: '192.168.10.100', last: '192.168.10.200' }],
  leaseTime: 7200,
  ntpServers: ['192.168.10.123'],
  staticLeases: [{ mac: '02:00:00:00:00:10', address: '192.168.10.10', hostname: 'nas' }],
};

const existingDhcp6: Dhcp6Entry = {
  id: 'dh6_lan',
  name: 'lan6',
  segment: 'seg_lan',
  ranges: [{ first: 'fd00:10::1000', last: 'fd00:10::1fff' }],
  leaseTime: 7200,
  prefixDelegation: { prefix: 'fd00:beef:1::/48', delegatedLength: 56 },
};

const sampleSnapshot: Snapshot = {
  state: 'saved',
  savedVersion: '12',
  value: {
    links: [],
    segments: [wanSeg, lanSeg, guestSeg],
    zones: [
      { id: 'zon_wan', name: 'wan' },
      { id: 'zon_lan', name: 'lan' },
      { id: 'zon_guest', name: 'guest' },
    ],
    dhcp: [existingDhcp4],
    dhcp6: [existingDhcp6],
    dns: { mode: 'recursive', segments: ['seg_lan'], localDomain: 'home.arpa' },
  },
};

const sampleLeases: LeaseView = {
  v4: [
    {
      segment: 'seg_lan',
      address: '192.168.10.10',
      mac: '02:00:00:00:00:10',
      hostname: 'nas',
      state: 'active',
      cltt: '2026-06-01T12:00:00Z',
      expiresAt: '2026-06-01T14:00:00Z',
    },
    {
      segment: 'seg_lan',
      address: '192.168.10.150',
      mac: 'bc:24:11:26:08:ca',
      hostname: 'laptop.home.arpa',
      state: 'active',
      cltt: '2026-06-01T12:30:00Z',
      expiresAt: '2026-06-01T14:30:00Z',
    },
    {
      segment: 'seg_lan',
      address: '192.168.10.151',
      mac: 'bc:24:11:26:08:cb',
      hostname: 'oldbox',
      state: 'expired',
      cltt: '2026-06-01T08:00:00Z',
      expiresAt: '2026-06-01T10:00:00Z',
    },
  ],
  v6: [
    {
      segment: 'seg_lan',
      type: 'address',
      address: 'fd00:10::1005',
      duid: '00:01:00:01:aa:bb:cc:dd:ee:01',
      mac: 'bc:24:11:26:08:ca',
      hostname: 'laptop',
      state: 'active',
      cltt: '2026-06-01T12:30:00Z',
      expiresAt: '2026-06-01T14:30:00Z',
    },
    {
      segment: 'seg_lan',
      type: 'prefix',
      address: 'fd00:beef:1:100::/56',
      duid: '00:01:00:01:aa:bb:cc:dd:ee:02',
      hostname: 'subrouter',
      state: 'active',
      cltt: '2026-06-01T12:30:00Z',
      expiresAt: '2026-06-01T14:30:00Z',
    },
  ],
};

function renderDhcp(caps = fullCaps, onSaved = vi.fn()) {
  const api = new ApiClient('');
  api.setCSRFToken('csrf');
  return render(
    <MemoryRouter>
      <DhcpScreen api={api} session={session(caps)} onSaved={onSaved} onSessionLost={vi.fn()} />
    </MemoryRouter>,
  );
}

describe('DhcpScreen', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date('2026-06-01T13:00:00Z'));
    setTimeZone('UTC');
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it('lists eligible IPv4 and IPv6 segments, status, options, and RA warning', async () => {
    fetchRoutes({
      '/api/v1/config': sampleSnapshot,
      '/api/v1/status/leases': sampleLeases,
    });
    renderDhcp();
    expect(await screen.findByText('DHCPv4 servers')).toBeInTheDocument();
    expect(screen.getByText('DHCPv6 servers')).toBeInTheDocument();
    expect(screen.getByText('192.168.10.0/24')).toBeInTheDocument();
    expect(screen.getByText('192.168.20.0/24')).toBeInTheDocument();
    expect(screen.getByText(/NTP: 192\.168\.10\.123 · 1 static/)).toBeInTheDocument();
    expect(screen.getByText(/RA mode is unmanaged/)).toBeInTheDocument();
    expect(screen.getByText('Delegated · subnet 2')).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Leases (4)' })).toBeInTheDocument();
  });

  it('enables DHCPv4 on an unconfigured segment with suggested upper-half range and extra options', async () => {
    let posted: Record<string, unknown> | undefined;
    fetchRoutes({
      '/api/v1/config': sampleSnapshot,
      '/api/v1/status/leases': sampleLeases,
      'POST /api/v1/config/dhcp': (init) => {
        posted = JSON.parse(String(init?.body)) as Record<string, unknown>;
        return jsonResponse(201, { value: { id: 'dhp_guest', ...posted }, version: '13' });
      },
    });
    const onSaved = vi.fn();
    renderDhcp(fullCaps, onSaved);

    fireEvent.click(await screen.findByRole('button', { name: 'Enable DHCPv4 on guest' }));
    const drawer = await screen.findByRole('dialog', { name: 'DHCPv4: guest' });
    // Suggested upper-half range for 192.168.20.1/24 is 192.168.20.128 - 192.168.20.254.
    expect(within(drawer).getByLabelText('Range 1 first address')).toHaveValue('192.168.20.128');
    expect(within(drawer).getByLabelText('Range 1 last address')).toHaveValue('192.168.20.254');

    fireEvent.change(within(drawer).getByLabelText('Gateway override'), { target: { value: '192.168.20.2' } });
    fireEvent.change(within(drawer).getByLabelText('NTP servers'), { target: { value: '192.168.20.123' } });
    fireEvent.change(within(drawer).getByLabelText('Next server (TFTP / PXE)'), { target: { value: '192.168.20.10' } });
    fireEvent.change(within(drawer).getByLabelText('Boot file name'), { target: { value: 'pxelinux.0' } });
    fireEvent.click(within(drawer).getByRole('button', { name: 'Add option' }));
    fireEvent.change(within(drawer).getByLabelText('Code'), { target: { value: '252' } });
    fireEvent.change(within(drawer).getByLabelText('Value'), { target: { value: 'http://wpad.guest/wpad.dat' } });
    fireEvent.click(within(drawer).getByRole('checkbox', { name: /Always send/ }));

    fireEvent.click(within(drawer).getByRole('button', { name: 'Enable DHCPv4' }));
    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(posted).toMatchObject({
      name: 'guest',
      segment: 'seg_guest',
      ranges: [{ first: '192.168.20.128', last: '192.168.20.254' }],
      leaseTime: 7200,
      gateway: '192.168.20.2',
      ntpServers: ['192.168.20.123'],
      nextServer: '192.168.20.10',
      bootFileName: 'pxelinux.0',
      options: [{ code: 252, type: 'string', data: 'http://wpad.guest/wpad.dat', alwaysSend: true }],
    });
  });

  it('edits and disables an existing DHCPv4 server with If-Match confirmation', async () => {
    let deletedHeaders: HeadersInit | undefined;
    fetchRoutes({
      '/api/v1/config/dhcp/dhp_lan': { value: existingDhcp4, version: '9' },
      '/api/v1/config': sampleSnapshot,
      '/api/v1/status/leases': sampleLeases,
      'DELETE /api/v1/config/dhcp/dhp_lan': (init) => {
        deletedHeaders = init?.headers;
        return new Response(null, { status: 204 });
      },
    });
    const onSaved = vi.fn();
    renderDhcp(fullCaps, onSaved);

    fireEvent.click(await screen.findByRole('button', { name: 'Edit DHCPv4 on lan' }));
    const drawer = await screen.findByRole('dialog', { name: 'DHCPv4: lan' });
    fireEvent.click(within(drawer).getByRole('button', { name: 'Disable server…' }));
    expect(within(drawer).getByText(/Disable DHCPv4 on lan\?/)).toBeInTheDocument();
    fireEvent.click(within(drawer).getByRole('button', { name: 'Disable server' }));

    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(new Headers(deletedHeaders).get('If-Match')).toBe('"dhp_lan.9"');
  });

  it('maps engine 422 validation errors onto drawer fields', async () => {
    fetchRoutes({
      '/api/v1/config/dhcp/dhp_lan': { value: existingDhcp4, version: '9' },
      '/api/v1/config': sampleSnapshot,
      '/api/v1/status/leases': sampleLeases,
      'PUT /api/v1/config/dhcp/dhp_lan': () => jsonResponse(422, {
        error: {
          code: 'VALIDATION_FAILED',
          message: 'Configuration validation failed.',
          details: [{ path: '/dhcp/0/gateway', message: 'The gateway address is inside a dynamic range.' }],
        },
      }),
    });
    renderDhcp();

    fireEvent.click(await screen.findByRole('button', { name: 'Edit DHCPv4 on lan' }));
    const drawer = await screen.findByRole('dialog', { name: 'DHCPv4: lan' });
    fireEvent.change(within(drawer).getByLabelText('Gateway override'), { target: { value: '192.168.10.150' } });
    fireEvent.click(within(drawer).getByRole('button', { name: 'Save' }));

    expect(await within(drawer).findByText('The gateway address is inside a dynamic range.')).toBeInTheDocument();
  });

  it('filters live leases and promotes a dynamic IPv4 lease to static', async () => {
    let putBody: Record<string, unknown> | undefined;
    let putHeaders: HeadersInit | undefined;
    fetchRoutes({
      '/api/v1/config/dhcp/dhp_lan': { value: existingDhcp4, version: '9' },
      '/api/v1/config': sampleSnapshot,
      '/api/v1/status/leases': sampleLeases,
      'PUT /api/v1/config/dhcp/dhp_lan': (init) => {
        putHeaders = init?.headers;
        putBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
        return jsonResponse(200, { value: { id: 'dhp_lan', ...putBody }, version: '10' });
      },
    });
    const onSaved = vi.fn();
    renderDhcp(fullCaps, onSaved);

    fireEvent.click(await screen.findByRole('tab', { name: 'Leases (4)' }));
    // Expired lease hidden by default; active leases shown.
    expect(screen.getByText('laptop.home.arpa')).toBeInTheDocument();
    expect(screen.queryByText('oldbox')).not.toBeInTheDocument();
    // Already-static lease shows Static badge and no Make static button.
    expect(screen.queryByRole('button', { name: 'Make 192.168.10.10 static' })).not.toBeInTheDocument();

    // Toggling expired switch shows oldbox.
    fireEvent.click(screen.getByLabelText(/Show expired & released/));
    expect(screen.getByText('oldbox')).toBeInTheDocument();

    // Promote dynamic laptop lease to static.
    fireEvent.click(screen.getByRole('button', { name: 'Make 192.168.10.150 static' }));
    const modal = await screen.findByRole('dialog', { name: 'Make static lease on lan' });
    expect(within(modal).getByLabelText(/MAC address/)).toHaveValue('bc:24:11:26:08:ca');
    expect(within(modal).getByLabelText(/IPv4 address/)).toHaveValue('192.168.10.150');
    expect(within(modal).getByLabelText(/Hostname/)).toHaveValue('laptop');

    fireEvent.click(within(modal).getByRole('button', { name: 'Save static lease' }));
    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(new Headers(putHeaders).get('If-Match')).toBe('"dhp_lan.9"');
    expect(putBody?.staticLeases).toEqual([
      { mac: '02:00:00:00:00:10', address: '192.168.10.10', hostname: 'nas' },
      { mac: 'bc:24:11:26:08:ca', address: '192.168.10.150', hostname: 'laptop' },
    ]);
  });

  it('enables DHCPv6 on a delegated segment with prefix delegation, SNTP, and custom option', async () => {
    let posted: Record<string, unknown> | undefined;
    fetchRoutes({
      '/api/v1/config': sampleSnapshot,
      '/api/v1/status/leases': sampleLeases,
      'POST /api/v1/config/dhcp6': (init) => {
        posted = JSON.parse(String(init?.body)) as Record<string, unknown>;
        return jsonResponse(201, { value: { id: 'dh6_guest', ...posted }, version: '13' });
      },
    });
    const onSaved = vi.fn();
    renderDhcp(fullCaps, onSaved);

    fireEvent.click(await screen.findByRole('button', { name: 'Enable DHCPv6 on guest' }));
    const drawer = await screen.findByRole('dialog', { name: 'DHCPv6: guest' });
    expect(within(drawer).getByLabelText('Range 1 first address')).toHaveValue('::1000');
    expect(within(drawer).getByLabelText('Range 1 last address')).toHaveValue('::1fff');

    fireEvent.change(within(drawer).getByLabelText('SNTP servers'), { target: { value: 'fd00:20::123' } });
    fireEvent.click(within(drawer).getByLabelText('Delegate IPv6 prefixes to downstream routers'));
    fireEvent.change(within(drawer).getByLabelText(/Subnet ID \(hex\)/), { target: { value: '8' } });
    fireEvent.change(within(drawer).getByLabelText(/Pool length/), { target: { value: '60' } });
    fireEvent.click(within(drawer).getByRole('button', { name: 'Add option' }));
    fireEvent.change(within(drawer).getByLabelText('Code'), { target: { value: '200' } });
    fireEvent.change(within(drawer).getByLabelText('Type'), { target: { value: 'boolean' } });

    fireEvent.click(within(drawer).getByRole('button', { name: 'Enable DHCPv6' }));
    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(posted).toMatchObject({
      name: 'guest6',
      segment: 'seg_guest',
      ranges: [{ first: '::1000', last: '::1fff' }],
      leaseTime: 7200,
      ntpServers: ['fd00:20::123'],
      prefixDelegation: { subnetId: '8', length: 60, delegatedLength: 64 },
      options: [{ code: 200, type: 'boolean', data: 'true' }],
    });
  });

  it('shows read-only leases view for status.read-only accounts without write actions', async () => {
    fetchRoutes({
      '/api/v1/status/leases': sampleLeases,
    });
    renderDhcp(['status.read']);
    expect(await screen.findByText('laptop.home.arpa')).toBeInTheDocument();
    expect(screen.queryByRole('tab', { name: 'Servers' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Make .* static/ })).not.toBeInTheDocument();
  });

  it('prunes blank custom-option rows on submit so server 422 indices align and disclosure opens', async () => {
    fetchRoutes({
      '/api/v1/config/dhcp/dhp_lan': { value: existingDhcp4, version: '9' },
      '/api/v1/config': sampleSnapshot,
      '/api/v1/status/leases': sampleLeases,
      'PUT /api/v1/config/dhcp/dhp_lan': () => jsonResponse(422, {
        error: {
          code: 'VALIDATION_FAILED',
          message: 'Configuration validation failed.',
          details: [{ path: '/dhcp/0/options/0/code', message: 'Option code 3 is reserved.' }],
        },
      }),
    });
    renderDhcp();

    fireEvent.click(await screen.findByRole('button', { name: 'Edit DHCPv4 on lan' }));
    const drawer = await screen.findByRole('dialog', { name: 'DHCPv4: lan' });
    // Add a blank option row first, then a populated option row with reserved code 3.
    fireEvent.click(within(drawer).getByRole('button', { name: 'Add option' }));
    fireEvent.click(within(drawer).getByRole('button', { name: 'Add option' }));
    const codeInputs = within(drawer).getAllByLabelText('Code');
    const valueInputs = within(drawer).getAllByLabelText('Value');
    fireEvent.change(codeInputs[1], { target: { value: '3' } });
    fireEvent.change(valueInputs[1], { target: { value: 'custom' } });

    fireEvent.click(within(drawer).getByRole('button', { name: 'Save' }));
    expect(await within(drawer).findByText('Option code 3 is reserved.')).toBeInTheDocument();
    // Blank row was pruned so the remaining row holds code "3" with the server error attached.
    expect(within(drawer).getAllByLabelText('Code')).toHaveLength(1);
    expect(within(drawer).getByLabelText('Code')).toHaveValue('3');
  });

  it('merges a DHCPv6 prefix lease into an existing static lease sharing the same DUID', async () => {
    const sharedDuid = '00:01:00:01:aa:bb:cc:dd:ee:02';
    const dhcp6WithAddr: Dhcp6Entry = {
      ...existingDhcp6,
      staticLeases: [{ duid: sharedDuid, address: 'fd00:10::50', hostname: 'subrouter' }],
    };
    const snap: Snapshot = {
      ...sampleSnapshot,
      value: { ...sampleSnapshot.value, dhcp6: [dhcp6WithAddr] },
    };
    const leases: LeaseView = {
      v4: [],
      v6: [
        {
          segment: 'seg_lan',
          type: 'address',
          address: 'fd00:10::50',
          duid: sharedDuid,
          hostname: 'subrouter',
          state: 'active',
          cltt: '2026-06-01T12:30:00Z',
          expiresAt: '2026-06-01T14:30:00Z',
        },
        {
          segment: 'seg_lan',
          type: 'prefix',
          address: 'fd00:beef:1:100::/56',
          duid: sharedDuid,
          hostname: 'subrouter',
          state: 'active',
          cltt: '2026-06-01T12:30:00Z',
          expiresAt: '2026-06-01T14:30:00Z',
        },
      ],
    };
    let putBody: Record<string, unknown> | undefined;
    fetchRoutes({
      '/api/v1/config/dhcp6/dh6_lan': { value: dhcp6WithAddr, version: '4' },
      '/api/v1/config': snap,
      '/api/v1/status/leases': leases,
      'PUT /api/v1/config/dhcp6/dh6_lan': (init) => {
        putBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
        return jsonResponse(200, { value: { id: 'dh6_lan', ...putBody }, version: '5' });
      },
    });
    const onSaved = vi.fn();
    renderDhcp(fullCaps, onSaved);

    fireEvent.click(await screen.findByRole('tab', { name: 'Leases (2)' }));
    // Address lease is already static, but prefix lease with the same DUID still offers Make static.
    expect(screen.queryByRole('button', { name: 'Make fd00:10::50 static' })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Make fd00:beef:1:100::/56 static' }));
    const modal = await screen.findByRole('dialog', { name: 'Make static lease on lan' });
    fireEvent.click(within(modal).getByRole('button', { name: 'Save static lease' }));

    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(putBody?.staticLeases).toEqual([
      { duid: sharedDuid, address: 'fd00:10::50', prefix: 'fd00:beef:1:100::/56', hostname: 'subrouter' },
    ]);
  });

  it('recovers when a selected segment filter vanishes on reload', async () => {
    let call = 0;
    const twoSegLeases: LeaseView = {
      v4: [
        sampleLeases.v4[1],
        {
          segment: 'seg_guest',
          address: '192.168.20.150',
          mac: 'bc:24:11:26:08:cc',
          hostname: 'guestphone',
          state: 'active',
          cltt: '2026-06-01T12:30:00Z',
          expiresAt: '2026-06-01T14:30:00Z',
        },
      ],
      v6: [],
    };
    const oneSegLeases: LeaseView = { v4: [sampleLeases.v4[1]], v6: [] };
    fetchRoutes({
      '/api/v1/config': sampleSnapshot,
      '/api/v1/status/leases': () => {
        call += 1;
        return jsonResponse(200, call === 1 ? twoSegLeases : oneSegLeases);
      },
    });
    renderDhcp();

    fireEvent.click(await screen.findByRole('tab', { name: 'Leases (2)' }));
    fireEvent.change(screen.getByLabelText('Segment'), { target: { value: 'seg_guest' } });
    expect(screen.getByText('guestphone')).toBeInTheDocument();
    expect(screen.queryByText('laptop.home.arpa')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }));
    expect(await screen.findByText('laptop.home.arpa')).toBeInTheDocument();
  });
});

describe('dhcpEdit helpers', () => {
  it('avoids secondary own IPv4 addresses when suggesting a dynamic range', () => {
    const dualIpSeg = {
      ...lanSeg,
      addressing: {
        ...lanSeg.addressing,
        ipv4: { mode: 'static' as const, addresses: ['192.168.10.1/24', '192.168.10.200/24'] },
      },
    };
    expect(suggestedIpv4Range(dualIpSeg)).toEqual({ first: '192.168.10.2', last: '192.168.10.127' });
  });

  it('rejects non-decimal option codes and preserves spaces in string option data', () => {
    const draft = {
      name: 'lan',
      leaseTime: '7200',
      ranges: [{ key: newRowKey(), first: '192.168.10.100', last: '192.168.10.200' }],
      gateway: '',
      dnsServers: '',
      domainName: '',
      domainSearch: '',
      ntpServers: '',
      nextServer: '',
      bootFileName: '',
      options: [
        { key: newRowKey(), code: '0x10', type: 'string' as const, data: ' padded ', alwaysSend: false },
      ],
      staticLeases: [],
    };
    expect(checkDhcp4(draft).get('options/0/code')).toBe('Code must be 1 to 254.');
    draft.options[0].code = '252';
    expect(checkDhcp4(draft).size).toBe(0);
    expect(dhcp4Payload(draft, 'seg_lan').options).toEqual([{ code: 252, type: 'string', data: ' padded ' }]);
  });

  it('matches static leases by client identity and IPv6 lease kind', () => {
    expect(leaseMatchesStatic4(
      { ...sampleLeases.v4[0], mac: 'aa:bb:cc:dd:ee:ff' },
      sampleSnapshot,
    )).toBe(false);
    const snapWithAddrOnly: Snapshot = {
      ...sampleSnapshot,
      value: {
        ...sampleSnapshot.value,
        dhcp6: [{ ...existingDhcp6, staticLeases: [{ duid: '00:01:00:01:aa:bb:cc:dd:ee:02', address: 'fd00:10::50' }] }],
      },
    };
    expect(leaseMatchesStatic6(sampleLeases.v6[1], snapWithAddrOnly)).toBe(false);
  });

  it('extracts IPv6 interface suffixes strictly', () => {
    expect(ipv6SuffixOf('2001:db8:1:2::1005/128')).toBe('::1005');
    expect(ipv6SuffixOf('2001:db8:1:2::%eth0')).toBe('::');
    expect(ipv6SuffixOf('2001:db8::192.0.2.1')).toBe('');
    expect(ipv6SuffixOf('2001::db8::1')).toBe('');
  });
});
