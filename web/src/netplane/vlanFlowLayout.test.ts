// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest';
import type { Device, TopologyGraph, VLANFlow, VLANFlowVLAN } from './api';
import { computeAllVlansLayout, computeSingleVlanLayout } from './vlanFlowLayout';

function makeDevice(id: string, name: string, role = 'switch'): Device {
  return {
    id,
    driver: 'openwrt',
    target_id: 't1',
    name,
    role,
    macs: [],
    firmware: {},
    uptime_sec: 100,
    online: true,
    last_seen: '2026-01-01T00:00:00Z',
    capabilities: [],
    system_stats: {},
    sensors: [],
  };
}

describe('computeSingleVlanLayout', () => {
  it('places gateways zones/segments first, then devices by hop distance from gateway devices, then endpoints', () => {
    const vlan10: VLANFlowVLAN = {
      id: 10,
      name: 'LAN',
      gateways: [
        {
          device_id: 'fw-core',
          segment_id: 'seg-10',
          segment_name: 'bond0.10',
          parent_link: 'bond0',
          zone_id: 'z-lan',
          zone_name: 'lan',
          addresses: [{ family: 'ipv4', address: '10.10.10.1', prefix_length: 24 }],
        },
      ],
      devices: ['fw-core', 'sw-core', 'sw-office', 'ap-hallway', 'ap-garage'],
      reached: ['fw-core', 'sw-core', 'sw-office', 'ap-hallway', 'ap-garage'],
      hops: [
        { edge_id: 'e1', from_id: 'fw-core', from_link: 'bond0', to_id: 'sw-core', to_link: 'bond0', kind: 'lag', from_mode: 'tagged', to_mode: 'tagged', state: 'carried', from_tagged: [10], to_tagged: [10] },
        { edge_id: 'e2', from_id: 'sw-core', from_link: 'lan1', to_id: 'sw-office', to_link: 'lan1', kind: 'wired', from_mode: 'tagged', to_mode: 'tagged', state: 'carried', from_tagged: [10], to_tagged: [10] },
        { edge_id: 'e3', from_id: 'sw-core', from_link: 'lan3', to_id: 'ap-hallway', to_link: 'eth0', kind: 'wired', from_mode: 'tagged', to_mode: 'tagged', state: 'carried', from_tagged: [10], to_tagged: [10] },
        { edge_id: 'e4', from_id: 'ap-hallway', to_id: 'ap-garage', kind: 'wireless_uplink', from_mode: 'unknown', to_mode: 'unknown', state: 'unverified', from_tagged: [], to_tagged: [] },
      ],
      access_ports: [
        { device_id: 'sw-office', link: 'lan2', label: 'Workstation', oper_state: 'up', reachable: true },
      ],
      ssids: [
        { device_id: 'ap-hallway', radio: 'radio0', name: 'Home-Core', client_count: 5, reachable: true },
        { device_id: 'ap-garage', radio: 'radio0', name: 'Home-Core', client_count: 2, reachable: true },
      ],
      clients: 8,
    };

    const flow: VLANFlow = {
      generated_at: '2026-01-01T00:00:00Z',
      vlans: [vlan10],
      findings: [],
    };

    const devicesById = new Map<string, Device>([
      ['fw-core', makeDevice('fw-core', 'fw-core', 'firewall')],
      ['sw-core', makeDevice('sw-core', 'sw-core', 'switch')],
      ['sw-office', makeDevice('sw-office', 'sw-office', 'switch')],
      ['ap-hallway', makeDevice('ap-hallway', 'ap-hallway', 'ap')],
      ['ap-garage', makeDevice('ap-garage', 'ap-garage', 'ap')],
    ]);

    const layout = computeSingleVlanLayout(vlan10, flow, devicesById);

    // Expect 6 columns: segments, hop 0 (fw-core), hop 1 (sw-core), hop 2 (sw-office, ap-hallway), hop 3 (ap-garage), endpoints.
    expect(layout.columns.map((c) => c.kind)).toEqual([
      'segments',
      'devices',
      'devices',
      'devices',
      'devices',
      'endpoints',
    ]);
    expect(layout.columns.slice(1, 5).map((c) => c.hopDistance)).toEqual([0, 1, 2, 3]);

    const byDev = new Map(layout.deviceCards.map((c) => [c.deviceId, c]));
    expect(byDev.get('fw-core')?.hopDistance).toBe(0);
    expect(byDev.get('sw-core')?.hopDistance).toBe(1);
    expect(byDev.get('sw-office')?.hopDistance).toBe(2);
    expect(byDev.get('ap-hallway')?.hopDistance).toBe(2);
    expect(byDev.get('ap-garage')?.hopDistance).toBe(3);

    // Strictly left-to-right column X positions.
    for (let i = 1; i < layout.columns.length; i++) {
      expect(layout.columns[i].x).toBeGreaterThan(layout.columns[i - 1].x + layout.columns[i - 1].width);
    }

    // Wireless uplink must stay unverified, never dead_end.
    const meshRibbon = layout.ribbons.find((r) => r.edgeId === 'e4');
    expect(meshRibbon).toBeDefined();
    expect(meshRibbon?.state).toBe('unverified');
    expect(meshRibbon?.fromMode).toBe('unknown');
    expect(meshRibbon?.toMode).toBe('unknown');

    const apHallChips = byDev.get('ap-hallway')!.rightPorts;
    expect(apHallChips.some((c) => c.hopEdgeId === 'e4' && c.mode === 'unknown' && c.state === 'unverified')).toBe(true);
  });

  it('adds foreign zone card and leaked endpoints card when a leak finding involves the VLAN', () => {
    const vlan10: VLANFlowVLAN = {
      id: 10,
      name: 'LAN',
      gateways: [{ device_id: 'fw', segment_id: 's10', segment_name: 'eth0.10', zone_name: 'lan', addresses: [{ family: 'ipv4', address: '10.0.10.1', prefix_length: 24 }] }],
      devices: ['fw', 'sw1', 'sw2'],
      reached: ['fw', 'sw1', 'sw2'],
      hops: [
        { edge_id: 'e1', from_id: 'fw', from_link: 'eth0', to_id: 'sw1', to_link: 'p1', kind: 'wired', from_mode: 'tagged', to_mode: 'tagged', state: 'carried', from_tagged: [10], to_tagged: [10] },
        { edge_id: 'e2', from_id: 'sw1', from_link: 'p2', to_id: 'sw2', to_link: 'p1', kind: 'wired', from_mode: 'untagged', to_mode: 'none', state: 'leak', from_pvid: 10, from_tagged: [], to_pvid: 20, to_tagged: [] },
      ],
      access_ports: [],
      ssids: [],
      clients: 0,
    };
    const vlan20: VLANFlowVLAN = {
      id: 20,
      name: 'IoT',
      gateways: [{ device_id: 'fw', segment_id: 's20', segment_name: 'eth0.20', zone_name: 'iot', addresses: [{ family: 'ipv4', address: '10.0.20.1', prefix_length: 24 }] }],
      devices: ['fw', 'sw1', 'sw2'],
      reached: ['fw', 'sw1'],
      hops: [
        { edge_id: 'e1', from_id: 'fw', from_link: 'eth0', to_id: 'sw1', to_link: 'p1', kind: 'wired', from_mode: 'tagged', to_mode: 'tagged', state: 'carried', from_tagged: [20], to_tagged: [20] },
        { edge_id: 'e2', from_id: 'sw1', from_link: 'p2', to_id: 'sw2', to_link: 'p1', kind: 'wired', from_mode: 'none', to_mode: 'untagged', state: 'leak', from_pvid: 10, from_tagged: [], to_pvid: 20, to_tagged: [] },
      ],
      access_ports: [{ device_id: 'sw2', link: 'p3', label: 'Camera', oper_state: 'up', reachable: false }],
      ssids: [],
      clients: 1,
    };
    const flow: VLANFlow = {
      generated_at: '2026-01-01T00:00:00Z',
      vlans: [vlan10, vlan20],
      findings: [
        {
          kind: 'leak',
          severity: 'crit',
          vlans: [10, 20],
          edge_id: 'e2',
          device_id: 'sw1',
          link: 'p2',
          peer_id: 'sw2',
          peer_link: 'p1',
          message: 'Untagged frames cross between VLAN 10 on sw1:p2 and VLAN 20 on sw2:p1',
          stranded: [],
        },
      ],
    };

    const layout = computeSingleVlanLayout(vlan10, flow);
    expect(layout.segmentCards.some((c) => c.kind === 'foreign_leak' && c.vlanId === 20)).toBe(true);
    expect(layout.endpointCards.some((c) => c.isForeignLeak && c.vlanId === 20 && c.deviceId === 'sw2')).toBe(true);
    const leakRibbon = layout.ribbons.find((r) => r.edgeId === 'e2');
    expect(leakRibbon?.state).toBe('leak');
    expect(leakRibbon?.foreignVlanId).toBe(20);
  });

  it('handles L2-only VLANs without gateways and places devices using topology roots', () => {
    const vlan99: VLANFlowVLAN = {
      id: 99,
      gateways: [],
      devices: ['sw-a', 'sw-b', 'sw-c'],
      reached: [],
      hops: [
        { edge_id: 'e1', from_id: 'sw-a', from_link: 'p1', to_id: 'sw-b', to_link: 'p1', kind: 'wired', from_mode: 'tagged', to_mode: 'tagged', state: 'carried', from_tagged: [99], to_tagged: [99] },
        { edge_id: 'e2', from_id: 'sw-b', from_link: 'p2', to_id: 'sw-c', to_link: 'p1', kind: 'wired', from_mode: 'tagged', to_mode: 'none', state: 'dead_end', from_tagged: [99], to_tagged: [] },
      ],
      access_ports: [],
      ssids: [],
      clients: 0,
    };
    const topo: TopologyGraph = {
      generated_at: '2026-01-01T00:00:00Z',
      root_ids: ['sw-a'],
      nodes: [
        { id: 'sw-a', kind: 'device', label: 'sw-a', online: true },
        { id: 'sw-b', kind: 'device', label: 'sw-b', online: true },
        { id: 'sw-c', kind: 'device', label: 'sw-c', online: true },
      ],
      edges: [
        { id: 'e1', from_id: 'sw-a', to_id: 'sw-b', kind: 'wired', evidence: 'lldp', confidence: 1, member_links: [] },
        { id: 'e2', from_id: 'sw-b', to_id: 'sw-c', kind: 'wired', evidence: 'lldp', confidence: 1, member_links: [] },
      ],
    };
    const layout = computeSingleVlanLayout(vlan99, { generated_at: '', vlans: [vlan99], findings: [] }, undefined, topo);
    // No segments column when gateways is empty and no foreign leak.
    expect(layout.columns.every((c) => c.kind === 'devices')).toBe(true);
    const byDev = new Map(layout.deviceCards.map((c) => [c.deviceId, c]));
    expect(byDev.get('sw-a')?.hopDistance).toBe(0);
    expect(byDev.get('sw-b')?.hopDistance).toBe(1);
    expect(byDev.get('sw-c')?.hopDistance).toBe(2);
  });

  it('scales cleanly to 16 devices across 4 hop tiers without vertical card overlap', () => {
    const devices = ['gw'];
    const hops: VLANFlowVLAN['hops'] = [];
    for (let core = 1; core <= 3; core++) {
      const cId = `core-${core}`;
      devices.push(cId);
      hops.push({ edge_id: `e-gw-${cId}`, from_id: 'gw', from_link: `p${core}`, to_id: cId, to_link: 'up', kind: 'wired', from_mode: 'tagged', to_mode: 'tagged', state: 'carried', from_tagged: [100], to_tagged: [100] });
      for (let dist = 1; dist <= 4; dist++) {
        const dId = `dist-${core}-${dist}`;
        devices.push(dId);
        hops.push({ edge_id: `e-${cId}-${dId}`, from_id: cId, from_link: `d${dist}`, to_id: dId, to_link: 'up', kind: 'wired', from_mode: 'tagged', to_mode: 'tagged', state: 'carried', from_tagged: [100], to_tagged: [100] });
      }
    }
    const vlan: VLANFlowVLAN = {
      id: 100,
      name: 'Corp',
      gateways: [{ device_id: 'gw', segment_id: 's100', segment_name: 'vlan100', zone_name: 'corp', addresses: [{ family: 'ipv4', address: '10.100.0.1', prefix_length: 24 }] }],
      devices,
      reached: devices,
      hops,
      access_ports: [{ device_id: 'dist-1-1', link: 'p1', oper_state: 'up', reachable: true }],
      ssids: [],
      clients: 42,
    };

    const layout = computeSingleVlanLayout(vlan, { generated_at: '', vlans: [vlan], findings: [] });
    expect(layout.deviceCards).toHaveLength(16);

    // Verify no vertical overlap within any column.
    const byCol = new Map<number, typeof layout.deviceCards>();
    for (const c of layout.deviceCards) {
      byCol.set(c.columnIndex, [...(byCol.get(c.columnIndex) ?? []), c]);
    }
    for (const colCards of byCol.values()) {
      const sorted = [...colCards].sort((a, b) => a.y - b.y);
      for (let i = 1; i < sorted.length; i++) {
        expect(sorted[i].y).toBeGreaterThanOrEqual(sorted[i - 1].y + sorted[i - 1].height);
      }
    }
  });
});

