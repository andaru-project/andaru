import { describe, expect, it } from 'vitest';
import { formatIp, parseIp, parsePrefix, simulateTrafficFlow, validateProbe } from './trafficFlow';
import { firewallZone, type LinkStatus, type Snapshot } from './types';

const baseConfig: Snapshot['value'] = {
  links: [
    { id: 'lnk_wan', name: 'WAN port', kind: 'ethernet', mtu: 1500, enabled: true, permanentMac: '52:54:00:aa:bb:01' },
    { id: 'lnk_lan', name: 'LAN port', kind: 'ethernet', mtu: 1500, enabled: true, permanentMac: '52:54:00:aa:bb:02' },
    { id: 'lnk_dmz', name: 'DMZ port', kind: 'ethernet', mtu: 1500, enabled: true, permanentMac: '52:54:00:aa:bb:03' },
  ],
  segments: [
    {
      id: 'seg_wan',
      name: 'WAN',
      link: 'lnk_wan',
      zone: 'zon_wan',
      addressing: {
        ipv4: { mode: 'static', addresses: ['203.0.113.2/24'], gateway: '203.0.113.1' },
        ipv6: { mode: 'static', addresses: ['2001:db8:1::2/64'], gateway: '2001:db8:1::1' },
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
  dhcp6: [{ id: 'dh6_lan', name: 'LAN DHCPv6', segment: 'seg_lan', ranges: [{ first: 'fd00:1::100', last: 'fd00:1::200' }] }],
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
      id: 'rul_block_tcp_https',
      name: 'Reject blocked HTTPS',
      enabled: true,
      action: 'reject',
      from: { zones: ['zon_lan'], addresses: [] },
      to: { zones: ['zon_wan'], addresses: ['adr_blocked'] },
      ports: ['svc_https'],
      log: true,
    },
    {
      id: 'rul_block_bad',
      name: 'Reject bad net',
      enabled: true,
      action: 'reject',
      from: { zones: ['zon_lan'], addresses: [] },
      to: { zones: ['zon_wan'], addresses: ['adr_blocked'] },
      ports: [],
      log: false,
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
      id: 'rul_lan_dmz',
      name: 'LAN to DMZ',
      enabled: true,
      action: 'accept',
      from: { zones: ['zon_lan'], addresses: [] },
      to: { zones: ['zon_dmz'], addresses: ['adr_range'] },
      ports: [],
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
    expect(result.decidedBy).toEqual({ label: 'Rule #3 (LAN outbound)', to: '/firewall/rules' });
    expect(result.finalTuple).toBe('TCP 203.0.113.2:54321 → 1.1.1.1:443');
    expect(result.steps.map((s) => s.stage)).toEqual(['ingress', 'prerouting', 'routing', 'filter', 'postrouting', 'verdict']);
  });

  it('distinguishes TCP RST reject (TCP port alias) from plain reject (empty ports)', () => {
    const tcpReset = simulateTrafficFlow(
      { ingressSegmentId: 'seg_lan', protocol: 'tcp', srcIp: '192.168.1.50', dstIp: '198.51.100.25', dstPort: 443 },
      baseConfig,
    );
    expect(tcpReset.verdict).toBe('reject');
    expect(tcpReset.verdictLabel).toBe('Rejected (TCP RST)');
    expect(tcpReset.decidedBy).toEqual({ label: 'Rule #1 (Reject blocked HTTPS)', to: '/firewall/rules' });
    expect(tcpReset.steps.some((s) => s.stage === 'postrouting')).toBe(false);

    const plainReject = simulateTrafficFlow(
      { ingressSegmentId: 'seg_lan', protocol: 'tcp', srcIp: '192.168.1.50', dstIp: '198.51.100.25', dstPort: 80 },
      baseConfig,
    );
    expect(plainReject.verdict).toBe('reject');
    expect(plainReject.verdictLabel).toBe('Rejected');
    expect(plainReject.decidedBy).toEqual({ label: 'Rule #2 (Reject bad net)', to: '/firewall/rules' });
  });

  it('traces WAN port forward DNAT with auto filter rule, NAT reflection, and hairpin masquerade', () => {
    const wanHit = simulateTrafficFlow(
      { ingressSegmentId: 'seg_wan', protocol: 'tcp', srcIp: '198.51.100.9', srcPort: 40000, dstIp: '203.0.113.2', dstPort: 443 },
      baseConfig,
    );
    expect(wanHit.verdict).toBe('accept');
    expect(wanHit.chain).toBe('forward');
    expect(wanHit.decidedBy).toEqual({ label: 'Port forward #1 auto rule (Web server)', to: '/firewall/nat' });
    expect(wanHit.finalTuple).toBe('TCP 198.51.100.9:40000 → 10.20.0.15:8443');

    // LAN client hitting WAN IP reflects to DMZ (10.20.0.15), allowed by LAN->DMZ rule (not auto rule since ingress is LAN).
    const reflected = simulateTrafficFlow(
      { ingressSegmentId: 'seg_lan', protocol: 'tcp', srcIp: '192.168.1.50', dstIp: '203.0.113.2', dstPort: 443 },
      baseConfig,
    );
    expect(reflected.verdict).toBe('accept');
    expect(reflected.decidedBy).toEqual({ label: 'Rule #4 (LAN to DMZ)', to: '/firewall/rules' });
    expect(reflected.finalTuple).toBe('TCP 192.168.1.50 → 10.20.0.15:8443');

    // DMZ client hitting WAN IP reflects back into DMZ and triggers hairpin masquerade (10.20.0.1).
    const withDmzSelf: Snapshot['value'] = {
      ...baseConfig,
      rules: [
        ...baseConfig.rules!,
        { id: 'rul_dmz_self', name: 'DMZ hairpin', enabled: true, action: 'accept', from: { zones: ['zon_dmz'], addresses: [] }, to: { zones: ['zon_dmz'], addresses: [] }, ports: [], log: false },
      ],
    };
    const hairpin = simulateTrafficFlow(
      { ingressSegmentId: 'seg_dmz', protocol: 'tcp', srcIp: '10.20.0.99', dstIp: '203.0.113.2', dstPort: 443 },
      withDmzSelf,
    );
    expect(hairpin.verdict).toBe('accept');
    expect(hairpin.finalTuple).toBe('TCP 10.20.0.1 → 10.20.0.15:8443');
    expect(hairpin.steps.find((s) => s.stage === 'postrouting')?.decidedBy?.label).toBe('NAT reflection masquerade');
  });

  it('handles noRedirect exception, target.self transparent redirect, and 1:1 inbound DNAT + outbound SNAT', () => {
    const withTransparentDns: Snapshot['value'] = {
      ...baseConfig,
      nat: {
        ...baseConfig.nat,
        portForwards: [
          {
            id: 'pfw_bypass',
            name: 'Admin DNS bypass',
            enabled: true,
            ingressZones: ['zon_lan'],
            protocol: 'udp',
            from: ['adr_admin'],
            to: { self: true },
            toNegated: true,
            ports: '53',
            noRedirect: true,
          },
          {
            id: 'pfw_dns_redir',
            name: 'Intercept DNS',
            enabled: true,
            ingressZones: ['zon_lan'],
            protocol: 'udp',
            from: [],
            to: { self: true },
            toNegated: true,
            ports: '53',
            target: { self: true },
            filterRule: 'none',
          },
        ],
      },
    };
    // Admin matches noRedirect -> continues untranslated to WAN (dropped because only HTTPS allowed outbound).
    const bypassed = simulateTrafficFlow(
      { ingressSegmentId: 'seg_lan', protocol: 'udp', srcIp: '192.168.1.10', dstIp: '8.8.8.8', dstPort: 53 },
      withTransparentDns,
    );
    expect(bypassed.steps.find((s) => s.stage === 'prerouting')?.decidedBy?.label).toBe('Port forward #1 (Admin DNS bypass)');
    expect(bypassed.finalTuple).toBe('UDP 192.168.1.10 → 8.8.8.8:53');

    // Non-admin LAN host gets redirected to firewall's ingress IP (192.168.1.1) and allowed by input DNS rule.
    const redirected = simulateTrafficFlow(
      { ingressSegmentId: 'seg_lan', protocol: 'udp', srcIp: '192.168.1.50', dstIp: '8.8.8.8', dstPort: 53 },
      withTransparentDns,
    );
    expect(redirected.verdict).toBe('accept');
    expect(redirected.chain).toBe('input');
    expect(redirected.finalTuple).toBe('UDP 192.168.1.50 → 192.168.1.1:53');
    expect(redirected.decidedBy?.label).toBe('Rule #5 (Allow LAN DNS)');

    // 1:1 inbound DNAT + outbound SNAT with noBinat exclusion.
    const configWithDmzRules: Snapshot['value'] = {
      ...baseConfig,
      rules: [
        ...baseConfig.rules!,
        { id: 'rul_dmz_out', name: 'DMZ outbound', enabled: true, action: 'accept', from: { zones: ['zon_dmz'], addresses: [] }, to: { zones: ['zon_wan'], addresses: [] }, ports: [], log: false },
        { id: 'rul_wan_dmz', name: 'WAN to DMZ', enabled: true, action: 'accept', from: { zones: ['zon_wan'], addresses: [] }, to: { zones: ['zon_dmz'], addresses: [] }, ports: [], log: false },
      ],
    };
    const inbound = simulateTrafficFlow(
      { ingressSegmentId: 'seg_wan', protocol: 'tcp', srcIp: '1.1.1.1', dstIp: '203.0.113.68', dstPort: 80 },
      configWithDmzRules,
    );
    expect(inbound.verdict).toBe('accept');
    expect(inbound.finalTuple).toBe('TCP 1.1.1.1 → 10.20.0.52:80');
    expect(inbound.steps.find((s) => s.stage === 'prerouting')?.decidedBy?.label).toBe('1:1 NAT #2 (DMZ 1:1)');

    const outboundMapped = simulateTrafficFlow(
      { ingressSegmentId: 'seg_dmz', protocol: 'tcp', srcIp: '10.20.0.52', dstIp: '1.1.1.1', dstPort: 80 },
      configWithDmzRules,
    );
    expect(outboundMapped.finalTuple).toBe('TCP 203.0.113.68 → 1.1.1.1:80');

    const outboundExcluded = simulateTrafficFlow(
      { ingressSegmentId: 'seg_dmz', protocol: 'tcp', srcIp: '10.20.0.50', dstIp: '1.1.1.1', dstPort: 80 },
      configWithDmzRules,
    );
    expect(outboundExcluded.finalTuple).toBe('TCP 203.0.113.2 → 1.1.1.1:80');
    expect(outboundExcluded.steps.find((s) => s.stage === 'postrouting')?.decidedBy?.label).toBe('Outbound NAT (Automatic)');
  });

  it('routes VIPs, local broadcasts, and link-local multicast to input and sets decidedBy on fixed rules', () => {
    const dnsVip = simulateTrafficFlow(
      { ingressSegmentId: 'seg_lan', protocol: 'udp', srcIp: '192.168.1.50', dstIp: '172.16.53.53', dstPort: 53 },
      baseConfig,
    );
    expect(dnsVip.verdict).toBe('accept');
    expect(dnsVip.chain).toBe('input');
    expect(dnsVip.steps.find((s) => s.stage === 'routing')?.isFirewall).toBe(true);
    expect(dnsVip.steps.find((s) => s.stage === 'routing')?.decidedBy).toEqual({ label: 'Virtual IP DNS VIP', to: '/network/virtual-ips' });

    const dhcp4 = simulateTrafficFlow(
      { ingressSegmentId: 'seg_lan', protocol: 'udp', srcIp: '0.0.0.0', srcPort: 68, dstIp: '255.255.255.255', dstPort: 67 },
      baseConfig,
    );
    expect(dhcp4.verdict).toBe('accept');
    expect(dhcp4.decidedBy?.label).toBe('DHCPv4 on LAN');

    const dhcp6 = simulateTrafficFlow(
      { ingressSegmentId: 'seg_lan', protocol: 'udp', srcIp: 'fe80::1234', srcPort: 546, dstIp: 'ff02::1:2', dstPort: 547 },
      baseConfig,
    );
    expect(dhcp6.verdict).toBe('accept');
    expect(dhcp6.decidedBy?.label).toBe('DHCPv6 on LAN');

    const ndp = simulateTrafficFlow(
      { ingressSegmentId: 'seg_lan', protocol: 'icmpv6', srcIp: 'fe80::1234', dstIp: 'ff02::2', icmpType: 'nd-router-solicit' },
      baseConfig,
    );
    expect(ndp.verdict).toBe('accept');
    expect(ndp.decidedBy).toEqual({ label: 'Fixed rules (Firewall)', to: '/firewall/rules' });

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
  });

  it('matches live dynamic addresses via MAC/kernelName and warns when management segment is disabled', () => {
    const dynamicWan: Snapshot['value'] = {
      ...baseConfig,
      segments: baseConfig.segments.map((s) => (s.id === 'seg_wan'
        ? { ...s, addressing: { ipv4: { mode: 'dhcp' }, ipv6: { mode: 'none' } } }
        : s)),
    };
    const liveLinks: LinkStatus[] = [
      {
        ifIndex: 2,
        name: 'enp1s0',
        linkType: 'ether',
        mtu: 1500,
        operState: 'UP',
        flags: ['UP'],
        permanentAddress: '52:54:00:AA:BB:01',
        addresses: [{ family: 'ipv4', address: '198.51.100.77', prefixLength: 24, scope: 'global' }],
      },
    ];
    const dynOut = simulateTrafficFlow(
      { ingressSegmentId: 'seg_lan', protocol: 'tcp', srcIp: '192.168.1.50', dstIp: '1.1.1.1', dstPort: 443 },
      dynamicWan,
      liveLinks,
      { evaluatingSavedDiffersFromLive: true },
    );
    expect(dynOut.verdict).toBe('accept');
    expect(dynOut.finalTuple).toBe('TCP 198.51.100.77 → 1.1.1.1:443');
    expect(dynOut.warnings.some((w) => w.includes('Dynamic DHCP/PPPoE/SLAAC'))).toBe(true);

    // Disabled management link disables firewall, NAT, and IP forwarding.
    const disabledMgmt: Snapshot['value'] = {
      ...baseConfig,
      links: baseConfig.links.map((l) => (l.id === 'lnk_lan' ? { ...l, enabled: false } : l)),
    };
    const unfilteredForward = simulateTrafficFlow(
      { ingressSegmentId: 'seg_dmz', protocol: 'tcp', srcIp: '10.20.0.10', dstIp: '1.1.1.1', dstPort: 80 },
      disabledMgmt,
    );
    expect(unfilteredForward.verdict).toBe('drop');
    expect(unfilteredForward.warnings.some((w) => w.includes('No usable management segment'))).toBe(true);

    // No IPv6 uplink reports no_route.
    const noV6 = simulateTrafficFlow(
      { ingressSegmentId: 'seg_lan', protocol: 'icmpv6', srcIp: 'fd00:1::50', dstIp: '2606:4700:4700::1111', icmpType: 'echo-request' },
      dynamicWan,
    );
    expect(noV6.verdict).toBe('no_route');
    expect(noV6.chain).toBe('none');
  });

  it('handles disabled ingress, MAC mismatch, link-local scoping/forwarding, IPv4-less redirect, excluded 1:1 reflection, and dual-subnet hairpin', () => {
    // Disabled ingress stops at ingress instead of matching zone allow rules.
    const disabledDmz: Snapshot['value'] = {
      ...baseConfig,
      links: baseConfig.links.map((l) => (l.id === 'lnk_dmz' ? { ...l, enabled: false } : l)),
      rules: [{ id: 'rul_allow_all', name: 'Allow all', enabled: true, action: 'accept', from: { zones: ['zon_dmz'], addresses: [] }, to: { zones: ['zon_wan'], addresses: [] }, ports: [], log: false }],
    };
    const fromDisabled = simulateTrafficFlow(
      { ingressSegmentId: 'seg_dmz', protocol: 'tcp', srcIp: '10.20.0.10', dstIp: '1.1.1.1', dstPort: 443 },
      disabledDmz,
    );
    expect(fromDisabled.verdict).toBe('drop');
    expect(fromDisabled.verdictLabel).toBe('Interface disabled');

    // Ethernet link with mismatched permanent MAC does not adopt another interface's live lease by name.
    const dynamicWan: Snapshot['value'] = {
      ...baseConfig,
      segments: baseConfig.segments.map((s) => (s.id === 'seg_wan'
        ? { ...s, addressing: { ipv4: { mode: 'dhcp' }, ipv6: { mode: 'none' } } }
        : s)),
    };
    const wrongMacLive: LinkStatus[] = [{
      ifIndex: 2,
      name: 'WAN port',
      linkType: 'ether',
      mtu: 1500,
      operState: 'UP',
      flags: ['UP'],
      permanentAddress: '52:54:00:ff:ff:ff',
      addresses: [{ family: 'ipv4', address: '198.51.100.99', prefixLength: 24, scope: 'global' }],
    }];
    const mismatch = simulateTrafficFlow(
      { ingressSegmentId: 'seg_lan', protocol: 'tcp', srcIp: '192.168.1.50', dstIp: '1.1.1.1', dstPort: 443 },
      dynamicWan,
      wrongMacLive,
    );
    expect(mismatch.finalTuple).not.toContain('198.51.100.99');
    expect(mismatch.warnings.some((w) => w.includes('no observed live address'))).toBe(true);

    // IPv6 link-local source cannot cross links to connected WAN subnet, and DMZ static fe80::1 is scoped to DMZ.
    const withDmzLinkLocal: Snapshot['value'] = {
      ...baseConfig,
      segments: baseConfig.segments.map((s) => (s.id === 'seg_dmz'
        ? { ...s, addressing: { ...s.addressing, ipv6: { mode: 'static', addresses: ['fe80::1/64'] } } }
        : s)),
    };
    const crossLinkLl = simulateTrafficFlow(
      { ingressSegmentId: 'seg_lan', protocol: 'icmpv6', srcIp: 'fe80::1234', dstIp: '2001:db8:1::99', icmpType: 'echo-request' },
      withDmzLinkLocal,
    );
    expect(crossLinkLl.verdict).toBe('no_route');
    const otherSegLl = simulateTrafficFlow(
      { ingressSegmentId: 'seg_lan', protocol: 'icmpv6', srcIp: 'fe80::1234', dstIp: 'fe80::1', icmpType: 'echo-request' },
      withDmzLinkLocal,
    );
    expect(otherSegLl.verdict).toBe('no_route');

    // IPv4 target.self redirect drops on an IPv4-less ingress interface.
    const ipv4LessDmz: Snapshot['value'] = {
      ...baseConfig,
      segments: baseConfig.segments.map((s) => (s.id === 'seg_dmz'
        ? { ...s, addressing: { ipv4: { mode: 'none' }, ipv6: { mode: 'none' } } }
        : s)),
      nat: {
        ...baseConfig.nat,
        portForwards: [{
          id: 'pfw_redir',
          name: 'DNS redirect',
          enabled: true,
          ingressZones: ['zon_dmz'],
          protocol: 'udp',
          from: [],
          to: { self: true },
          toNegated: true,
          ports: '53',
          target: { self: true },
          filterRule: 'auto',
        }],
      },
    };
    const droppedRedir = simulateTrafficFlow(
      { ingressSegmentId: 'seg_dmz', protocol: 'udp', srcIp: '10.20.0.10', dstIp: '8.8.8.8', dstPort: 53 },
      ipv4LessDmz,
    );
    expect(droppedRedir.verdict).toBe('drop');
    expect(droppedRedir.steps.find((s) => s.stage === 'prerouting')?.summary).toMatch(/has no IPv4 address/);

    // Dual-subnet DMZ hairpin selects the translated destination's connected route prefix (10.30.0.1), not source prefix (10.20.0.1).
    const dualDmz: Snapshot['value'] = {
      ...baseConfig,
      segments: baseConfig.segments.map((s) => (s.id === 'seg_dmz'
        ? { ...s, addressing: { ipv4: { mode: 'static', addresses: ['10.20.0.1/24', '10.30.0.1/24'] }, ipv6: { mode: 'none' } } }
        : s)),
      rules: [
        ...baseConfig.rules!,
        { id: 'rul_dmz_any', name: 'DMZ hairpin', enabled: true, action: 'accept', from: { zones: ['zon_dmz'], addresses: [] }, to: { zones: ['zon_dmz'], addresses: [] }, ports: [], log: false },
      ],
      nat: {
        ...baseConfig.nat,
        portForwards: [{
          id: 'pfw_dual',
          name: 'Web dual',
          enabled: true,
          ingressZones: ['zon_wan'],
          protocol: 'tcp',
          from: [],
          to: { self: true },
          ports: '443',
          target: { address: '10.30.0.15' },
          filterRule: 'auto',
          reflection: true,
        }],
      },
    };
    const dualHairpin = simulateTrafficFlow(
      { ingressSegmentId: 'seg_dmz', protocol: 'tcp', srcIp: '10.20.0.99', dstIp: '203.0.113.2', dstPort: 443 },
      dualDmz,
    );
    expect(dualHairpin.verdict).toBe('accept');
    expect(dualHairpin.finalTuple).toBe('TCP 10.30.0.1 → 10.30.0.15:443');

    // Fully excluded reflected 1:1 mapping does not enable table-wide hairpin reflection.
    const excludedReflection: Snapshot['value'] = {
      ...baseConfig,
      rules: [
        ...baseConfig.rules!,
        { id: 'rul_dmz_any', name: 'DMZ any', enabled: true, action: 'accept', from: { zones: ['zon_dmz'], addresses: [] }, to: { zones: ['zon_dmz'], addresses: [] }, ports: [], log: false },
      ],
      nat: {
        outbound: { mode: 'automatic' },
        portForwards: [{
          id: 'pfw_dmz_noreflect',
          name: 'DMZ local DNAT',
          enabled: true,
          ingressZones: ['zon_dmz'],
          protocol: 'tcp',
          from: [],
          to: { self: true },
          ports: '443',
          target: { address: '10.20.0.15' },
          filterRule: 'auto',
          reflection: false,
        }],
        oneToOne: [
          { id: 'oto_excl', name: 'Exclude all', enabled: true, zone: 'zon_wan', internal: '10.20.0.0/24', destination: [], noBinat: true },
          { id: 'oto_refl', name: 'Excluded reflected', enabled: true, zone: 'zon_wan', external: '203.0.113.50/32', internal: '10.20.0.50/32', destination: [], reflection: true },
        ],
      },
    };
    const noHairpin = simulateTrafficFlow(
      { ingressSegmentId: 'seg_dmz', protocol: 'tcp', srcIp: '10.20.0.99', dstIp: '10.20.0.1', dstPort: 443 },
      excludedReflection,
    );
    expect(noHairpin.finalTuple).toBe('TCP 10.20.0.99 → 10.20.0.15:443');

    // Multi-subnet uplink masquerade selects the address matching the route next hop (gateway or connected subnet).
    const multiSubnetWan: Snapshot['value'] = {
      ...baseConfig,
      segments: baseConfig.segments.map((s) => (s.id === 'seg_wan'
        ? { ...s, addressing: { ipv4: { mode: 'static', addresses: ['10.0.0.2/24', '203.0.113.2/24'], gateway: '203.0.113.1' }, ipv6: { mode: 'none' } } }
        : s)),
    };
    const viaGw = simulateTrafficFlow(
      { ingressSegmentId: 'seg_lan', protocol: 'tcp', srcIp: '10.10.0.50', dstIp: '8.8.8.8', dstPort: 443 },
      multiSubnetWan,
    );
    expect(viaGw.verdict).toBe('accept');
    expect(viaGw.finalTuple).toBe('TCP 203.0.113.2 → 8.8.8.8:443');

    const viaConnected = simulateTrafficFlow(
      { ingressSegmentId: 'seg_lan', protocol: 'tcp', srcIp: '10.10.0.50', dstIp: '10.0.0.50', dstPort: 443 },
      multiSubnetWan,
    );
    expect(viaConnected.verdict).toBe('accept');
    expect(viaConnected.finalTuple).toBe('TCP 10.0.0.2 → 10.0.0.50:443');

    // Without a usable management segment, local input passes unfiltered but transit forwarding is disabled.
    const noMgmt: Snapshot['value'] = {
      ...baseConfig,
      management: undefined,
    };
    const noMgmtForward = simulateTrafficFlow(
      { ingressSegmentId: 'seg_lan', protocol: 'tcp', srcIp: '192.168.1.50', dstIp: '10.20.0.15', dstPort: 443 },
      noMgmt,
    );
    expect(noMgmtForward.verdict).toBe('drop');
    expect(noMgmtForward.steps.find((s) => s.stage === 'filter')?.summary).toMatch(/forwarding is disabled/i);

    const noMgmtInput = simulateTrafficFlow(
      { ingressSegmentId: 'seg_lan', protocol: 'tcp', srcIp: '192.168.1.50', dstIp: '192.168.1.1', dstPort: 443 },
      noMgmt,
    );
    expect(noMgmtInput.verdict).toBe('accept');
    expect(noMgmtInput.steps.find((s) => s.stage === 'filter')?.outcome).toBe('warn');

    // Static DHCPv6 prefix-delegation pool installs an unreachable route so unleased destinations do not follow the IPv6 uplink.
    const dhcp6Pool: Snapshot['value'] = {
      ...baseConfig,
      segments: baseConfig.segments.map((s) => {
        if (s.id === 'seg_wan') {
          return { ...s, addressing: { ...s.addressing, ipv6: { mode: 'static', addresses: ['2001:db8:1::2/64'], gateway: '2001:db8:1::1' } } };
        }
        if (s.id === 'seg_lan') {
          return { ...s, addressing: { ...s.addressing, ipv6: { mode: 'static', addresses: ['2001:db8:100::1/64'] } } };
        }
        return s;
      }),
      dhcp6: [{
        id: 'dh6_lan',
        name: 'LAN PD',
        segment: 'seg_lan',
        prefixDelegation: { prefix: '2001:db8:100::/56', delegatedLength: 60 },
      }],
    };
    const unleasedPd = simulateTrafficFlow(
      { ingressSegmentId: 'seg_lan', protocol: 'tcp', srcIp: '2001:db8:100::50', dstIp: '2001:db8:100:20::99', dstPort: 443 },
      dhcp6Pool,
    );
    expect(unleasedPd.verdict).toBe('no_route');
    expect(unleasedPd.decidedBy).toEqual({ label: 'DHCPv6 LAN PD', to: '/network/dhcp' });

    // A more specific connected /64 inside the /56 pool still beats the covering unreachable route.
    const connectedInsidePool = simulateTrafficFlow(
      { ingressSegmentId: 'seg_lan', protocol: 'tcp', srcIp: '2001:db8:100::50', dstIp: '2001:db8:100::99', dstPort: 443 },
      dhcp6Pool,
    );
    expect(connectedInsidePool.chain).toBe('forward');
    expect(connectedInsidePool.steps.find((s) => s.stage === 'routing')?.summary).toMatch(/Directly connected subnet 2001:db8:100::1\/64/);
  });
});
