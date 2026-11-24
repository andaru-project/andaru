// SPDX-License-Identifier: AGPL-3.0-or-later
import { createTheme, MantineProvider } from '@mantine/core';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { MemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';
import { api } from './api';
import type { Device, Link, NetworkState, TopologyEdge, VLANFlow, VLANFlowFinding, VLANFlowHop } from './api';
import { App } from './App';
import { PortFaceplate } from './components/PortFaceplate';
import { TopologyView } from './components/TopologyView';
import { VlanFlowCanvas } from './components/VlanFlowCanvas';
import { FindingCallout } from './components/ui';
import { OverviewScreen } from './screens/OverviewScreen';
import { VlanFlowScreen } from './screens/VlanFlowScreen';
import { vlanFlowPath } from './routing';
import { computeAllVlansLayout } from './vlanFlowLayout';
import { findingKey } from './vlanUtils';

const testTheme = createTheme({ primaryColor: 'teal', defaultRadius: 'sm' });

function wrap(ui: ReactNode, path = '/') {
  return render(
    <MantineProvider theme={testTheme}>
      <MemoryRouter initialEntries={[path]}>{ui}</MemoryRouter>
    </MantineProvider>,
  );
}

const emptyState: NetworkState = {
  devices: [],
  links: [],
  lag_groups: [],
  segments: [],
  zones: [],
  gateways: [],
  routes: [],
  vpns: [],
  firewalls: [],
  radios: [],
  wireless_uplinks: [],
  clients: [],
};

describe('OverviewScreen', () => {
  it('shows VLAN health as unavailable until a flow report loads', () => {
    wrap(
      <OverviewScreen state={emptyState} topology={null} flow={null} events={[]} sseConnected devicesById={new Map()} />,
    );
    expect(screen.getByText('Unavailable')).toBeInTheDocument();
    expect(screen.queryByText('Clean')).not.toBeInTheDocument();
    expect(screen.queryByText('All Clear')).not.toBeInTheDocument();
  });

  it('reports a loaded report without findings as clean', () => {
    wrap(
      <OverviewScreen
        state={emptyState}
        topology={null}
        flow={{ generated_at: '2026-01-01T00:00:00Z', vlans: [], findings: [] }}
        events={[]}
        sseConnected
        devicesById={new Map()}
      />,
    );
    expect(screen.getByText('Clean')).toBeInTheDocument();
    expect(screen.getByText('All Clear')).toBeInTheDocument();
  });
});

describe('FindingCallout', () => {
  const finding: VLANFlowFinding = {
    kind: 'dead_end',
    severity: 'warn',
    vlans: [],
    edge_id: 'e1',
    device_id: 'sw1',
    link: 'p1',
    peer_id: 'sw2',
    peer_link: 'p2',
    message: 'VLAN stops here',
    stranded: [],
  };

  it('runs onAction without a title, even with no VLAN to trace', () => {
    const onAction = vi.fn();
    wrap(<FindingCallout finding={finding} actionLabel="Select" onAction={onAction} />);
    fireEvent.click(screen.getByRole('button', { name: 'Select' }));
    expect(onAction).toHaveBeenCalledOnce();
  });

  it('falls back to the trace link without onAction', () => {
    wrap(<FindingCallout finding={{ ...finding, vlans: [20] }} actionLabel="Trace" />);
    expect(screen.getByRole('link', { name: 'Trace' }).getAttribute('href')).toContain('vlan=20');
  });
});

describe('VlanFlowScreen', () => {
  const vlan = (id: number) => ({
    id,
    gateways: [],
    devices: [],
    reached: [],
    hops: [],
    access_ports: [],
    ssids: [],
    clients: 0,
  });
  const finding20: VLANFlowFinding = {
    kind: 'dead_end',
    severity: 'warn',
    vlans: [20],
    edge_id: 'e1',
    device_id: 'sw1',
    link: 'p1',
    peer_id: 'sw2',
    peer_link: 'p2',
    message: 'VLAN 20 stops here',
    stranded: [],
  };
  const flow = { generated_at: '2026-01-01T00:00:00Z', vlans: [vlan(10), vlan(20)], findings: [finding20] };
  const renderAt = (vlanId: number) =>
    wrap(
      <VlanFlowScreen
        flow={flow}
        state={null}
        topology={null}
        devicesById={new Map()}
        vlansById={new Map(flow.vlans.map((v) => [v.id, v]))}
        vlanColors={new Map()}
      />,
      vlanFlowPath({ mode: 'single', vlanId, findingKey: findingKey(finding20) }),
    );

  it('ignores a finding URL param for another VLAN in single mode', () => {
    renderAt(10);
    expect(screen.queryByText('Consequence')).not.toBeInTheDocument();
  });

  it('shows the finding when it belongs to the selected VLAN', () => {
    renderAt(20);
    expect(screen.getByText('Consequence')).toBeInTheDocument();
  });
});

describe('PortFaceplate', () => {
  const device: Device = {
    id: 'sw1',
    driver: 'openwrt',
    target_id: 't1',
    name: 'Switch',
    role: 'switch',
    macs: [],
    firmware: {},
    uptime_sec: 1,
    online: true,
    last_seen: '2026-01-01T00:00:00Z',
    capabilities: [],
    system_stats: {},
    sensors: [],
  };
  const port = (pvid: number, extra: Partial<Link> = {}): Link => ({
    device_id: 'sw1',
    index: 1,
    if_name: 'lan1',
    admin_up: true,
    link_up: true,
    oper_state: 'up',
    speed_mbps: 1000,
    media: 'copper',
    is_fiber: false,
    pvid,
    tagged_vlans: [],
    ...extra,
  });
  const faceplate = (links: Link[]) => (
    <PortFaceplate
      device={device}
      links={links}
      lagGroups={[]}
      flow={null}
      state={null}
      topology={null}
      devicesById={new Map()}
      vlansById={new Map()}
      vlanColors={new Map()}
      selectedPort={null}
      onSelectPort={() => {}}
      onOpenDevice={() => {}}
      onOpenVlanFlow={() => {}}
    />
  );

  it('clears the VLAN highlight when a refresh removes that VLAN', () => {
    const { rerender } = wrap(faceplate([port(20)]));
    fireEvent.click(screen.getByRole('button', { name: '20' }));
    expect(screen.getByRole('button', { name: '20' })).toHaveAttribute('aria-pressed', 'true');
    expect(screen.getByRole('button', { name: 'None' })).toHaveAttribute('aria-pressed', 'false');

    rerender(
      <MantineProvider theme={testTheme}>
        <MemoryRouter>{faceplate([port(10)])}</MemoryRouter>
      </MantineProvider>,
    );
    expect(screen.getByRole('button', { name: 'None' })).toHaveAttribute('aria-pressed', 'true');
  });

  it('shows a missing PoE draw as unknown, not 0 W', () => {
    wrap(faceplate([port(10, { poe: { supported: true, enabled: true } })]));
    expect(screen.getByText(/PoE —/)).toBeInTheDocument();
    expect(screen.queryByText(/PoE 0\.0W/)).not.toBeInTheDocument();
  });
});

describe('TopologyView', () => {
  const node = (id: string) => ({ id, kind: 'device' as const, label: id, online: true });
  const edge = (id: string, from: string, to: string): TopologyEdge => ({
    id,
    from_id: from,
    from_link: `${id}a`,
    to_id: to,
    to_link: `${id}b`,
    kind: 'ethernet',
    evidence: 'lldp',
    confidence: 1,
    member_links: [],
  });
  const hop = (edgeId: string, from: string, to: string) => ({
    edge_id: edgeId,
    from_id: from,
    to_id: to,
    kind: 'ethernet',
    from_mode: 'tagged' as const,
    to_mode: 'tagged' as const,
    state: 'carried' as const,
    from_tagged: [10],
    to_tagged: [10],
  });
  // Two roots put both devices on one layer, so their parallel links arch side by side.
  const graph = {
    generated_at: '2026-01-01T00:00:00Z',
    root_ids: ['a', 'b'],
    nodes: [node('a'), node('b')],
    edges: [edge('e1', 'a', 'b'), edge('e2', 'a', 'b')],
  };
  const flow = {
    generated_at: '2026-01-01T00:00:00Z',
    vlans: [
      {
        id: 10,
        gateways: [],
        devices: ['a', 'b'],
        reached: [],
        hops: [hop('e1', 'a', 'b'), hop('e2', 'a', 'b')],
        access_ports: [],
        ssids: [],
        clients: 0,
      },
    ],
    findings: [],
  };
  const renderView = () =>
    wrap(
      <TopologyView
        graph={graph}
        state={null}
        flow={flow}
        devicesById={new Map()}
        vlanOverlay={10}
        showClients={false}
        showEvidence
        selection={null}
        onSelect={() => {}}
      />,
    );

  it('keeps discovery evidence on links with a VLAN overlay badge', () => {
    renderView();
    expect(screen.getByText(/✓ T · e1a↔e1b · LLDP/)).toBeInTheDocument();
  });

  it('separates the labels of parallel links on one layer by a full label height', () => {
    renderView();
    const labelY = (id: string) =>
      Number(screen.getByText(new RegExp(`${id}a↔${id}b`)).previousElementSibling?.getAttribute('y'));
    expect(Math.abs(labelY('e1') - labelY('e2'))).toBeGreaterThanOrEqual(20);
  });

  it('renders a graph wider than the minimum layout at its natural width', () => {
    const ids = Array.from({ length: 10 }, (_, i) => `d${i}`);
    wrap(
      <TopologyView
        graph={{ ...graph, root_ids: ids, nodes: ids.map(node), edges: [] }}
        state={null}
        flow={null}
        devicesById={new Map()}
        vlanOverlay={null}
        showClients={false}
        showEvidence
        selection={null}
        onSelect={() => {}}
      />,
    );
    const svg = screen.getByRole('group', { name: /topology graph/ });
    const naturalW = Number(svg.getAttribute('viewBox')?.split(' ')[2]);
    expect(naturalW).toBeGreaterThan(760);
    expect(svg.style.minWidth).toBe(`${naturalW}px`);
  });
});

describe('VlanFlowCanvas', () => {
  const gateway = { device_id: 'fw', segment_id: 's', segment_name: 'seg', addresses: [] };
  const hop = (over: Partial<VLANFlowHop>): VLANFlowHop => ({
    edge_id: 'e1',
    from_id: 'fw',
    from_link: 'eth0',
    to_id: 'sw1',
    to_link: 'p1',
    kind: 'wired',
    from_mode: 'tagged',
    to_mode: 'tagged',
    state: 'carried',
    from_tagged: [],
    to_tagged: [],
    ...over,
  });
  const vlan = (id: number, hops: VLANFlowHop[]) => ({
    id,
    gateways: [{ ...gateway, segment_id: `s${id}` }],
    devices: ['fw', 'sw1'],
    reached: ['fw'],
    hops,
    access_ports: [],
    ssids: [],
    clients: 0,
  });
  const draw = (mode: 'single' | 'all', flow: VLANFlow) =>
    wrap(
      <VlanFlowCanvas
        mode={mode}
        flow={flow}
        selectedVlanId={flow.vlans[0].id}
        selectedFindingKey={null}
        devicesById={new Map()}
        topology={null}
        vlanColors={new Map()}
        onSelectVlan={() => {}}
        onSelectFinding={() => {}}
        onOpenDevice={() => {}}
      />,
    );

  it('gives each All-VLANs lane with a finding its own keyboard stop, and no duplicate pill stop', () => {
    const dead: VLANFlowFinding = { kind: 'dead_end', severity: 'warn', vlans: [10], edge_id: 'e1', device_id: 'sw1', link: 'p1', peer_id: 'fw', peer_link: 'eth0', message: 'VLAN 10 dead end', stranded: [] };
    const mism: VLANFlowFinding = { ...dead, kind: 'tag_mismatch', vlans: [20], message: 'VLAN 20 mismatch' };
    const flow: VLANFlow = {
      generated_at: '2026-01-01T00:00:00Z',
      vlans: [
        vlan(10, [hop({ from_mode: 'none', to_mode: 'tagged', state: 'dead_end' })]),
        vlan(20, [hop({ to_mode: 'untagged', state: 'mismatch' })]),
        vlan(30, [hop({})]),
      ],
      findings: [dead, mism],
    };
    const onSelectFinding = vi.fn();
    wrap(
      <VlanFlowCanvas
        mode="all" flow={flow} selectedVlanId={10} selectedFindingKey={null} devicesById={new Map()} topology={null}
        vlanColors={new Map()} onSelectVlan={() => {}} onSelectFinding={onSelectFinding} onOpenDevice={() => {}}
      />,
    );
    const lanes = screen.getAllByRole('button', { name: /VLAN (10 dead end|20 mismatch)/ });
    expect(lanes).toHaveLength(2); // one stop per finding lane; the bundle pill is pointer-only
    lanes.forEach((l) => expect(l).toHaveAttribute('tabindex', '0'));
    fireEvent.keyDown(lanes[1], { key: 'Enter' });
    expect(onSelectFinding).toHaveBeenCalledWith(expect.objectContaining({ message: lanes[1].getAttribute('aria-label') }));
  });

  it('gives a leak spanning two lanes a single keyboard stop', () => {
    const leak: VLANFlowFinding = { kind: 'leak', severity: 'crit', vlans: [10, 20], edge_id: 'e1', device_id: 'fw', link: 'eth0', peer_id: 'sw1', peer_link: 'p1', message: 'VLAN 10 leaks into 20', stranded: [] };
    const flow: VLANFlow = {
      generated_at: '2026-01-01T00:00:00Z',
      vlans: [vlan(10, [hop({ to_mode: 'none', state: 'leak' })]), vlan(20, [hop({ from_mode: 'none', state: 'leak' })])],
      findings: [leak],
    };
    draw('all', flow);
    expect(screen.getAllByRole('button', { name: 'VLAN 10 leaks into 20' })).toHaveLength(1);
  });

  it('dashes the half of an All-VLANs dead-end lane that lacks the VLAN', () => {
    // The gateway side does not carry VLAN 10; the switch side tags it.
    const flow: VLANFlow = {
      generated_at: '2026-01-01T00:00:00Z',
      vlans: [vlan(10, [hop({ from_mode: 'none', to_mode: 'tagged', state: 'dead_end' })])],
      findings: [],
    };
    const { container } = draw('all', flow);
    const { x1 } = computeAllVlansLayout(flow).edgeBundles[0];
    const dashed = Array.from(container.querySelectorAll('path[stroke-dasharray="4,3"]'));
    expect(dashed).toHaveLength(1);
    expect(dashed[0].getAttribute('d')).toMatch(new RegExp(`^M ${x1} `));
  });

  it('paints leak ribbons with a user-space gradient so horizontal ribbons stay visible', () => {
    const leak: VLANFlowFinding = {
      kind: 'leak',
      severity: 'crit',
      vlans: [10, 20],
      edge_id: 'e1',
      device_id: 'fw',
      link: 'eth0',
      peer_id: 'sw1',
      peer_link: 'p1',
      message: 'leak',
      stranded: [],
    };
    const flow: VLANFlow = {
      generated_at: '2026-01-01T00:00:00Z',
      vlans: [vlan(10, [hop({ from_mode: 'untagged', to_mode: 'untagged', state: 'leak' })]), vlan(20, [])],
      findings: [leak],
    };
    const { container } = draw('single', flow);
    const grad = container.querySelector('linearGradient');
    expect(grad).toHaveAttribute('gradientUnits', 'userSpaceOnUse');
    expect(container.querySelector(`path[stroke="url(#${grad?.id})"]`)).not.toBeNull();
    expect(screen.getByText('⚠ LEAK 10⇄20')).toBeInTheDocument();
  });

  it('keeps same-column dead-end Bezier control points to the right of the card edge', () => {
    const flow: VLANFlow = {
      generated_at: '2026-01-01T00:00:00Z',
      vlans: [
        {
          ...vlan(10, [
            hop({ edge_id: 'e1', from_id: 'fw', to_id: 'a' }),
            hop({ edge_id: 'e2', from_id: 'fw', to_id: 'b' }),
            hop({ edge_id: 'e3', from_id: 'a', to_id: 'b', from_mode: 'tagged', to_mode: 'none', state: 'dead_end' }),
          ]),
          devices: ['fw', 'a', 'b'],
          reached: ['fw', 'a', 'b'],
        },
      ],
      findings: [],
    };
    const { container } = draw('single', flow);
    const dashed = container.querySelector('path[stroke-dasharray="4,4"]');
    expect(dashed).not.toBeNull();
    const nums = (dashed?.getAttribute('d') ?? '').match(/-?\d+(?:\.\d+)?/g)?.map(Number) ?? [];
    // M mx my C q2x q2y, m23x m23y, x2 y2 -> every X control point must stay >= x2 (the card's right edge).
    const x2 = nums[6];
    expect(nums[0]).toBeGreaterThan(x2);
    expect(nums[2]).toBeGreaterThan(x2);
    expect(nums[4]).toBeGreaterThan(x2);
  });

  it('keeps All-VLANs trunk lane spacing usable and non-overlapping with 14 VLANs', () => {
    const vlans = Array.from({ length: 14 }, (_, i) => vlan(10 + i, [hop({})]));
    const flow: VLANFlow = { generated_at: '2026-01-01T00:00:00Z', vlans, findings: [] };
    const { container } = draw('all', flow);
    const hitPaths = Array.from(container.querySelectorAll('path[stroke="transparent"]'));
    expect(hitPaths).toHaveLength(14);
    const ys = hitPaths.map((p) => Number((p.getAttribute('d') ?? '').split(' ')[2]));
    for (let i = 1; i < ys.length; i++) {
      const gap = ys[i] - ys[i - 1];
      expect(gap).toBeGreaterThanOrEqual(5);
      expect(Number(hitPaths[i].getAttribute('stroke-width'))).toBeCloseTo(gap, 5);
    }
  });

  it('keeps outer lane endpoints inside both cards and hit targets non-overlapping with 50 VLANs', () => {
    const vlans = Array.from({ length: 50 }, (_, i) => vlan(10 + i, [hop({})]));
    const flow: VLANFlow = { generated_at: '2026-01-01T00:00:00Z', vlans, findings: [] };
    const layout = computeAllVlansLayout(flow);
    const [fromCard, toCard] = layout.deviceCards;
    const { container } = draw('all', flow);
    const hitPaths = Array.from(container.querySelectorAll('path[stroke="transparent"]'));
    expect(hitPaths).toHaveLength(50);
    const endpoints = hitPaths.map((p) => {
      const nums = (p.getAttribute('d') ?? '').match(/-?\d+(?:\.\d+)?/g)?.map(Number) ?? [];
      return { y1: nums[1], y2: nums[7], hitW: Number(p.getAttribute('stroke-width')) };
    });
    for (const ep of endpoints) {
      expect(ep.y1).toBeGreaterThanOrEqual(fromCard.y);
      expect(ep.y1).toBeLessThanOrEqual(fromCard.y + fromCard.height);
      expect(ep.y2).toBeGreaterThanOrEqual(toCard.y);
      expect(ep.y2).toBeLessThanOrEqual(toCard.y + toCard.height);
    }
    for (let i = 1; i < endpoints.length; i++) {
      expect(endpoints[i].hitW).toBeLessThanOrEqual(endpoints[i].y1 - endpoints[i - 1].y1 + 1e-6);
    }
  });
});

describe('App poll all', () => {
  it('names the targets that failed to poll apart from refresh errors', async () => {
    const target = (id: string, name: string) => ({ id, name, driver: 'openwrt', endpoint: 'x', enabled: true, poll_interval: 60 });
    vi.spyOn(api, 'getState').mockResolvedValue(emptyState);
    vi.spyOn(api, 'getTopology').mockResolvedValue({ generated_at: '2026-01-01T00:00:00Z', root_ids: [], nodes: [], edges: [] });
    vi.spyOn(api, 'getVLANFlow').mockResolvedValue({ generated_at: '2026-01-01T00:00:00Z', vlans: [], findings: [] });
    vi.spyOn(api, 'listTargets').mockResolvedValue([target('t1', 'Core'), target('t2', 'Attic')]);
    vi.spyOn(api, 'listDiagnostics').mockResolvedValue([]);
    vi.spyOn(api, 'pollTarget').mockImplementation(async (id) => {
      if (id === 't2') throw new Error('HTTP 502: POLL_FAILED');
      return {};
    });

    wrap(<App />);
    fireEvent.click(await screen.findByRole('button', { name: 'Poll all' }));
    await waitFor(() => expect(screen.getByText('Poll failed for: Attic')).toBeInTheDocument());
    expect(screen.queryByText('API error')).not.toBeInTheDocument();
  });
});
