// SPDX-License-Identifier: AGPL-3.0-or-later
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useVisiblePoll } from '../useResource';
import {
  api,
  apiUrl,
  type Device,
  type DiagnosticResult,
  type NetplaneEvent,
  type NetworkState,
  type TargetConfig,
  type TopologyGraph,
  type VLANFlow,
  type VLANFlowVLAN,
} from './api';
import { buildVlanColorMap } from './vlanUtils';

const SSE_EVENT_TYPES = [
  'device.online',
  'device.offline',
  'link.up',
  'link.down',
  'link.stp_changed',
  'sfp.alert',
  'vpn.up',
  'vpn.down',
  'gateway.up',
  'gateway.down',
  'topology.updated',
  'client.joined',
  'client.left',
];

export interface FleetDataOptions {
  /** Whether the caller is allowed to list targets (requires configuration.read in Andaru). */
  canReadTargets?: boolean;
  /** Whether the caller is allowed to trigger target polls (requires configuration.write in Andaru). */
  canPoll?: boolean;
}

export function useFleetLookups(
  state: NetworkState | null,
  flow: VLANFlow | null,
  targets: TargetConfig[],
) {
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
  const enabledTargets = useMemo(() => targets.filter((t) => t.enabled), [targets]);

  return {
    devices,
    clients,
    findings,
    vlans,
    devicesById,
    vlansById,
    vlanColors,
    hasCrit,
    enabledTargets,
  };
}

export function useFleetData({
  canReadTargets = true,
  canPoll = true,
}: FleetDataOptions = {}) {
  const [sseEpoch, setSseEpoch] = useState(0);
  const sseFailures = useRef(0);

  const [state, setState] = useState<NetworkState | null>(null);
  const [topology, setTopology] = useState<TopologyGraph | null>(null);
  const [flow, setFlow] = useState<VLANFlow | null>(null);
  const [targets, setTargets] = useState<TargetConfig[]>([]);
  const [diagRuns, setDiagRuns] = useState<DiagnosticResult[]>([]);
  const [events, setEvents] = useState<NetplaneEvent[]>([]);
  const [sseConnected, setSseConnected] = useState(false);
  const [errMsg, setErrMsg] = useState<string | null>(null);
  const [pollErr, setPollErr] = useState<string | null>(null);
  const [pollBusy, setPollBusy] = useState(false);
  const [refreshNonce, setRefreshNonce] = useState(0);

  const refreshGen = useRef(0);
  const diagGen = useRef(0);
  const inFlight = useRef(0);
  const pendingBackground = useRef(false);

  const runRefresh = useCallback(async (initialBackground: boolean) => {
    let background = initialBackground;
    while (true) {
      const gen = ++refreshGen.current;
      const dGen = diagGen.current;
      const opts = background ? { background: true } : undefined;
      inFlight.current++;
      try {
        const [st, topo, fl, tgts, diags] = await Promise.all([
          api.getState(opts),
          api.getTopology(opts),
          api.getVLANFlow(opts),
          canReadTargets ? api.listTargets(opts) : Promise.resolve<TargetConfig[]>([]),
          api.listDiagnostics(undefined, opts),
        ]);
        if (gen === refreshGen.current) {
          setState(st);
          setTopology(topo);
          setFlow(fl);
          setTargets(tgts);
          if (dGen === diagGen.current) {
            setDiagRuns(diags);
          }
          setRefreshNonce((n) => n + 1);
          setErrMsg(null);
        }
      } catch (e) {
        if (gen === refreshGen.current) {
          setErrMsg(e instanceof Error ? e.message : String(e));
        }
      } finally {
        inFlight.current--;
      }
      if (inFlight.current === 0 && pendingBackground.current) {
        pendingBackground.current = false;
        background = true;
        continue;
      }
      return;
    }
  }, [canReadTargets]);

  // Automatic refreshes (15s poll and SSE topology.updated) coalesce while a batch is already in
  // flight so slow responses are published instead of being superseded by every timer tick.
  const refreshBackground = useCallback(() => {
    if (inFlight.current > 0) {
      pendingBackground.current = true;
      return;
    }
    void runRefresh(true);
  }, [runRefresh]);

  const backgroundRef = useRef(refreshBackground);
  useEffect(() => {
    backgroundRef.current = refreshBackground;
  }, [refreshBackground]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- initial fetch on mount
    void runRefresh(false);
  }, [runRefresh]);

  useVisiblePoll(refreshBackground, 15_000);

  useEffect(() => {
    if (typeof EventSource === 'undefined') return;
    const es = new EventSource(apiUrl('/events/stream'));
    es.onopen = () => {
      sseFailures.current = 0;
      setSseConnected(true);
    };
    let retry: number | undefined;
    es.onerror = () => {
      setSseConnected(false);
      if (es.readyState === EventSource.CLOSED) {
        const delay = Math.min(60_000, 5_000 * 2 ** sseFailures.current++);
        retry = window.setTimeout(() => setSseEpoch((n) => n + 1), delay);
      }
    };
    const onEvent = (e: MessageEvent<string>) => {
      try {
        const parsed = JSON.parse(e.data) as NetplaneEvent;
        setEvents((prev) => [parsed, ...prev].slice(0, 50));
        if (parsed.type === 'topology.updated') {
          backgroundRef.current();
        }
      } catch {
        // Ignore non-JSON SSE keepalive/frames.
      }
    };
    for (const t of SSE_EVENT_TYPES) {
      es.addEventListener(t, onEvent as EventListener);
    }
    return () => {
      window.clearTimeout(retry);
      es.close();
    };
  }, [sseEpoch]);

  const handlePollAll = useCallback(async () => {
    setPollBusy(true);
    try {
      const enabled = canPoll ? targets.filter((t) => t.enabled) : [];
      const results = await Promise.allSettled(enabled.map((t) => api.pollTarget(t.id)));
      const failed = enabled.filter((_, i) => results[i].status === 'rejected').map((t) => t.name || t.id);
      // Refresh anyway so targets that did poll show their new state.
      await runRefresh(false);
      setPollErr(failed.length > 0 ? `Poll failed for: ${failed.join(', ')}` : null);
    } finally {
      setPollBusy(false);
    }
  }, [canPoll, targets, runRefresh]);

  const handleDiagnosticCompleted = useCallback((res: DiagnosticResult) => {
    diagGen.current++;
    setDiagRuns((prev) => [res, ...prev.filter((d) => d.id !== res.id)]);
  }, []);

  return {
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
    handlePollAll,
    handleDiagnosticCompleted,
  };
}
