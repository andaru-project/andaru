// SPDX-License-Identifier: AGPL-3.0-or-later
import {
  Alert,
  AppShell,
  Badge,
  Burger,
  Button,
  Group,
  NavLink,
  Stack,
  Text,
  Title,
  useComputedColorScheme,
  useMantineColorScheme,
} from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import { Fragment, type ReactNode, useMemo } from 'react';
import { Link, Navigate, Route, Routes, useLocation } from 'react-router';
import {
  type Device,
  type DiagnosticResult,
  type NetplaneEvent,
  type NetworkState,
  type TargetConfig,
  type TopologyGraph,
  type VLANFlow,
  type VLANFlowVLAN,
} from './api';
import {
  AndaruMark,
  IconClients,
  IconDevices,
  IconDiagnostics,
  IconOverview,
  IconTopology,
  IconVlanFlow,
  IconVlans,
} from './components/Icons';
import { ClientsScreen } from './screens/ClientsScreen';
import { DeviceDetailScreen } from './screens/DeviceDetailScreen';
import { DevicesScreen } from './screens/DevicesScreen';
import { DiagnosticsScreen } from './screens/DiagnosticsScreen';
import { OverviewScreen } from './screens/OverviewScreen';
import { TopologyScreen } from './screens/TopologyScreen';
import { VlanFlowScreen } from './screens/VlanFlowScreen';
import { VlansScreen } from './screens/VlansScreen';
import { buildVlanColorMap } from './vlanUtils';

interface NavItem {
  to: string;
  label: string;
  icon: () => ReactNode;
  badge?: 'findings' | 'devices' | 'vlans' | 'clients' | 'events';
}

const NAV: { group: string; items: NavItem[] }[] = [
  {
    group: 'Monitor',
    items: [
      { to: '/', label: 'Overview', icon: IconOverview },
      { to: '/topology', label: 'Topology', icon: IconTopology },
      { to: '/vlans/flow', label: 'VLAN Flow', icon: IconVlanFlow, badge: 'findings' },
    ],
  },
  {
    group: 'Inventory',
    items: [
      { to: '/devices', label: 'Devices', icon: IconDevices, badge: 'devices' },
      { to: '/vlans', label: 'VLANs & Segments', icon: IconVlans, badge: 'vlans' },
      { to: '/clients', label: 'Clients', icon: IconClients, badge: 'clients' },
    ],
  },
  {
    group: 'Operations',
    items: [
      { to: '/diagnostics', label: 'Diagnostics & Events', icon: IconDiagnostics, badge: 'events' },
    ],
  },
];

interface ShellProps {
  state: NetworkState | null;
  topology: TopologyGraph | null;
  flow: VLANFlow | null;
  targets: TargetConfig[];
  diagRuns: DiagnosticResult[];
  events: NetplaneEvent[];
  sseConnected: boolean;
  errMsg: string | null;
  /** Targets the last "Poll all" could not poll, reported apart from refresh errors. */
  pollErr?: string | null;
  pollBusy: boolean;
  refreshNonce: number;
  onPollAll: () => void;
  onDiagnosticCompleted: (res: DiagnosticResult) => void;
}

