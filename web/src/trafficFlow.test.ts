import { describe, expect, it } from 'vitest';
import { formatIp, parseIp, parsePrefix, simulateTrafficFlow, validateProbe } from './trafficFlow';
import { firewallZone, type Snapshot } from './types';

const baseConfig: Snapshot['value'] = {
  links: [
    { id: 'lnk_wan', name: 'eth0', kind: 'ethernet', mtu: 1500, enabled: true },
    { id: 'lnk_lan', name: 'eth1', kind: 'ethernet', mtu: 1500, enabled: true },
    { id: 'lnk_dmz', name: 'eth2', kind: 'ethernet', mtu: 1500, enabled: true },
  ],
  segments: [
    {
      id: 'seg_wan',
      name: 'WAN',
      link: 'lnk_wan',
      zone: 'zon_wan',
      addressing: {
        ipv4: { mode: 'static', addresses: ['203.0.113.2/24'], gateway: '203.0.113.1' },
        ipv6: { mode: 'static', addresses: ['2001:db8:wan::2/64'], gateway: '2001:db8:wan::1' },
      },
    },
    {
      id: 'seg_lan',
      name: 'LAN',
      link: 'lnk_lan',
      zone: 'zon_lan',
      addressing: {
        ipv4: { mode: 'static', addresses: ['192.168.1.1/24'] },
        ipv6: { mode: 'static', addresses: ['fd00:1::1/64'] },
      },
    },
    {
      id: 'seg_dmz',
      name: 'DMZ',
      link: 'lnk_dmz',
      zone: 'zon_dmz',
      addressing: {
        ipv4: { mode: 'static', addresses: ['10.20.0.1/24'] },
        ipv6: { mode: 'none' },
      },
    },
  ],
  zones: [
    { id: 'zon_wan', name: 'WAN', color: 'red' },
    { id: 'zon_lan', name: 'LAN', color: 'green' },
    { id: 'zon_dmz', name: 'DMZ', color: 'violet' },
  ],
  management: { segment: 'seg_lan', allowedSources: ['adr_admin'] },
  dhcp: [{ id: 'dhp_lan', name: 'LAN DHCP', segment: 'seg_lan', ranges: [{ first: '192.168.1.100', last: '192.168.1.200' }] }],
  virtualAddresses: [{ id: 'vip_dns', name: 'DNS VIP', address: '172.16.53.53' }],
  aliases: {
    addresses: [
      { id: 'adr_admin', name: 'Admin workstation', kind: 'host', address: '192.168.1.10' },
      { id: 'adr_blocked', name: 'Blocked subnet', kind: 'network', prefix: '198.51.100.0/24' },
      { id: 'adr_range', name: 'Server range', kind: 'range', first: '10.20.0.10', last: '10.20.0.20' },
      { id: 'adr_group', name: 'Combined', kind: 'group', members: ['adr_admin', 'adr_blocked'] },
    ],
    ports: [
      { id: 'svc_https', name: 'HTTPS', protocol: 'tcp', ports: ['443'] },
      { id: 'svc_dns', name: 'DNS', protocol: 'udp', ports: ['53'] },
      { id: 'svc_ping', name: 'ICMP Echo', protocol: 'icmp', types: ['echo-request'] },
      { id: 'svc_web', name: 'Web group', protocol: 'group', members: ['svc_https'] },
    ],
  },
  rules: [
    {
      id: 'rul_block_bad',
      name: 'Block bad net',
      enabled: true,
      action: 'reject',
      from: { zones: ['zon_lan'], addresses: [] },
      to: { zones: ['zon_wan'], addresses: ['adr_blocked'] },
      ports: [],
      log: true,
    },
    {
      id: 'rul_lan_out',
      name: 'LAN outbound',
      enabled: true,
      action: 'accept',
      from: { zones: ['zon_lan'], addresses: [] },
      to: { zones: ['zon_wan'], addresses: [] },
      ports: ['svc_web'],
      log: false,
    },
    {
      id: 'rul_dns_vip',
      name: 'Allow LAN DNS',
      enabled: true,
      action: 'accept',
      from: { zones: ['zon_lan'], addresses: [] },
      to: { zones: [firewallZone], addresses: [] },
      ports: ['svc_dns'],
      log: false,
    },
  ],
  nat: {
    outbound: { mode: 'automatic' },
    portForwards: [
      {
        id: 'pfw_https',
        name: 'Web server',
        enabled: true,
        ingressZones: ['zon_wan'],
        protocol: 'tcp',
        from: [],
        to: { self: true },
        ports: '443',
        target: { address: '10.20.0.15' },
        targetPort: 8443,
        filterRule: 'auto',
        reflection: true,
      },
    ],
    oneToOne: [
      {
        id: 'oto_skip',
        name: 'Exclude host',
        enabled: true,
        zone: 'zon_wan',
        internal: '10.20.0.50/32',
        destination: [],
        noBinat: true,
      },
      {
        id: 'oto_dmz',
        name: 'DMZ 1:1',
        enabled: true,
        zone: 'zon_wan',
        external: '203.0.113.64/28',
        internal: '10.20.0.48/28',
        destination: [],
      },
    ],
  },
};

