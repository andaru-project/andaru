// SPDX-License-Identifier: AGPL-3.0-or-later
import { Shell } from './Shell';
import { useFleetData } from './useFleetData';

export function App() {
  const fleet = useFleetData({ listenAuthEvent: true });

  return (
    <Shell
      state={fleet.state}
      topology={fleet.topology}
      flow={fleet.flow}
      targets={fleet.targets}
      diagRuns={fleet.diagRuns}
      events={fleet.events}
      sseConnected={fleet.sseConnected}
      errMsg={fleet.errMsg}
      pollErr={fleet.pollErr}
      pollBusy={fleet.pollBusy}
      refreshNonce={fleet.refreshNonce}
      onPollAll={() => void fleet.handlePollAll()}
      onDiagnosticCompleted={fleet.handleDiagnosticCompleted}
    />
  );
}
