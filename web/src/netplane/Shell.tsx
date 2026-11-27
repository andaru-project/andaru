// SPDX-License-Identifier: AGPL-3.0-or-later
import { Stack } from '@mantine/core';
import { useState } from 'react';
import { Route, Routes } from 'react-router';
import { AppFrame, type NavGroup } from '../AppFrame';
import { FleetWorkspace } from './FleetWorkspace';

interface ShellProps {
  /** Without it, the account menu has no sign out: the server needs no token. */
  onSignOut?: () => void;
  onSessionLost(): void;
}

const pageTitles: Record<string, string> = {
  '/': 'Overview',
  '/network/topology': 'Topology',
  '/network/vlans': 'VLANs & Segments',
  '/network/vlans/flow': 'VLAN Flow',
  '/network/devices': 'Devices',
  '/network/clients': 'Clients',
  '/network/diagnostics': 'Diagnostics & events',
};

// Andaru's network and diagnostics branches, without the pages of the box Andaru runs on.
const groups: NavGroup[] = [
  {
    label: 'Network',
    items: [
      { to: '/network/topology', label: 'Topology' },
      { to: '/network/vlans', label: 'VLANs & Segments' },
      { to: '/network/vlans/flow', label: 'VLAN Flow' },
      { to: '/network/devices', label: 'Devices' },
      { to: '/network/clients', label: 'Clients' },
    ],
  },
  { label: 'Diagnostics', items: [{ to: '/network/diagnostics', label: 'Tools & events' }] },
];

export function Shell({ onSignOut, onSessionLost }: ShellProps) {
  const [connected, setConnected] = useState(true);
  return (
    <AppFrame
      product="Netplane"
      baseTitle="Netplane"
      pageTitles={pageTitles}
      connected={connected}
      accountLabel="Account"
      timeZoneNote="Used to display timestamps across Netplane in this browser."
      onSignOut={onSignOut}
      homeLabel="Overview"
      groups={groups}
    >
      <Stack>
        <Routes>
          <Route path="/*" element={<FleetWorkspace apiPrefix="api/v1" overview csrfToken="" canReadTargets canWrite onSessionLost={onSessionLost} onConnection={setConnected} />} />
        </Routes>
      </Stack>
    </AppFrame>
  );
}
