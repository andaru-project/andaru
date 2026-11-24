// SPDX-License-Identifier: AGPL-3.0-or-later
import { useCallback, useEffect, useRef, useState } from 'react';
import {
  api,
  apiUrl,
  AUTH_EVENT,
  type DiagnosticResult,
  type NetplaneEvent,
  type NetworkState,
  type TargetConfig,
  type TopologyGraph,
  type VLANFlow,
} from './api';

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
  /** Listen for standalone Netplane token-prompt auth events to reconnect SSE. */
  listenAuthEvent?: boolean;
}

export function useFleetData({
  canReadTargets = true,
  canPoll = true,
  listenAuthEvent = false,
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

  const refreshAll = useCallback(async () => {
    const gen = ++refreshGen.current;
    const dGen = diagGen.current;
    try {
      const [st, topo, fl, tgts, diags] = await Promise.all([
        api.getState(),
        api.getTopology(),
        api.getVLANFlow(),
        canReadTargets ? api.listTargets() : Promise.resolve<TargetConfig[]>([]),
        api.listDiagnostics(30),
      ]);
      if (gen !== refreshGen.current) return;
      setState(st);
      setTopology(topo);
      setFlow(fl);
      setTargets(tgts);
      if (dGen === diagGen.current) {
        setDiagRuns(diags);
      }
      setRefreshNonce((n) => n + 1);
      setErrMsg(null);
    } catch (e) {
      if (gen !== refreshGen.current) return;
      setErrMsg(e instanceof Error ? e.message : String(e));
    }
  }, [canReadTargets]);

  const refreshRef = useRef(refreshAll);
  useEffect(() => {
    refreshRef.current = refreshAll;
  }, [refreshAll]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- initial fetch on mount
    void refreshAll();
    const timer = window.setInterval(() => void refreshAll(), 15_000);
    return () => window.clearInterval(timer);
  }, [refreshAll]);

  useEffect(() => {
    if (!listenAuthEvent) return;
    const onAuth = () => {
      sseFailures.current = 0;
      // SSE only: in-flight requests retry with the new cookie themselves, and a fresh refresh
      // here would hit 401 again before the server has accepted the token.
      setSseEpoch((n) => n + 1);
    };
    window.addEventListener(AUTH_EVENT, onAuth);
    return () => window.removeEventListener(AUTH_EVENT, onAuth);
  }, [listenAuthEvent]);

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
          void refreshRef.current();
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
      await refreshAll();
      setPollErr(failed.length > 0 ? `Poll failed for: ${failed.join(', ')}` : null);
    } catch (e) {
      setErrMsg(e instanceof Error ? e.message : String(e));
    } finally {
      setPollBusy(false);
    }
  }, [canPoll, targets, refreshAll]);

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
