import { nextPppoeSecretName } from './interfaceEdit';
import { firewallZone, type ConfigBatchOperation } from './types';

export interface SetupNetworkDraft {
  mgmtMac: string;
  mgmtName: string;
  mgmtIpv4Mode: 'static' | 'dhcp';
  mgmtIpv4Address: string;
  mgmtIpv6Mode: 'none' | 'static' | 'delegated';
  mgmtIpv6Address: string;
  mgmtIpv6RaMode: 'unmanaged' | 'router' | 'disabled';
  serveDhcp: boolean;
  dhcpStart: string;
  dhcpEnd: string;
  serveDns: boolean;
  wanMac: string;
  wanName: string;
  wanIpv4Mode: 'dhcp' | 'pppoe' | 'static' | 'none';
  wanIpv4Address: string;
  wanIpv4Gateway: string;
  wanPppoeUsername?: string;
  wanPppoePassword?: string;
  wanPppoeVlanId?: string;
  wanIpv6Mode: 'none' | 'dhcp6' | 'slaac' | 'static';
  wanIpv6Address: string;
  wanIpv6Gateway: string;
  wanDhcp6PrefixLen: string;
  wanDnsServers: string;
}

export interface SetupSingletonVersions {
  management?: string;
  dns?: string;
  natOutbound?: string;
  existingSecrets?: string[];
}

/** Suggest a default .100-.199 DHCP range when the management IPv4 address is a /24. */
export function defaultDhcpRange(cidr: string): { start: string; end: string } | null {
  const match = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\/24$/.exec(cidr.trim());
  if (!match) return null;
  const [, a, b, c] = match;
  return { start: `${a}.${b}.${c}.100`, end: `${a}.${b}.${c}.199` };
}

export function effectiveMgmtIpv6Mode(draft: SetupNetworkDraft): 'none' | 'static' | 'delegated' {
  if (draft.mgmtIpv6Mode === 'delegated' && (!draft.wanMac || draft.wanIpv6Mode !== 'dhcp6')) {
    return 'none';
  }
  return draft.mgmtIpv6Mode;
}

function parseDnsServers(raw: string): string[] {
  return raw.split(/[\s,]+/).filter(Boolean);
}