describe('trafficFlow helpers', () => {
  it('parses and formats IPv4/IPv6 addresses and prefixes', () => {
    expect(parseIp('192.168.1.1')?.canonical).toBe('192.168.1.1');
    expect(parseIp('FD00:0000:0000:0001::000a')?.canonical).toBe('fd00:0:0:1::a');
    expect(parseIp('::ffff:1.2.3.4')).toBeNull();
    expect(parseIp('::ffff:102:304')).toBeNull();
    expect(formatIp(4, parseIp('203.0.113.2')!.value)).toBe('203.0.113.2');
    expect(parsePrefix('192.168.1.5/24')?.network).toBe(parseIp('192.168.1.0')!.value);
  });

  it('validates probe addresses, family match, protocol match, and port bounds', () => {
    const mismatch = validateProbe(
      { ingressSegmentId: 'seg_lan', protocol: 'tcp', srcIp: '192.168.1.50', dstIp: '2001:db8::1', dstPort: 443 },
      baseConfig,
    );
    expect(mismatch.get('dstIp')).toMatch(/IPv4/);

    const badIcmp = validateProbe(
      { ingressSegmentId: 'seg_lan', protocol: 'icmpv6', srcIp: '192.168.1.50', dstIp: '1.1.1.1' },
      baseConfig,
    );
    expect(badIcmp.get('protocol')).toMatch(/IPv6/);

    const badPort = validateProbe(
      { ingressSegmentId: 'seg_lan', protocol: 'tcp', srcIp: '192.168.1.50', dstIp: '1.1.1.1', dstPort: 70000 },
      baseConfig,
    );
    expect(badPort.get('dstPort')).toMatch(/1 to 65535/);
  });
});

