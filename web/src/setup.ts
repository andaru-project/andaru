import type { ConfigBatchOperation } from './types';

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
  wanIpv4Mode: 'dhcp' | 'static' | 'none';
  wanIpv4Address: string;
  wanIpv4Gateway: string;
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
    const wanIpv4: Record<string, unknown> =
      draft.wanIpv4Mode === 'static'
        ? { mode: 'static', addresses: [draft.wanIpv4Address.trim()], gateway: draft.wanIpv4Gateway.trim() }
        : { mode: draft.wanIpv4Mode };
    const wanIpv6: Record<string, unknown> =
      draft.wanIpv6Mode === 'static'
        ? { mode: 'static', addresses: [draft.wanIpv6Address.trim()], gateway: draft.wanIpv6Gateway.trim() }
        : draft.wanIpv6Mode === 'dhcp6'
          ? {
              mode: 'dhcp6',
              ...(draft.wanDhcp6PrefixLen.trim() ? { requestPrefixLength: Number(draft.wanDhcp6PrefixLen.trim()) } : {}),
            }
          : { mode: draft.wanIpv6Mode };
    const wanAddressing: Record<string, unknown> = { ipv4: wanIpv4, ipv6: wanIpv6 };
    // When the DNS service is enabled, Andaru requires upstream DNS servers on dns.upstreams
    // rather than on the WAN segment.
    if (!servingDns && (draft.wanIpv4Mode === 'static' || draft.wanIpv6Mode === 'static') && dnsList.length > 0) {
      wanAddressing.dns = { servers: dnsList };
    }
    ops.push(
      { op: 'create', collection: 'zones', localId: 'wan_zone', value: { name: 'WAN' } },
      {
        op: 'create',
        collection: 'links',
        localId: 'wan_link',
        value: { name: draft.wanName || 'wan0', kind: 'ethernet', permanentMac: draft.wanMac.toLowerCase(), mtu: 1500, enabled: true },
      },
      {
        op: 'create',
        collection: 'segments',
        localId: 'wan_seg',
        value: { name: 'WAN', link: '@wan_link', zone: '@wan_zone', addressing: wanAddressing },
      },
    );
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
    const useForward = hasWan && (draft.wanIpv4Mode === 'static' || draft.wanIpv6Mode === 'static') && dnsList.length > 0;
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
  return ops;
}
