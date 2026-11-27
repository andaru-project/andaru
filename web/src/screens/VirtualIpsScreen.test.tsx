import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiClient } from '../api';
import { fetchRoutes, jsonResponse } from '../test/fetch';
import { session } from '../test/fixtures';
import { render } from '../test/render';
import type { SavedObject, Snapshot, VirtualAddress } from '../types';
import { checkVirtualIp, isProxyArpEligibleSegment, isSingleVirtualAddress, maxVirtualAddresses, virtualAddressFamily, virtualAddressKind, virtualAddressUses } from '../virtualIps';
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
  it('classifies IPv4 and IPv6 addresses and blocks, rejects IPv4-mapped forms, and detects canonical IPv6 duplicates', () => {
    expect(virtualAddressFamily({ address: '172.16.53.53' })).toBe('IPv4');
    expect(virtualAddressFamily({ address: '172.16.53.53/32' })).toBe('IPv4');
    expect(virtualAddressFamily({ address: 'fd00:53::53' })).toBe('IPv6');
    expect(virtualAddressFamily({ address: 'fe80::1%eth0' })).toBeUndefined();
    expect(virtualAddressFamily({ address: '010.0.0.1' })).toBeUndefined();
    expect(virtualAddressFamily({ address: '::ffff:1.2.3.4' })).toBeUndefined();
    expect(virtualAddressFamily({ address: '::ffff:102:304' })).toBeUndefined();
    expect(virtualAddressFamily({ address: '0:0:0:0:0:ffff:c000:280' })).toBeUndefined();

    expect(checkVirtualIp('Dup v6', 'FD00:0053:0:0:0:0:0:0053', fixtureVips).errors.get('address')).toMatch(/already has this address/);
    expect(checkVirtualIp('Dup v6 /128', 'FD00:0053::0053/128', fixtureVips).errors.get('address')).toMatch(/already has this address/);
    expect(checkVirtualIp('Distinct v4', '172.16.53.54', fixtureVips).errors.size).toBe(0);
  });

  it('rejects non-unicast, loopback, link-local, multicast, broadcast, unspecified, IPv4-mapped, and zone-scoped inputs', () => {
    for (const bad of ['0.0.0.0', '0.0.0.0/24', '127.0.0.1', '127.0.0.0/24', '169.254.1.1', '169.254.1.0/24', '224.0.0.1', '224.0.0.0/24', '255.255.255.255', '255.255.255.0/24', '::', '::/64', '::1', '::1/128', 'fe80::1', 'fe80::/64', 'ff02::1', 'ff02::/64', '::ffff:102:304', '::ffff:0:0/96', '0:0:0:0:0:fffe::/95', '::8000:0:0/81']) {
      expect(checkVirtualIp('test', bad, []).errors.get('address')).toMatch(/unicast/);
    }
    expect(checkVirtualIp('test', 'fe80::1%eth0', []).errors.get('address')).toMatch(/interface zone/);
    expect(checkVirtualIp('test', '198.51.100.0/23', []).errors.get('address')).toMatch(/\/24 and \/32/);
    expect(checkVirtualIp('test', '2001:db8:53::/47', []).errors.get('address')).toMatch(/\/48 and \/128/);
    expect(checkVirtualIp('test', '198.51.100.1/29', []).errors.get('address')).toMatch(/host bits must be zero/);
    expect(checkVirtualIp('test', '2001:db8:53::1/64', []).errors.get('address')).toMatch(/host bits must be zero/);
    expect(checkVirtualIp('', '', []).errors.get('name')).toMatch(/needs a name/);
    expect(checkVirtualIp('', '', []).errors.get('address')).toMatch(/needs an address/);
  });

  it('validates held routed blocks, /32 and /128 single addresses, segment subnet overlap, and block-targeted uses', () => {
    for (const valid of ['172.16.53.53', '172.16.53.53/32', '198.51.100.0/29', '198.51.100.0/24', 'fd00:53::53', 'fd00:53::53/128', '2001:db8:53::/64', '2001:db8:53::/48']) {
      expect(checkVirtualIp('Held ok', valid, []).errors.size).toBe(0);
    }

    expect(virtualAddressFamily({ address: '198.51.100.0/29' })).toBe('IPv4');
    expect(virtualAddressFamily({ address: '2001:db8:53::/64' })).toBe('IPv6');
    expect(isSingleVirtualAddress({ address: '172.16.53.53' })).toBe(true);
    expect(isSingleVirtualAddress({ address: '172.16.53.53/32' })).toBe(true);
    expect(isSingleVirtualAddress({ address: 'fd00:53::53' })).toBe(true);
    expect(isSingleVirtualAddress({ address: 'fd00:53::53/128' })).toBe(true);
    expect(isSingleVirtualAddress({ address: '198.51.100.0/29' })).toBe(false);
    expect(isSingleVirtualAddress({ address: '2001:db8:53::/64' })).toBe(false);
    expect(isSingleVirtualAddress({ kind: 'proxyArp', address: '203.0.113.50' })).toBe(false);

    const lanSeg = {
      id: 'seg_lan',
      name: 'LAN',
      link: 'lnk_1',
      zone: 'zon_lan',
      addressing: {
        ipv4: { mode: 'static' as const, addresses: ['192.168.1.1/24'] },
        ipv6: { mode: 'static' as const, addresses: ['2001:db8:1::1/64'] },
      },
    };
    const host32Seg = {
      id: 'seg_host32',
      name: 'Host32',
      link: 'lnk_2',
      zone: 'zon_lan',
      addressing: {
        ipv4: { mode: 'static' as const, addresses: ['198.51.100.10/32'] },
        ipv6: { mode: 'delegated' as const, addresses: ['2001:db8:2::1/64'] },
      },
    };
    expect(checkVirtualIp('On v4 subnet', '192.168.1.0/29', [], undefined, { kind: 'address', segments: [lanSeg] }).errors.get('address')).toMatch(/segment's subnet/);
    expect(checkVirtualIp('On v6 subnet', '2001:db8:1::/64', [], undefined, { kind: 'address', segments: [lanSeg] }).errors.get('address')).toMatch(/segment's subnet/);
    expect(checkVirtualIp('Covers /32 static', '198.51.100.8/29', [], undefined, { kind: 'address', segments: [host32Seg] }).errors.get('address')).toMatch(/segment's subnet/);
    expect(checkVirtualIp('Overlaps delegated v6', '2001:db8:2::/64', [], undefined, { kind: 'address', segments: [host32Seg] }).errors.get('address')).toMatch(/segment's subnet/);

    const existingBlocks: Array<SavedObject<VirtualAddress>> = [
      { value: { id: 'vip_b4', name: 'Routed v4', address: '198.51.100.0/29' }, version: '1' },
      { value: { id: 'vip_b6', name: 'Routed v6', address: '2001:db8:53::/64' }, version: '1' },
    ];
    expect(checkVirtualIp('Dup v4 inside block', '198.51.100.5', existingBlocks).errors.get('address')).toMatch(/overlaps another virtual IP/);
    expect(checkVirtualIp('Overlap v4 block', '198.51.100.0/28', existingBlocks).errors.get('address')).toMatch(/overlaps another virtual IP/);
    expect(checkVirtualIp('Dup v6 inside block', '2001:db8:53::42', existingBlocks).errors.get('address')).toMatch(/overlaps another virtual IP/);
    expect(checkVirtualIp('Dup v4 /32', '172.16.53.53/32', fixtureVips).errors.get('address')).toMatch(/already has this address/);
    expect(checkVirtualIp('Proxy over held block', '198.51.100.4/30', existingBlocks, undefined, { kind: 'proxyArp', segment: 'seg_x' }).errors.get('address')).toMatch(/overlaps an existing held virtual IP/);

    const snapshotWithBlockPf: Snapshot = {
      ...sampleSnapshot,
      value: {
        ...sampleSnapshot.value,
        nat: {
          portForwards: [
            {
              id: 'pfw_blk',
              name: 'Block service',
              enabled: true,
              family: 'ipv4',
              ingressZones: ['zon_wan'],
              protocol: 'tcp',
              from: [],
              to: { self: true },
              ports: '443',
              target: { address: '198.51.100.5' },
            },
          ],
        },
      },
    };
    const uses = virtualAddressUses(existingBlocks[0].value, snapshotWithBlockPf);
    expect(uses).toEqual([
      { key: 'pfw:pfw_blk', label: 'Port forward 1 (Block service)', to: '/firewall/nat', locksAddress: true },
    ]);
  });

  it('validates proxyArp entries, segment eligibility, IPv6 refusal, prefixes, gateways, and cross-kind overlaps', () => {
    const wanStatic = {
      id: 'seg_wan',
      name: 'WAN',
      link: 'lnk_wan',
      zone: 'zon_wan',
      addressing: { ipv4: { mode: 'static', addresses: ['203.0.113.2/24'], gateway: '203.0.113.1' }, ipv6: { mode: 'none' } },
    };
    const wanHostOnly = {
      id: 'seg_host32',
      name: 'WAN /32',
      link: 'lnk_host32',
      zone: 'zon_wan',
      addressing: { ipv4: { mode: 'static', addresses: ['198.51.100.10/32'] }, ipv6: { mode: 'none' } },
    };
    const wanDhcp = {
      id: 'seg_dhcp',
      name: 'WAN DHCP',
      link: 'lnk_dhcp',
      zone: 'zon_wan',
      addressing: { ipv4: { mode: 'dhcp' }, ipv6: { mode: 'none' } },
    };
    const wanPppoe = {
      id: 'seg_pppoe',
      name: 'WAN PPPoE',
      link: 'lnk_pppoe',
      zone: 'zon_wan',
      addressing: { ipv4: { mode: 'pppoe' }, ipv6: { mode: 'none' } },
    };
    const segments = [wanStatic, wanHostOnly, wanDhcp, wanPppoe];

    expect(isProxyArpEligibleSegment(wanStatic)).toBe(true);
    expect(isProxyArpEligibleSegment(wanHostOnly)).toBe(false);
    expect(isProxyArpEligibleSegment(wanDhcp)).toBe(false);
    expect(isProxyArpEligibleSegment(wanPppoe)).toBe(false);
    expect(virtualAddressKind({ kind: 'proxyArp' })).toBe('proxyArp');
    expect(virtualAddressKind({})).toBe('address');
    expect(virtualAddressFamily({ kind: 'proxyArp', address: '203.0.113.56/29' })).toBe('IPv4');
    expect(virtualAddressFamily({ kind: 'proxyArp', address: '203.0.113.50' })).toBe('IPv4');

    // Missing, DHCP, or /32-only segment refused at segment.
    expect(checkVirtualIp('Proxy 1', '203.0.113.50', [], undefined, { kind: 'proxyArp', segment: '', segments }).errors.get('segment')).toMatch(/static IPv4/);
    const dhcpRefusal = checkVirtualIp('Proxy 1', '203.0.113.50', [], undefined, { kind: 'proxyArp', segment: 'seg_dhcp', segments });
    expect(dhcpRefusal.errors.get('segment')).toMatch(/static IPv4/);
    expect(dhcpRefusal.stale).toBe(true);
    expect(checkVirtualIp('Proxy 1', '198.51.100.10', [], undefined, { kind: 'proxyArp', segment: 'seg_host32', segments }).errors.get('segment')).toMatch(/static IPv4/);

    // Pure IPv6 refused pointing at Proxy NDP; 4in6, internal whitespace, and non-unicast IPv4 rejected as invalid IPv4.
    for (const v6 of ['2001:db8::50', '2001:DB8::/64']) {
      expect(checkVirtualIp('Proxy v6', v6, [], undefined, { kind: 'proxyArp', segment: 'seg_wan', segments }).errors.get('address'))
        .toBe('Proxy ARP is IPv4 only; use Proxy NDP for IPv6.');
    }
    for (const badV4 of ['::ffff:203.0.113.50', '::ffff:203.0.113.50/128', '::ffff:cb00:7132/128', '203.0.113.56 /29', '203.0.113.56/029', '127.0.0.1', '169.254.1.1', '224.0.0.1']) {
      expect(checkVirtualIp('Bad v4', badV4, [], undefined, { kind: 'proxyArp', segment: 'seg_wan', segments }).errors.get('address'))
        .toMatch(/unicast IPv4/);
    }

    // Prefix bounds, host bits, subnet membership, static address, and gateway collisions.
    expect(checkVirtualIp('Zero len', '0.0.0.0/0', [], undefined, { kind: 'proxyArp', segment: 'seg_wan', segments }).errors.get('address')).toMatch(/unicast IPv4/);
    expect(checkVirtualIp('Too wide', '203.0.112.0/23', [], undefined, { kind: 'proxyArp', segment: 'seg_wan', segments }).errors.get('address')).toMatch(/\/24 and \/32/);
    expect(checkVirtualIp('Host bits', '203.0.113.57/29', [], undefined, { kind: 'proxyArp', segment: 'seg_wan', segments }).errors.get('address')).toMatch(/host bits must be zero/);
    expect(checkVirtualIp('Wrong net', '198.51.100.50', [], undefined, { kind: 'proxyArp', segment: 'seg_wan', segments }).errors.get('address')).toMatch(/static IPv4 subnets/);
    expect(checkVirtualIp('Hits static', '203.0.113.0/29', [], undefined, { kind: 'proxyArp', segment: 'seg_wan', segments }).errors.get('address')).toMatch(/static address/);
    expect(checkVirtualIp('Hits gateway', '203.0.113.1', [], undefined, { kind: 'proxyArp', segment: 'seg_wan', segments }).errors.get('address')).toMatch(/gateway/);

    // Valid single IPv4, explicit /32, /31, and /29 prefixes.
    for (const valid of ['203.0.113.50', '203.0.113.50/32', '203.0.113.52/31', '203.0.113.56/29']) {
      expect(checkVirtualIp('Proxy ok', valid, [], undefined, { kind: 'proxyArp', segment: 'seg_wan', segments }).errors.size).toBe(0);
    }

    // Cross-entry overlap checks (both directions) and self-exclusion when editing.
    const existing: Array<SavedObject<VirtualAddress>> = [
      { value: { id: 'vip_p1', name: 'Block 1', kind: 'proxyArp', segment: 'seg_wan', address: '203.0.113.56/29' }, version: '1' },
      { value: { id: 'vip_h1', name: 'Held 1', address: '203.0.113.99' }, version: '1' },
    ];
    expect(checkVirtualIp('Block 1', '203.0.113.56/29', existing, 'vip_p1', { kind: 'proxyArp', segment: 'seg_wan', segments }).errors.size).toBe(0);

    const overlapProxy = checkVirtualIp('Overlap', '203.0.113.60', existing, undefined, { kind: 'proxyArp', segment: 'seg_wan', segments });
    expect(overlapProxy.errors.get('address')).toMatch(/overlaps another proxy ARP/);
    expect(overlapProxy.duplicate).toBe(true);

    const includesHeld = checkVirtualIp('Covers held', '203.0.113.96/29', existing, undefined, { kind: 'proxyArp', segment: 'seg_wan', segments });
    expect(includesHeld.errors.get('address')).toMatch(/existing held virtual IP/);
    expect(includesHeld.duplicate).toBe(true);

    const heldInsideProxy = checkVirtualIp('Held inside', '203.0.113.58', existing, undefined, { kind: 'address' });
    expect(heldInsideProxy.errors.get('address')).toMatch(/proxy ARP/);
    expect(heldInsideProxy.duplicate).toBe(true);
  });

  it('validates proxyNdp entries, global unicast IPv6, segment rules, and cross-kind overlaps', () => {
    const wanStatic6 = {
      id: 'seg_wan6',
      name: 'WAN',
      link: 'lnk_wan',
      zone: 'zon_wan',
      addressing: { ipv4: { mode: 'none' }, ipv6: { mode: 'static', addresses: ['2001:db8:1::2/64'] } },
    };
    const wanSlaac = {
      id: 'seg_slaac',
      name: 'WAN SLAAC',
      link: 'lnk_slaac',
      zone: 'zon_wan',
      addressing: { ipv4: { mode: 'none' }, ipv6: { mode: 'slaac' } },
    };
    const lan6 = {
      id: 'seg_lan',
      name: 'LAN',
      link: 'lnk_lan',
      zone: 'zon_lan',
      addressing: { ipv4: { mode: 'static', addresses: ['192.168.1.1/24'] }, ipv6: { mode: 'static', addresses: ['2001:db8:2::1/64'] } },
    };
    const segments = [wanStatic6, wanSlaac, lan6];

    expect(virtualAddressKind({ kind: 'proxyNdp' })).toBe('proxyNdp');
    expect(virtualAddressFamily({ kind: 'proxyNdp', address: '2001:db8:1::50' })).toBe('IPv6');
    expect(virtualAddressFamily({ kind: 'proxyNdp', address: '2001:db8:1::50/128' })).toBeUndefined();
    expect(virtualAddressFamily({ kind: 'proxyNdp', address: '203.0.113.50' })).toBeUndefined();
    expect(isSingleVirtualAddress({ kind: 'proxyNdp', address: '2001:db8:1::50' })).toBe(false);

    // Missing or identical segments.
    const missingSegs = checkVirtualIp('Host 1', '2001:db8:1::50', [], undefined, { kind: 'proxyNdp', segment: '', hostSegment: '', segments });
    expect(missingSegs.errors.get('segment')).toMatch(/uplink segment/);
    expect(missingSegs.errors.get('hostSegment')).toMatch(/host lives/);
    expect(checkVirtualIp('Same seg', '2001:db8:1::50', [], undefined, { kind: 'proxyNdp', segment: 'seg_wan6', hostSegment: 'seg_wan6', segments }).errors.get('hostSegment'))
      .toMatch(/must differ/);

    // Invalid addresses (IPv4, prefixes, ULA, link-local, loopback, unspecified, multicast, 4in6).
    for (const bad of ['203.0.113.50', '2001:db8:1::50/128', '2001:db8:1::/64', 'fd00::50', 'fc00::50', 'fe80::50', '::1', '::', 'ff02::1', '::ffff:1.2.3.4', '::ffff:102:304']) {
      expect(checkVirtualIp('Bad ndp', bad, [], undefined, { kind: 'proxyNdp', segment: 'seg_wan6', hostSegment: 'seg_lan', segments }).errors.get('address'))
        .toBe('Expected one global unicast IPv6 address, without a length.');
    }
    expect(checkVirtualIp('Zone', '2001:db8:1::50%eth0', [], undefined, { kind: 'proxyNdp', segment: 'seg_wan6', hostSegment: 'seg_lan', segments }).errors.get('address'))
      .toMatch(/interface zone/);

    // Static uplink subnet membership, own static address, and host segment subnet collision.
    expect(checkVirtualIp('Off static uplink', '2001:db8:9::50', [], undefined, { kind: 'proxyNdp', segment: 'seg_wan6', hostSegment: 'seg_lan', segments }).errors.get('address'))
      .toMatch(/static IPv6 subnets/);
    expect(checkVirtualIp('Own static addr', '2001:db8:1::2', [], undefined, { kind: 'proxyNdp', segment: 'seg_wan6', hostSegment: 'seg_lan', segments }).errors.get('address'))
      .toMatch(/static address held by a segment/);
    expect(checkVirtualIp('On host subnet', '2001:db8:2::50', [], undefined, { kind: 'proxyNdp', segment: 'seg_slaac', hostSegment: 'seg_lan', segments }).errors.get('address'))
      .toMatch(/host segment's subnet/);

    // Valid on static uplink and on SLAAC uplink.
    expect(checkVirtualIp('Valid static', '2001:db8:1::50', [], undefined, { kind: 'proxyNdp', segment: 'seg_wan6', hostSegment: 'seg_lan', segments }).errors.size).toBe(0);
    expect(checkVirtualIp('Valid slaac', '2001:db8:9::50', [], undefined, { kind: 'proxyNdp', segment: 'seg_slaac', hostSegment: 'seg_lan', segments }).errors.size).toBe(0);

    // Overlap checks (both directions) and self-exclusion when editing.
    const existing: Array<SavedObject<VirtualAddress>> = [
      { value: { id: 'vip_n1', name: 'NDP 1', kind: 'proxyNdp', segment: 'seg_wan6', hostSegment: 'seg_lan', address: '2001:db8:1::50' }, version: '1' },
      { value: { id: 'vip_h6', name: 'Held block', address: '2001:db8:9::/64' }, version: '1' },
    ];
    expect(checkVirtualIp('NDP 1', '2001:db8:1::50', existing, 'vip_n1', { kind: 'proxyNdp', segment: 'seg_wan6', hostSegment: 'seg_lan', segments }).errors.size).toBe(0);
    expect(checkVirtualIp('Dup NDP', '2001:0db8:0001::0050', existing, undefined, { kind: 'proxyNdp', segment: 'seg_wan6', hostSegment: 'seg_lan', segments }).errors.get('address'))
      .toMatch(/Another proxy NDP entry/);
    expect(checkVirtualIp('Hits held block', '2001:db8:9::50', existing, undefined, { kind: 'proxyNdp', segment: 'seg_slaac', hostSegment: 'seg_lan', segments }).errors.get('address'))
      .toMatch(/overlaps an existing virtual IP/);
    expect(checkVirtualIp('Held over NDP', '2001:db8:1::/64', existing, undefined, { kind: 'address' }).errors.get('address'))
      .toMatch(/proxy NDP/);
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

    // Rejects non-zero host bits and duplicate name, focusing first invalid input.
    const nameInput = within(addDrawer).getByLabelText(/^Name/);
    const addrInput = within(addDrawer).getByRole('textbox', { name: /^Address or prefix/ });
    await user.type(nameInput, 'DNS v4');
    await user.type(addrInput, '198.51.100.1/29');
    await user.click(within(addDrawer).getByRole('button', { name: 'Add virtual IP' }));
    expect(await within(addDrawer).findByText('Another virtual IP has this name.')).toBeInTheDocument();
    expect(within(addDrawer).getByText('Prefix host bits must be zero.')).toBeInTheDocument();
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

  it('displays proxyArp kind, segment, and IPv4 CIDR badge in the list and filters by kind and segment', async () => {
    const user = userEvent.setup();
    const proxyVip: SavedObject<VirtualAddress> = {
      value: {
        id: 'vip_proxy',
        name: 'ISP block',
        kind: 'proxyArp',
        segment: 'seg_wan',
        address: '203.0.113.56/29',
        description: 'Extra routed block',
      },
      version: '4',
    };
    const multiSnapshot: Snapshot = {
      ...sampleSnapshot,
      value: {
        ...sampleSnapshot.value,
        segments: [
          ...sampleSnapshot.value.segments,
          { id: 'seg_wan', name: 'Uplink', link: 'lnk_1', zone: 'zon_wan', addressing: { ipv4: { mode: 'static', addresses: ['203.0.113.2/24'] }, ipv6: { mode: 'none' } } },
        ],
        virtualAddresses: [...sampleSnapshot.value.virtualAddresses!, proxyVip.value],
      },
    };
    fetchRoutes({
      '/api/v1/config/virtualAddresses': { state: 'saved', savedVersion: '9', items: [...fixtureVips, proxyVip] },
      '/api/v1/config': multiSnapshot,
    });
    renderVirtualIps();

    expect(await screen.findByText('ISP block')).toBeInTheDocument();
    const row = screen.getByText('ISP block').closest('tr')!;
    expect(within(row).getByText('Proxy ARP')).toBeInTheDocument();
    expect(within(row).getByText('203.0.113.56/29')).toBeInTheDocument();
    expect(within(row).getByText('IPv4')).toBeInTheDocument();
    expect(within(row).getByText('Uplink')).toBeInTheDocument();

    const heldRow = screen.getByText('DNS v4').closest('tr')!;
    expect(within(heldRow).getByText('Held address')).toBeInTheDocument();

    const filter = screen.getByLabelText('Filter virtual IPs');
    await user.type(filter, 'proxy arp');
    expect(screen.getByText('ISP block')).toBeInTheDocument();
    expect(screen.queryByText('DNS v4')).not.toBeInTheDocument();

    // Filter by segment name ("uplink"), which does not appear in the VIP's name or description.
    await user.clear(filter);
    await user.type(filter, 'uplink');
    expect(screen.getByText('ISP block')).toBeInTheDocument();
    expect(screen.queryByText('DNS v4')).not.toBeInTheDocument();
  });

  it('creates a proxyArp entry on a static IPv4 segment, excludes DHCP/PPPoE segments, and maps server segment errors', async () => {
    const user = userEvent.setup();
    let postedBody: Record<string, unknown> | undefined;
    let rejectSegment = false;
    const multiSnapshot: Snapshot = {
      ...sampleSnapshot,
      value: {
        ...sampleSnapshot.value,
        segments: [
          ...sampleSnapshot.value.segments,
          { id: 'seg_wan', name: 'WAN', link: 'lnk_1', zone: 'zon_wan', addressing: { ipv4: { mode: 'static', addresses: ['203.0.113.2/24'] }, ipv6: { mode: 'none' } } },
          { id: 'seg_dhcp', name: 'Cable DHCP', link: 'lnk_1', zone: 'zon_wan', addressing: { ipv4: { mode: 'dhcp' }, ipv6: { mode: 'none' } } },
        ],
      },
    };
    fetchRoutes({
      '/api/v1/config/virtualAddresses': { state: 'saved', savedVersion: '9', items: fixtureVips },
      '/api/v1/config': multiSnapshot,
      'POST /api/v1/config/virtualAddresses': (init) => {
        if (rejectSegment) {
          return jsonResponse(422, {
            error: {
              code: 'VALIDATION_FAILED',
              message: 'Validation failed.',
              details: [{ path: '/virtualAddresses/3/segment', message: 'Segment has no static IPv4 subnet.' }],
            },
          });
        }
        postedBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
        return jsonResponse(201, { value: { id: 'vip_proxy_new', ...postedBody }, version: '10' });
      },
    });
    const { onSaved } = renderVirtualIps();

    await user.click(await screen.findByRole('button', { name: 'Add virtual IP' }));
    const drawer = await screen.findByRole('dialog', { name: 'Add virtual IP' });

    // Segment picker is hidden for Held address and appears for Proxy ARP.
    expect(within(drawer).queryByLabelText(/^Segment/)).not.toBeInTheDocument();
    await user.selectOptions(within(drawer).getByLabelText(/^Kind/), 'proxyArp');

    const segmentSelect = within(drawer).getByLabelText(/^Segment/);
    const optionTexts = within(segmentSelect).getAllByRole('option').map((o) => o.textContent);
    expect(optionTexts).toContain('WAN (203.0.113.2/24)');
    expect(optionTexts.some((t) => t?.includes('Cable DHCP'))).toBe(false);

    // IPv6 input points to Proxy NDP locally.
    await user.type(within(drawer).getByLabelText(/^Name/), 'WAN NAT block');
    await user.selectOptions(segmentSelect, 'seg_wan');
    const addrInput = within(drawer).getByRole('textbox', { name: /^Address or prefix/ });
    await user.type(addrInput, '2001:db8::50');
    await user.click(within(drawer).getByRole('button', { name: 'Add virtual IP' }));
    expect(await within(drawer).findByText('Proxy ARP is IPv4 only; use Proxy NDP for IPv6.')).toBeInTheDocument();

    // Server /segment 422 maps directly to the Segment field and focuses it.
    await user.clear(addrInput);
    await user.type(addrInput, '203.0.113.56/29');
    rejectSegment = true;
    await user.click(within(drawer).getByRole('button', { name: 'Add virtual IP' }));
    expect(await within(drawer).findByText('Segment has no static IPv4 subnet.')).toBeInTheDocument();
    expect(segmentSelect).toHaveAttribute('aria-invalid', 'true');
    expect(segmentSelect).toHaveFocus();

    // Valid submit sends kind and segment.
    rejectSegment = false;
    await user.click(within(drawer).getByRole('button', { name: 'Add virtual IP' }));
    await waitFor(() => expect(postedBody).toEqual({
      name: 'WAN NAT block',
      kind: 'proxyArp',
      segment: 'seg_wan',
      address: '203.0.113.56/29',
    }));
    expect(onSaved).toHaveBeenCalledTimes(1);
  });

  it('edits a proxyArp entry in-place and switches back to held address without sending segment', async () => {
    const user = userEvent.setup();
    let putBody: Record<string, unknown> | undefined;
    const proxyVip: SavedObject<VirtualAddress> = {
      value: { id: 'vip_proxy', name: 'WAN extra', kind: 'proxyArp', segment: 'seg_wan', address: '203.0.113.50' },
      version: '4',
    };
    const multiSnapshot: Snapshot = {
      ...sampleSnapshot,
      value: {
        ...sampleSnapshot.value,
        segments: [
          ...sampleSnapshot.value.segments,
          { id: 'seg_wan', name: 'WAN', link: 'lnk_1', zone: 'zon_wan', addressing: { ipv4: { mode: 'static', addresses: ['203.0.113.2/24'] }, ipv6: { mode: 'none' } } },
        ],
        virtualAddresses: [proxyVip.value],
      },
    };
    fetchRoutes({
      '/api/v1/config/virtualAddresses': { state: 'saved', savedVersion: '9', items: [proxyVip] },
      '/api/v1/config': multiSnapshot,
      'PUT /api/v1/config/virtualAddresses/vip_proxy': (init) => {
        putBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
        return jsonResponse(200, { value: { id: 'vip_proxy', ...putBody }, version: '5' });
      },
    });
    renderVirtualIps();

    // Edit in-place keeping kind: proxyArp.
    await user.click(await screen.findByRole('button', { name: 'Edit virtual IP WAN extra' }));
    const drawer = await screen.findByRole('dialog', { name: 'Edit virtual IP: WAN extra' });
    await user.type(within(drawer).getByLabelText('Description'), 'Dedicated IP');
    await user.click(within(drawer).getByRole('button', { name: 'Save virtual IP' }));
    await waitFor(() => expect(putBody).toEqual({
      name: 'WAN extra',
      kind: 'proxyArp',
      segment: 'seg_wan',
      address: '203.0.113.50',
      description: 'Dedicated IP',
    }));

    // Switch back to held address and omit segment/kind.
    await user.click(await screen.findByRole('button', { name: 'Edit virtual IP WAN extra' }));
    const editAgain = await screen.findByRole('dialog', { name: 'Edit virtual IP: WAN extra' });
    await user.selectOptions(within(editAgain).getByLabelText(/^Kind/), 'address');
    expect(within(editAgain).queryByLabelText(/^Segment/)).not.toBeInTheDocument();
    const addrInput = within(editAgain).getByRole('textbox', { name: /^Address/ });
    await user.clear(addrInput);
    await user.type(addrInput, '172.16.53.100');
    await user.click(within(editAgain).getByRole('button', { name: 'Save virtual IP' }));

    await waitFor(() => expect(putBody).toEqual({
      name: 'WAN extra',
      address: '172.16.53.100',
    }));
  });

  it('warns when switching a DNS-referenced VIP to Proxy ARP and surfaces unrendered /segment 422s in the top banner', async () => {
    const user = userEvent.setup();
    fetchRoutes({
      '/api/v1/config/virtualAddresses': { state: 'saved', savedVersion: '9', items: fixtureVips },
      '/api/v1/config': sampleSnapshot,
      'PUT /api/v1/config/virtualAddresses/vip_dns4': () => jsonResponse(422, {
        error: {
          code: 'VALIDATION_FAILED',
          message: 'Validation failed.',
          details: [{ path: '/virtualAddresses/0/segment', message: 'Only a proxy ARP entry names a segment.' }],
        },
      }),
    });
    renderVirtualIps();

    await user.click(await screen.findByRole('button', { name: 'Edit virtual IP DNS v4' }));
    const drawer = await screen.findByRole('dialog', { name: 'Edit virtual IP: DNS v4' });
    expect(within(drawer).queryByText('Address in use by DNS service')).not.toBeInTheDocument();

    await user.selectOptions(within(drawer).getByLabelText(/^Kind/), 'proxyArp');
    expect(within(drawer).getByText('Address in use by DNS service')).toBeInTheDocument();

    // Switch back to held address: server /segment 422 must appear in the top alert and receive focus.
    await user.selectOptions(within(drawer).getByLabelText(/^Kind/), 'address');
    await user.click(within(drawer).getByRole('button', { name: 'Save virtual IP' }));
    const alert = await within(drawer).findByRole('alert');
    expect(alert).toHaveTextContent('Only a proxy ARP entry names a segment.');
    expect(alert).toHaveFocus();
  });

  it('creates a held routed block, warns on DNS-to-block edits (not /32), and blocks deleting a block targeted by a port forward', async () => {
    const user = userEvent.setup();
    let postedBody: Record<string, unknown> | undefined;
    const blockVip: SavedObject<VirtualAddress> = {
      value: { id: 'vip_blk_used', name: 'Targeted block', address: '198.51.100.8/29' },
      version: '4',
    };
    const snapshotWithBlockPf: Snapshot = {
      ...sampleSnapshot,
      value: {
        ...sampleSnapshot.value,
        virtualAddresses: [...sampleSnapshot.value.virtualAddresses!, blockVip.value],
        nat: {
          portForwards: [
            ...sampleSnapshot.value.nat!.portForwards!,
            {
              id: 'pfw_blk',
              name: 'Routed HTTPS',
              enabled: true,
              family: 'ipv4',
              ingressZones: ['zon_wan'],
              protocol: 'tcp',
              from: [],
              to: { self: true },
              ports: '443',
              target: { address: '198.51.100.12' },
            },
          ],
        },
      },
    };
    fetchRoutes({
      '/api/v1/config/virtualAddresses': { state: 'saved', savedVersion: '9', items: [...fixtureVips, blockVip] },
      '/api/v1/config': snapshotWithBlockPf,
      'POST /api/v1/config/virtualAddresses': (init) => {
        postedBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
        return jsonResponse(201, { value: { id: 'vip_blk', ...postedBody }, version: '10' });
      },
    });
    const { onSaved } = renderVirtualIps();

    await user.click(await screen.findByRole('button', { name: 'Add virtual IP' }));
    const addDrawer = await screen.findByRole('dialog', { name: 'Add virtual IP' });
    await user.type(within(addDrawer).getByLabelText(/^Name/), 'Routed v4 block');
    await user.type(within(addDrawer).getByRole('textbox', { name: /^Address or prefix/ }), '198.51.100.0/29');
    await user.click(within(addDrawer).getByRole('button', { name: 'Add virtual IP' }));

    await waitFor(() => expect(postedBody).toEqual({
      name: 'Routed v4 block',
      address: '198.51.100.0/29',
    }));
    expect(onSaved).toHaveBeenCalledTimes(1);

    // Editing a DNS-referenced VIP to a block shows the single-address DNS warning, while /32 does not.
    await user.click(screen.getByRole('button', { name: 'Edit virtual IP DNS v4' }));
    const editDrawer = await screen.findByRole('dialog', { name: 'Edit virtual IP: DNS v4' });
    const addrInput = within(editDrawer).getByRole('textbox', { name: /^Address or prefix/ });
    await user.clear(addrInput);
    await user.type(addrInput, '198.51.100.0/29');
    expect(within(editDrawer).getByText('Address in use by DNS service')).toBeInTheDocument();
    expect(within(editDrawer).getByText(/name one address, not a block/)).toBeInTheDocument();
    await user.clear(addrInput);
    await user.type(addrInput, '172.16.53.53/32');
    expect(within(editDrawer).queryByText('Address in use by DNS service')).not.toBeInTheDocument();
    await user.click(within(editDrawer).getByRole('button', { name: 'Cancel' }));

    // Delete modal blocks deletion of a held block when a port forward targets an address inside it.
    await user.click(screen.getByRole('button', { name: 'Delete virtual IP Targeted block' }));
    const deleteModal = await screen.findByRole('dialog', { name: 'Delete virtual IP' });
    expect(deleteModal).toHaveTextContent('Port forward 2 (Routed HTTPS)');
    expect(within(deleteModal).queryByRole('button', { name: 'Delete virtual IP' })).not.toBeInTheDocument();
  });

  it('displays, creates, edits, and filters proxyNdp entries with hostSegment and /128 host note', async () => {
    const user = userEvent.setup();
    let postedBody: Record<string, unknown> | undefined;
    let putBody: Record<string, unknown> | undefined;
    let rejectHostSegment = false;
    const ndpVip: SavedObject<VirtualAddress> = {
      value: {
        id: 'vip_ndp',
        name: 'Web server v6',
        kind: 'proxyNdp',
        segment: 'seg_wan',
        hostSegment: 'seg_lan',
        address: '2001:db8:1::50',
      },
      version: '4',
    };
    const multiSnapshot: Snapshot = {
      ...sampleSnapshot,
      value: {
        ...sampleSnapshot.value,
        segments: [
          ...sampleSnapshot.value.segments,
          { id: 'seg_wan', name: 'Uplink', link: 'lnk_1', zone: 'zon_wan', addressing: { ipv4: { mode: 'none' }, ipv6: { mode: 'static', addresses: ['2001:db8:1::2/64'] } } },
        ],
        virtualAddresses: [...sampleSnapshot.value.virtualAddresses!, ndpVip.value],
      },
    };
    fetchRoutes({
      '/api/v1/config/virtualAddresses': { state: 'saved', savedVersion: '9', items: [...fixtureVips, ndpVip] },
      '/api/v1/config': multiSnapshot,
      'POST /api/v1/config/virtualAddresses': (init) => {
        if (rejectHostSegment) {
          return jsonResponse(422, {
            error: {
              code: 'VALIDATION_FAILED',
              message: 'Validation failed.',
              details: [{ path: '/virtualAddresses/4/hostSegment', message: 'Host segment must differ.' }],
            },
          });
        }
        postedBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
        return jsonResponse(201, { value: { id: 'vip_ndp_new', ...postedBody }, version: '10' });
      },
      'PUT /api/v1/config/virtualAddresses/vip_ndp': (init) => {
        putBody = JSON.parse(String(init?.body)) as Record<string, unknown>;
        return jsonResponse(200, { value: { id: 'vip_ndp', ...putBody }, version: '5' });
      },
    });
    const { onSaved } = renderVirtualIps();

    expect(await screen.findByText('Web server v6')).toBeInTheDocument();
    const row = screen.getByText('Web server v6').closest('tr')!;
    expect(within(row).getByText('Proxy NDP')).toBeInTheDocument();
    expect(within(row).getByText('2001:db8:1::50')).toBeInTheDocument();
    expect(within(row).getByText('IPv6')).toBeInTheDocument();
    expect(within(row).getByText('Uplink → LAN')).toBeInTheDocument();

    // Filter by "proxy ndp" and by host segment name.
    const filter = screen.getByLabelText('Filter virtual IPs');
    await user.type(filter, 'proxy ndp');
    expect(screen.getByText('Web server v6')).toBeInTheDocument();
    expect(screen.queryByText('DNS v4')).not.toBeInTheDocument();
    await user.clear(filter);

    // Add a new Proxy NDP entry: verifies uplink/host pickers, /128 note, /hostSegment 422 mapping, and payload.
    await user.click(screen.getByRole('button', { name: 'Add virtual IP' }));
    const drawer = await screen.findByRole('dialog', { name: 'Add virtual IP' });
    await user.selectOptions(within(drawer).getByLabelText(/^Kind/), 'proxyNdp');
    expect(within(drawer).getByText('Host configuration')).toBeInTheDocument();
    expect(within(drawer).getByText(/Configure this address on the inside host/)).toHaveTextContent('/128');

    const uplinkSelect = within(drawer).getByLabelText(/^Uplink segment/);
    const hostSelect = within(drawer).getByLabelText(/^Host segment/);
    await user.type(within(drawer).getByLabelText(/^Name/), 'Mail v6');
    await user.selectOptions(uplinkSelect, 'seg_wan');
    await user.selectOptions(hostSelect, 'seg_lan');
    await user.type(within(drawer).getByRole('textbox', { name: /^Address/ }), '2001:db8:1::60');

    rejectHostSegment = true;
    await user.click(within(drawer).getByRole('button', { name: 'Add virtual IP' }));
    expect(await within(drawer).findByText('Host segment must differ.')).toBeInTheDocument();
    expect(hostSelect).toHaveAttribute('aria-invalid', 'true');
    expect(hostSelect).toHaveFocus();

    rejectHostSegment = false;
    await user.click(within(drawer).getByRole('button', { name: 'Add virtual IP' }));
    await waitFor(() => expect(postedBody).toEqual({
      name: 'Mail v6',
      kind: 'proxyNdp',
      segment: 'seg_wan',
      hostSegment: 'seg_lan',
      address: '2001:db8:1::60',
    }));
    expect(onSaved).toHaveBeenCalledTimes(1);

    // Edit existing proxyNdp entry and switch back to held address without sending segment/hostSegment.
    await user.click(screen.getByRole('button', { name: 'Edit virtual IP Web server v6' }));
    const editDrawer = await screen.findByRole('dialog', { name: 'Edit virtual IP: Web server v6' });
    await user.selectOptions(within(editDrawer).getByLabelText(/^Kind/), 'address');
    expect(within(editDrawer).queryByLabelText(/^Host segment/)).not.toBeInTheDocument();
    const editAddr = within(editDrawer).getByRole('textbox', { name: /^Address or prefix/ });
    await user.clear(editAddr);
    await user.type(editAddr, '2001:db8:53::1');
    await user.click(within(editDrawer).getByRole('button', { name: 'Save virtual IP' }));
    await waitFor(() => expect(putBody).toEqual({
      name: 'Web server v6',
      address: '2001:db8:53::1',
    }));
  });
});