/** Build the atomic /api/v1/config/changes batch for the setup assistant's network choices. */
export function buildSetupOperations(
  draft: SetupNetworkDraft,
  versions: SetupSingletonVersions = {},
): ConfigBatchOperation[] {
  const ops: ConfigBatchOperation[] = [
    { op: 'create', collection: 'zones', localId: 'lan_zone', value: { name: 'LAN' } },
    {
      op: 'create',
      collection: 'links',
      localId: 'mgmt_link',
      value: { name: draft.mgmtName || 'lan0', kind: 'ethernet', permanentMac: draft.mgmtMac.toLowerCase(), mtu: 1500, enabled: true },
    },
  ];

  const hasWan = Boolean(draft.wanMac);
  const servingDns = draft.mgmtIpv4Mode === 'static' && draft.serveDns;
  const dnsList = parseDnsServers(draft.wanDnsServers);

  if (hasWan) {
    const isPppoe = draft.wanIpv4Mode === 'pppoe';
    const effectiveWanIpv6Mode = isPppoe && draft.wanIpv6Mode === 'static' ? 'none' : draft.wanIpv6Mode;
    const wanIpv4: Record<string, unknown> =
      isPppoe
        ? { mode: 'ppp' }
        : draft.wanIpv4Mode === 'static'
          ? { mode: 'static', addresses: [draft.wanIpv4Address.trim()], gateway: draft.wanIpv4Gateway.trim() }
          : { mode: draft.wanIpv4Mode };
    const wanIpv6: Record<string, unknown> =
      effectiveWanIpv6Mode === 'static'
        ? { mode: 'static', addresses: [draft.wanIpv6Address.trim()], gateway: draft.wanIpv6Gateway.trim() }
        : effectiveWanIpv6Mode === 'dhcp6'
          ? {
              mode: 'dhcp6',
              ...(draft.wanDhcp6PrefixLen.trim() ? { requestPrefixLength: Number(draft.wanDhcp6PrefixLen.trim()) } : {}),
            }
          : { mode: effectiveWanIpv6Mode };
    const wanAddressing: Record<string, unknown> = { ipv4: wanIpv4, ipv6: wanIpv6 };
    // When the DNS service is enabled, Andaru requires upstream DNS servers on dns.upstreams
    // rather than on the WAN segment.
    if (!servingDns && (draft.wanIpv4Mode === 'static' || effectiveWanIpv6Mode === 'static') && dnsList.length > 0) {
      wanAddressing.dns = { servers: dnsList };
    }
    ops.push({ op: 'create', collection: 'zones', localId: 'wan_zone', value: { name: 'WAN' } });
    if (isPppoe) {
      const wanBaseName = draft.wanName || 'wan0';
      ops.push({
        op: 'create',
        collection: 'links',
        localId: 'wan_port',
        value: { name: wanBaseName, kind: 'ethernet', permanentMac: draft.wanMac.toLowerCase(), mtu: 1500, enabled: true },
      });
      const rawVlan = (draft.wanPppoeVlanId ?? '').trim();
      const vlanId = /^\d+$/.test(rawVlan) ? Number(rawVlan) : 0;
      let pppoeParent = '@wan_port';
      if (vlanId >= 1 && vlanId <= 4094) {
        ops.push({
          op: 'create',
          collection: 'links',
          localId: 'wan_vlan',
          value: { name: `${wanBaseName}.${vlanId}`, kind: 'vlan', parent: '@wan_port', vlanId, mtu: 1500, enabled: true },
        });
        pppoeParent = '@wan_vlan';
      }
      ops.push({
        op: 'create',
        collection: 'links',
        localId: 'wan_link',
        value: {
          name: 'WAN PPPoE',
          kind: 'pppoe',
          parent: pppoeParent,
          mtu: 1492,
          enabled: true,
          username: (draft.wanPppoeUsername ?? '').trim(),
          password: {
            secret: nextPppoeSecretName('WAN', versions.existingSecrets),
            set: draft.wanPppoePassword ?? '',
            expectedVersion: '0',
          },
        },
      });
    } else {
      ops.push({
        op: 'create',
        collection: 'links',
        localId: 'wan_link',
        value: { name: draft.wanName || 'wan0', kind: 'ethernet', permanentMac: draft.wanMac.toLowerCase(), mtu: 1500, enabled: true },
      });
    }
    ops.push({
      op: 'create',
      collection: 'segments',
      localId: 'wan_seg',
      value: { name: 'WAN', link: '@wan_link', zone: '@wan_zone', addressing: wanAddressing },
    });
  }

  const mgmtIpv4: Record<string, unknown> =
    draft.mgmtIpv4Mode === 'static'
      ? { mode: 'static', addresses: [draft.mgmtIpv4Address.trim()] }
      : { mode: 'dhcp' };
  const ipv6Mode = effectiveMgmtIpv6Mode(draft);
  const mgmtIpv6: Record<string, unknown> =
    ipv6Mode === 'static'
      ? { mode: 'static', addresses: [draft.mgmtIpv6Address.trim()], ra: { mode: draft.mgmtIpv6RaMode } }
      : ipv6Mode === 'delegated'
        ? { mode: 'delegated', from: '@wan_seg', subnetId: '0', ra: { mode: draft.mgmtIpv6RaMode } }
        : { mode: 'none' };

  ops.push(
    {
      op: 'create',
      collection: 'segments',
      localId: 'mgmt_seg',
      value: { name: 'Management LAN', link: '@mgmt_link', zone: '@lan_zone', addressing: { ipv4: mgmtIpv4, ipv6: mgmtIpv6 } },
    },
    {
      op: 'replace',
      collection: 'management',
      expectedVersion: versions.management ?? '0',
      value: { segment: '@mgmt_seg' },
    },
  );

  if (draft.mgmtIpv4Mode === 'static' && draft.serveDhcp) {
    ops.push({
      op: 'create',
      collection: 'dhcp',
      value: {
        name: 'Management DHCP',
        segment: '@mgmt_seg',
        ranges: [{ first: draft.dhcpStart.trim(), last: draft.dhcpEnd.trim() }],
      },
    });
  }
  if (servingDns) {
    const wanHasStatic = draft.wanIpv4Mode === 'static' || (draft.wanIpv4Mode !== 'pppoe' && draft.wanIpv6Mode === 'static');
    const useForward = hasWan && wanHasStatic && dnsList.length > 0;
    ops.push({
      op: 'replace',
      collection: 'dns',
      expectedVersion: versions.dns ?? '0',
      value: useForward
        ? { mode: 'forward', upstreams: dnsList.map((address) => ({ address, protocol: 'do53' })), segments: ['@mgmt_seg'] }
        : { mode: 'recursive', segments: ['@mgmt_seg'] },
    });
  }
  if (hasWan) {
    ops.push({
      op: 'replace',
      collection: 'nat.outbound',
      expectedVersion: versions.natOutbound ?? '0',
      value: { mode: 'automatic' },
    });
  }

  // Setting management.segment enables default-drop input/forward nftables chains; add baseline
  // rules so HTTPS management stays reachable/confirmable, LAN DNS works when enabled, and LAN
  // clients can forward out to WAN.
  ops.push(
    {
      op: 'create',
      collection: 'aliases.ports',
      localId: 'port_https',
      value: { name: 'HTTPS', protocol: 'tcp', ports: ['443'] },
    },
    {
      op: 'create',
      collection: 'rules',
      value: {
        name: 'Allow WebUI on LAN',
        enabled: true,
        action: 'accept',
        from: { zones: ['@lan_zone'], addresses: [] },
        to: { zones: [firewallZone], addresses: [] },
        ports: ['@port_https'],
        log: false,
      },
    },
  );
  if (servingDns) {
    ops.push(
      {
        op: 'create',
        collection: 'aliases.ports',
        localId: 'port_dns_udp',
        value: { name: 'DNS (UDP)', protocol: 'udp', ports: ['53'] },
      },
      {
        op: 'create',
        collection: 'aliases.ports',
        localId: 'port_dns_tcp',
        value: { name: 'DNS (TCP)', protocol: 'tcp', ports: ['53'] },
      },
      {
        op: 'create',
        collection: 'rules',
        value: {
          name: 'Allow DNS on LAN',
          enabled: true,
          action: 'accept',
          from: { zones: ['@lan_zone'], addresses: [] },
          to: { zones: [firewallZone], addresses: [] },
          ports: ['@port_dns_udp', '@port_dns_tcp'],
          log: false,
        },
      },
    );
  }
  if (hasWan) {
    ops.push({
      op: 'create',
      collection: 'rules',
      value: {
        name: 'Allow LAN to WAN',
        enabled: true,
        action: 'accept',
        from: { zones: ['@lan_zone'], addresses: [] },
        to: { zones: ['@wan_zone'], addresses: [] },
        ports: [],
        log: false,
      },
    });
  }
  return ops;
}
