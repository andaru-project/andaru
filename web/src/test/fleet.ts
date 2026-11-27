import type { NetworkState, TopologyGraph, VLANFlow } from '../netplane/api';

// An empty fleet, for the fleet screens and the standalone app.
export const emptyState: NetworkState = {
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
export const emptyTopology: TopologyGraph = { generated_at: '2026-01-01T00:00:00Z', root_ids: [], nodes: [], edges: [] };
export const emptyFlow: VLANFlow = { generated_at: '2026-01-01T00:00:00Z', vlans: [], findings: [] };

// The standalone server's answers for an empty fleet, for fetchRoutes.
export const emptyFleet: Record<string, object> = {
  '/api/v1/state': emptyState,
  '/api/v1/topology': emptyTopology,
  '/api/v1/vlans/flow': emptyFlow,
  '/api/v1/targets': { items: [] },
  '/api/v1/diagnostics': { items: [] },
};