describe('computeSingleVlanLayout endpoint rows', () => {
  it('flags each unreachable access port on a card that mixes reachable and unreachable ports', () => {
    const vlan: VLANFlowVLAN = {
      id: 10,
      gateways: [{ device_id: 'fw', segment_id: 's10', segment_name: 'vlan10', addresses: [] }],
      devices: ['fw', 'sw1'],
      reached: ['fw', 'sw1'],
      hops: [{ edge_id: 'e1', from_id: 'fw', to_id: 'sw1', kind: 'wired', from_mode: 'tagged', to_mode: 'tagged', state: 'carried', from_tagged: [10], to_tagged: [10] }],
      access_ports: [
        { device_id: 'sw1', link: 'p2', oper_state: 'up', reachable: true },
        { device_id: 'sw1', link: 'p3', oper_state: 'up', reachable: false },
      ],
      ssids: [],
      clients: 0,
    };
    const layout = computeSingleVlanLayout(vlan, { generated_at: '2026-01-01T00:00:00Z', vlans: [vlan], findings: [] }, new Map());
    const card = layout.endpointCards.find((c) => c.deviceId === 'sw1')!;
    expect(card.allUnreachable).toBe(false);
    expect(card.lines.map((l) => [l.text, l.tone])).toEqual([
      ['[U] p2', 'normal'],
      ['[U] p3', 'warn'],
    ]);
  });
});