export function Shell({
  state,
  topology,
  flow,
  targets,
  diagRuns,
  events,
  sseConnected,
  errMsg,
  pollErr,
  pollBusy,
  refreshNonce,
  onPollAll,
  onDiagnosticCompleted,
}: ShellProps) {
  const [opened, { toggle, close }] = useDisclosure();
  const { pathname } = useLocation();
  const { setColorScheme } = useMantineColorScheme();
  const computedColorScheme = useComputedColorScheme('light', { getInitialValueInEffect: true });

  const devices = useMemo(() => state?.devices ?? [], [state?.devices]);
  const clients = state?.clients ?? [];
  const findings = flow?.findings ?? [];
  const vlans = useMemo(() => flow?.vlans ?? [], [flow?.vlans]);

  const devicesById = useMemo(
    () => new Map<string, Device>(devices.map((d) => [d.id, d])),
    [devices],
  );
  const vlansById = useMemo(
    () => new Map<number, VLANFlowVLAN>(vlans.map((v) => [v.id, v])),
    [vlans],
  );
  const vlanColors = useMemo(() => buildVlanColorMap(vlans), [vlans]);

  const hasCrit = findings.some((f) => f.severity === 'crit');
  const onlineClients = clients.filter((c) => c.online).length;
  const enabledTargets = targets.filter((t) => t.enabled);

  const isActive = (target: string) => {
    if (target === '/') return pathname === '/' || pathname === '/overview';
    if (target === '/devices') return pathname === '/devices' || pathname.startsWith('/devices/');
    return pathname === target;
  };

  const toggleScheme = () => {
    setColorScheme(computedColorScheme === 'dark' ? 'light' : 'dark');
  };

  const navCounts = {
    devices: devices.length,
    vlans: vlans.length,
    clients: onlineClients,
    events: events.length,
  };

  return (
    <AppShell
      className="app-shell"
      data-nav-open={opened || undefined}
      header={{ height: 56 }}
      navbar={{ width: 240, breakpoint: 900, collapsed: { mobile: !opened } }}
      padding="md"
    >
      <AppShell.Header className="app-header">
        <Group h="100%" px="md" justify="space-between" wrap="nowrap">
          <Group className="app-brand" gap="sm" wrap="nowrap">
            <Burger
              className="nav-burger"
              color="white"
              opened={opened}
              onClick={toggle}
              size="sm"
              aria-label={opened ? 'Close navigation' : 'Open navigation'}
            />
            <Link to="/" className="app-brand-link" aria-label="Andaru Netplane Home" onClick={close}>
              <AndaruMark size={28} />
              <Title className="app-title" order={1} size="h4" c="white">
                Andaru Netplane
              </Title>
            </Link>
          </Group>

          <Group className="app-controls" gap="xs" wrap="nowrap">
            {findings.length > 0 && (
              <Button
                component={Link}
                to="/vlans/flow"
                size="compact-xs"
                color={hasCrit ? 'red' : 'yellow'}
                variant="filled"
                className="np-hide-xs"
              >
                ⚠ {findings.length} VLAN {findings.length === 1 ? 'issue' : 'issues'}
              </Button>
            )}
            <Badge
              color={sseConnected ? 'green' : 'gray'}
              variant="dot"
              className="quiet-badge np-hide-xs"
              role="status"
            >
              {sseConnected ? 'Live SSE' : 'Connecting'}
            </Badge>
            <Button
              size="xs"
              variant="default"
              className="np-header-action"
              loading={pollBusy}
              onClick={onPollAll}
              title={enabledTargets.length > 0 ? 'Poll all enabled targets now' : 'Refresh network state'}
            >
              {enabledTargets.length > 0 ? 'Poll all' : 'Refresh'}
            </Button>
            <Button
              size="xs"
              variant="default"
              className="np-header-action"
              onClick={toggleScheme}
              aria-label={`Switch to ${computedColorScheme === 'dark' ? 'light' : 'dark'} theme`}
            >
              {computedColorScheme === 'dark' ? '☀ Light' : '☾ Dark'}
            </Button>
          </Group>
        </Group>
      </AppShell.Header>

      <AppShell.Navbar className="app-navbar" p="xs" component="nav" aria-label="Main navigation">
        <Stack gap={2}>
          {NAV.map((section) => (
            <Fragment key={section.group}>
              <div className="np-nav-group-label">{section.group}</div>
              {section.items.map((item) => {
                const active = isActive(item.to);
                const rightSection =
                  item.badge === 'findings'
                    ? findings.length > 0 ? (
                        <Badge size="xs" variant="filled" color={hasCrit ? 'red' : 'yellow'}>
                          {findings.length}
                        </Badge>
                      ) : undefined
                    : item.badge && navCounts[item.badge] > 0
                      ? <Text size="xs" c="dimmed" ff="monospace">{navCounts[item.badge]}</Text>
                      : undefined;
                return (
                  <NavLink
                    key={item.to}
                    className="nav-item"
                    component={Link}
                    to={item.to}
                    label={item.label}
                    leftSection={item.icon()}
                    rightSection={rightSection}
                    active={active}
                    aria-current={active ? 'page' : undefined}
                    onClick={close}
                  />
                );
              })}
            </Fragment>
          ))}
        </Stack>
      </AppShell.Navbar>

      <AppShell.Main>
        <Stack gap="md">
          {errMsg && (
            <Alert color="red" role="alert" title="API error">
              {errMsg}
            </Alert>
          )}
          {pollErr && (
            <Alert color="yellow" role="alert" title="Poll failed">
              {pollErr}
            </Alert>
          )}
          <Routes>
            <Route
              path="/"
              element={
                <OverviewScreen
                  state={state}
                  topology={topology}
                  flow={flow}
                  events={events}
                  sseConnected={sseConnected}
                  devicesById={devicesById}
                />
              }
            />
            <Route path="/overview" element={<Navigate to="/" replace />} />
            <Route
              path="/topology"
              element={
                <TopologyScreen
                  graph={topology}
                  state={state}
                  flow={flow}
                  devicesById={devicesById}
                  vlansById={vlansById}
                  vlanColors={vlanColors}
                />
              }
            />
            <Route
              path="/vlans/flow"
              element={
                <VlanFlowScreen
                  flow={flow}
                  state={state}
                  topology={topology}
                  devicesById={devicesById}
                  vlansById={vlansById}
                  vlanColors={vlanColors}
                />
              }
            />
            <Route
              path="/vlans"
              element={
                <VlansScreen
                  flow={flow}
                  state={state}
                  devicesById={devicesById}
                  vlanColors={vlanColors}
                />
              }
            />
            <Route path="/devices" element={<DevicesScreen state={state} flow={flow} />} />
            <Route
              path="/devices/:id"
              element={
                <DeviceDetailScreen
                  devices={devices}
                  state={state}
                  topology={topology}
                  flow={flow}
                  devicesById={devicesById}
                  vlansById={vlansById}
                  vlanColors={vlanColors}
                  refreshNonce={refreshNonce}
                />
              }
            />
            <Route
              path="/clients"
              element={
                <ClientsScreen
                  clients={clients}
                  flow={flow}
                  devicesById={devicesById}
                  vlansById={vlansById}
                  vlanColors={vlanColors}
                />
              }
            />
            <Route
              path="/diagnostics"
              element={
                <DiagnosticsScreen
                  devices={devices}
                  diagRuns={diagRuns}
                  events={events}
                  sseConnected={sseConnected}
                  onDiagnosticCompleted={onDiagnosticCompleted}
                />
              }
            />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </Stack>
      </AppShell.Main>
    </AppShell>
  );
}