describe('simulateTrafficFlow', () => {
  it('traces allowed LAN->WAN HTTPS traffic with outbound automatic masquerade', () => {
    const result = simulateTrafficFlow(
      { ingressSegmentId: 'seg_lan', protocol: 'tcp', srcIp: '192.168.1.50', srcPort: 54321, dstIp: '1.1.1.1', dstPort: 443 },
      baseConfig,
    );
    expect(result.verdict).toBe('accept');
    expect(result.chain).toBe('forward');
    expect(result.decidedBy).toEqual({ label: 'Rule #2 (LAN outbound)', to: '/firewall/rules' });
    expect(result.finalTuple).toBe('TCP 203.0.113.2:54321 → 1.1.1.1:443');
    expect(result.steps.map((s) => s.stage)).toEqual(['ingress', 'prerouting', 'routing', 'filter', 'postrouting', 'verdict']);
  });

  it('traces rejected LAN->WAN TCP traffic with TCP RST and no post-routing step', () => {
    const result = simulateTrafficFlow(
      { ingressSegmentId: 'seg_lan', protocol: 'tcp', srcIp: '192.168.1.50', dstIp: '198.51.100.25', dstPort: 443 },
      baseConfig,
    );
    expect(result.verdict).toBe('reject');
    expect(result.verdictLabel).toBe('Rejected (TCP RST)');
    expect(result.decidedBy).toEqual({ label: 'Rule #1 (Block bad net)', to: '/firewall/rules' });
    expect(result.steps.some((s) => s.stage === 'postrouting')).toBe(false);
  });

  it('traces WAN port forward DNAT with auto filter rule to DMZ', () => {
    const result = simulateTrafficFlow(
      { ingressSegmentId: 'seg_wan', protocol: 'tcp', srcIp: '198.51.100.9', srcPort: 40000, dstIp: '203.0.113.2', dstPort: 443 },
      baseConfig,
    );
    expect(result.verdict).toBe('accept');
    expect(result.chain).toBe('forward');
    expect(result.decidedBy).toEqual({ label: 'Port forward #1 auto rule (Web server)', to: '/firewall/nat' });
    expect(result.finalTuple).toBe('TCP 198.51.100.9:40000 → 10.20.0.15:8443');
    expect(result.steps.find((s) => s.stage === 'prerouting')?.outcome).toBe('transform');
  });

  it('traces 1:1 NAT outbound SNAT and respects noBinat exclusions', () => {
    const configWithDmzOut: Snapshot['value'] = {
      ...baseConfig,
      rules: [
        ...baseConfig.rules!,
        { id: 'rul_dmz_out', name: 'DMZ outbound', enabled: true, action: 'accept', from: { zones: ['zon_dmz'], addresses: [] }, to: { zones: ['zon_wan'], addresses: [] }, ports: [], log: false },
      ],
    };
    // 10.20.0.52 (+4 in 10.20.0.48/28) maps to 203.0.113.68 (+4 in 203.0.113.64/28).
    const mapped = simulateTrafficFlow(
      { ingressSegmentId: 'seg_dmz', protocol: 'tcp', srcIp: '10.20.0.52', dstIp: '1.1.1.1', dstPort: 80 },
      configWithDmzOut,
    );
    expect(mapped.verdict).toBe('accept');
    expect(mapped.finalTuple).toBe('TCP 203.0.113.68 → 1.1.1.1:80');
    expect(mapped.steps.find((s) => s.stage === 'postrouting')?.decidedBy?.label).toBe('1:1 NAT #2 (DMZ 1:1)');

    // 10.20.0.50 is excluded by noBinat and falls through to outbound masquerade.
    const excluded = simulateTrafficFlow(
      { ingressSegmentId: 'seg_dmz', protocol: 'tcp', srcIp: '10.20.0.50', dstIp: '1.1.1.1', dstPort: 80 },
      configWithDmzOut,
    );
    expect(excluded.finalTuple).toBe('TCP 203.0.113.2 → 1.1.1.1:80');
    expect(excluded.steps.find((s) => s.stage === 'postrouting')?.decidedBy?.label).toBe('Outbound NAT (Automatic)');
  });

  it('routes traffic to a virtual IP into input chain and checks fixed management SSH and input rules', () => {
    const dnsVip = simulateTrafficFlow(
      { ingressSegmentId: 'seg_lan', protocol: 'udp', srcIp: '192.168.1.50', dstIp: '172.16.53.53', dstPort: 53 },
      baseConfig,
    );
    expect(dnsVip.verdict).toBe('accept');
    expect(dnsVip.chain).toBe('input');
    expect(dnsVip.steps.find((s) => s.stage === 'routing')?.decidedBy).toEqual({ label: 'Virtual IP DNS VIP', to: '/network/virtual-ips' });

    const mgmtAllowed = simulateTrafficFlow(
      { ingressSegmentId: 'seg_lan', protocol: 'tcp', srcIp: '192.168.1.10', dstIp: '192.168.1.1', dstPort: 22 },
      baseConfig,
    );
    expect(mgmtAllowed.verdict).toBe('accept');
    expect(mgmtAllowed.decidedBy?.label).toBe('Management SSH (LAN)');

    const mgmtDenied = simulateTrafficFlow(
      { ingressSegmentId: 'seg_lan', protocol: 'tcp', srcIp: '192.168.1.99', dstIp: '192.168.1.1', dstPort: 22 },
      baseConfig,
    );
    expect(mgmtDenied.verdict).toBe('drop');
    expect(mgmtDenied.decidedBy?.label).toBe('Default input policy (drop)');
  });

  it('reports no_route when no IPv6 uplink exists for an external IPv6 destination', () => {
    const noV6Uplink: Snapshot['value'] = {
      ...baseConfig,
      segments: baseConfig.segments.map((s) => (s.id === 'seg_wan' ? { ...s, addressing: { ...s.addressing, ipv6: { mode: 'none' } } } : s)),
    };
    const result = simulateTrafficFlow(
      { ingressSegmentId: 'seg_lan', protocol: 'icmpv6', srcIp: 'fd00:1::50', dstIp: '2606:4700:4700::1111', icmpType: 'echo-request' },
      noV6Uplink,
    );
    expect(result.verdict).toBe('no_route');
    expect(result.chain).toBe('none');
  });
});