describe('computeAllVlansLayout', () => {
  it('aggregates multiple VLANs into parallel edge bundles across hop-ordered device columns', () => {
    const flow: VLANFlow = {
      generated_at: '2026-01-01T00:00:00Z',
      vlans: [
        {
          id: 10,
          name: 'LAN',
          gateways: [{ device_id: 'fw', segment_id: 's10', segment_name: 'vlan10', zone_name: 'lan', addresses: [{ family: 'ipv4', address: '10.0.10.1', prefix_length: 24 }] }],
          devices: ['fw', 'sw1'],
          reached: ['fw', 'sw1'],
          hops: [{ edge_id: 'e1', from_id: 'fw', from_link: 'eth0', to_id: 'sw1', to_link: 'p1', kind: 'wired', from_mode: 'tagged', to_mode: 'tagged', state: 'carried', from_tagged: [10], to_tagged: [10] }],
          access_ports: [{ device_id: 'sw1', link: 'p2', oper_state: 'up', reachable: true }],
          ssids: [],
          clients: 2,
        },
        {
          id: 20,
          name: 'Guest',
          gateways: [{ device_id: 'fw', segment_id: 's20', segment_name: 'vlan20', zone_name: 'guest', addresses: [{ family: 'ipv4', address: '10.0.20.1', prefix_length: 24 }] }],
          devices: ['fw', 'sw1'],
          reached: ['fw'],
          hops: [{ edge_id: 'e1', from_id: 'fw', from_link: 'eth0', to_id: 'sw1', to_link: 'p1', kind: 'wired', from_mode: 'tagged', to_mode: 'none', state: 'dead_end', from_tagged: [20], to_tagged: [] }],
          access_ports: [{ device_id: 'sw1', link: 'p3', oper_state: 'up', reachable: false }],
          ssids: [],
          clients: 0,
        },
      ],
      findings: [
        {
          kind: 'dead_end',
          severity: 'warn',
          vlans: [20],
          edge_id: 'e1',
          device_id: 'fw',
          link: 'eth0',
          peer_id: 'sw1',
          peer_link: 'p1',
          message: 'VLAN 20 is tagged on fw:eth0 but missing on sw1:p1',
          stranded: ['sw1'],
        },
      ],
    };

    const layout = computeAllVlansLayout(flow);
    expect(layout.columns.map((c) => c.kind)).toEqual(['vlans', 'devices', 'devices', 'endpoints']);
    expect(layout.vlanCards).toHaveLength(2);
    expect(layout.edgeBundles).toHaveLength(1);
    expect(layout.edgeBundles[0].lanes).toHaveLength(2);
    expect(layout.edgeBundles[0].findings).toHaveLength(1);
    expect(layout.endpointCards).toHaveLength(1);
    expect(layout.endpointCards[0].hasUnreachable).toBe(true);
  });

  it('reserves room for a same-column hop pill when there is no endpoint column', () => {
    const gw = { device_id: 'fw', segment_id: 's10', segment_name: 'vlan10', addresses: [] };
    const hop = (id: string, from: string, to: string, state: 'carried' | 'unverified') => ({
      edge_id: id, from_id: from, to_id: to, kind: 'wired', from_mode: 'tagged' as const, to_mode: 'tagged' as const,
      state, from_tagged: [10], to_tagged: [10],
    });
    const flow: VLANFlow = {
      generated_at: '2026-01-01T00:00:00Z',
      vlans: [
        {
          id: 10,
          gateways: [gw],
          devices: ['fw', 'a', 'b'],
          reached: ['fw', 'a', 'b'],
          // a and b are both one hop from the gateway, so the a-b hop stays inside the last column.
          hops: [hop('e1', 'fw', 'a', 'carried'), hop('e2', 'fw', 'b', 'carried'), hop('e3', 'a', 'b', 'unverified')],
          access_ports: [],
          ssids: [],
          clients: 0,
        },
      ],
      findings: [],
    };

    const layout = computeAllVlansLayout(flow);
    expect(layout.columns.map((c) => c.kind)).not.toContain('endpoints');
    const sameCol = layout.edgeBundles.find((b) => b.edgeId === 'e3')!;
    expect(sameCol.sameColumn).toBe(true);
    // The pill centre sits 46 units right of the column and is 88 wide.
    expect(layout.width).toBeGreaterThanOrEqual(Math.max(sameCol.x1, sameCol.x2) + 46 + 44);
  });

  it('anchors an L2-only VLAN to its leftmost participating device instead of alphabetically first', () => {
    const flow: VLANFlow = {
      generated_at: '2026-01-01T00:00:00Z',
      vlans: [
        {
          id: 10,
          gateways: [{ device_id: 'gw', segment_id: 's10', segment_name: 'lan', addresses: [] }],
          devices: ['gw', 'sw-core', 'ap-edge'],
          reached: ['gw', 'sw-core', 'ap-edge'],
          hops: [
            { edge_id: 'e1', from_id: 'gw', to_id: 'sw-core', kind: 'wired', from_mode: 'tagged', to_mode: 'tagged', state: 'carried', from_tagged: [10], to_tagged: [10] },
            { edge_id: 'e2', from_id: 'sw-core', to_id: 'ap-edge', kind: 'wired', from_mode: 'tagged', to_mode: 'tagged', state: 'carried', from_tagged: [10], to_tagged: [10] },
          ],
          access_ports: [],
          ssids: [],
          clients: 0,
        },
        {
          id: 99,
          gateways: [],
          // Alphabetically 'ap-edge' comes before 'sw-core', but 'sw-core' is closer to Col 0.
          devices: ['ap-edge', 'sw-core'],
          reached: [],
          hops: [
            { edge_id: 'e2', from_id: 'sw-core', to_id: 'ap-edge', kind: 'wired', from_mode: 'tagged', to_mode: 'tagged', state: 'carried', from_tagged: [99], to_tagged: [99] },
          ],
          access_ports: [],
          ssids: [],
          clients: 0,
        },
      ],
      findings: [],
    };
    const layout = computeAllVlansLayout(flow);
    const l2Link = layout.segmentLinks.find((l) => l.vlanId === 99);
    expect(l2Link?.deviceId).toBe('sw-core');
  });
});
