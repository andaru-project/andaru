// SPDX-License-Identifier: AGPL-3.0-or-later
import './styles.css';
import { Alert, Badge, Button, Group, Stack } from '@mantine/core';
import { Link, Navigate, Route, Routes } from 'react-router';
import { consumeUserActivity } from '../api';
import { configureNetplaneApi } from './api';
import { setRoutePrefix, vlanFlowPath } from './routing';
import { ClientsScreen } from './screens/ClientsScreen';
import { DeviceDetailScreen } from './screens/DeviceDetailScreen';
import { DevicesScreen } from './screens/DevicesScreen';
import { DiagnosticsScreen } from './screens/DiagnosticsScreen';
import { TopologyScreen } from './screens/TopologyScreen';
import { VlanFlowScreen } from './screens/VlanFlowScreen';
import { VlansScreen } from './screens/VlansScreen';
import { useFleetData, useFleetLookups } from './useFleetData';

export interface FleetWorkspaceProps {
  csrfToken: string;
  canReadTargets: boolean;
  canWrite: boolean;
  onSessionLost: () => void;
}

export function FleetWorkspace({
  csrfToken,
  canReadTargets,
  canWrite,
  onSessionLost,
}: FleetWorkspaceProps) {
  // Configure synchronously so child routes render `/network/...` links and hit `/api/v1/fleet/*`
  // from the very first render and across StrictMode remounts.
  setRoutePrefix('/network');
  configureNetplaneApi({
    apiPrefix: 'api/v1/fleet',
    csrfToken,
    onUnauthorized: onSessionLost,
    consumeActivity: consumeUserActivity,
  });

  const fleet = useFleetData({ canReadTargets, canPoll: canWrite });
  const {
    devices,
    clients,
    findings,
    devicesById,
    vlansById,
    vlanColors,
    hasCrit,
    enabledTargets,
  } = useFleetLookups(fleet.state, fleet.flow, fleet.targets);

  const canPollTargets = canWrite && enabledTargets.length > 0;

  return (
    <Stack gap="md">
      <Group justify="flex-end" gap="xs" wrap="wrap">
        {findings.length > 0 && (
          <Button
            component={Link}
            to={vlanFlowPath()}
            size="compact-xs"
            color={hasCrit ? 'red' : 'yellow'}
            variant="filled"
          >
            <span aria-hidden="true">⚠</span> {findings.length} VLAN {findings.length === 1 ? 'issue' : 'issues'}
          </Button>
        )}
        <Badge
          color={fleet.sseConnected ? 'green' : 'gray'}
          variant="dot"
          role="status"
        >
          {fleet.sseConnected ? 'Live fleet stream' : 'Connecting'}
        </Badge>
        <Button
          size="xs"
          variant="default"
          loading={fleet.pollBusy}
          onClick={() => void fleet.handlePollAll()}
          title={canPollTargets ? 'Poll all enabled fleet targets now' : 'Refresh fleet state'}
        >
          {canPollTargets ? 'Poll all' : 'Refresh'}
        </Button>
      </Group>

      {fleet.errMsg && (
        <Alert color="red" role="alert" title="Fleet API error">
          {fleet.errMsg}
        </Alert>
      )}
      {fleet.pollErr && (
        <Alert color="yellow" role="alert" title="Fleet poll failed">
          {fleet.pollErr}
        </Alert>
      )}

      <Routes>
        <Route
          path="topology"
          element={
            <TopologyScreen
              graph={fleet.topology}
              state={fleet.state}
              flow={fleet.flow}
              devicesById={devicesById}
              vlansById={vlansById}
              vlanColors={vlanColors}
            />
          }
        />
        <Route
          path="vlans/flow"
          element={
            <VlanFlowScreen
              flow={fleet.flow}
              state={fleet.state}
              topology={fleet.topology}
              devicesById={devicesById}
              vlansById={vlansById}
              vlanColors={vlanColors}
            />
          }
        />
        <Route
          path="vlans"
          element={
            <VlansScreen
              flow={fleet.flow}
              state={fleet.state}
              devicesById={devicesById}
              vlanColors={vlanColors}
            />
          }
        />
        <Route path="devices" element={<DevicesScreen state={fleet.state} flow={fleet.flow} />} />
        <Route
          path="devices/:id"
          element={
            <DeviceDetailScreen
              devices={devices}
              state={fleet.state}
              topology={fleet.topology}
              flow={fleet.flow}
              devicesById={devicesById}
              vlansById={vlansById}
              vlanColors={vlanColors}
              refreshNonce={fleet.refreshNonce}
            />
          }
        />
        <Route
          path="clients"
          element={
            <ClientsScreen
              clients={clients}
              flow={fleet.flow}
              devicesById={devicesById}
              vlansById={vlansById}
              vlanColors={vlanColors}
            />
          }
        />
        <Route
          path="diagnostics"
          element={
            <DiagnosticsScreen
              devices={devices}
              diagRuns={fleet.diagRuns}
              events={fleet.events}
              sseConnected={fleet.sseConnected}
              canRunDiagnostics={canWrite}
              onDiagnosticCompleted={fleet.handleDiagnosticCompleted}
            />
          }
        />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </Stack>
  );
}
