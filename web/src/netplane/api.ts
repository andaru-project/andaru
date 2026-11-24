// SPDX-License-Identifier: AGPL-3.0-or-later
import { apiBasePath } from './routing';

export interface SensorReading {
  name: string;
  kind: string;
  value: number;
  unit: string;
  status: 'ok' | 'warn' | 'crit' | 'unknown';
}

export interface PoEBudget {
  total_watts: number;
  used_watts: number;
}

export interface Device {
  id: string;
  driver: string;
  target_id: string;
  name: string;
  hostname?: string;
  vendor?: string;
  model?: string;
  role: string;
  primary_mac?: string;
  macs: string[];
  mgmt_ip?: string;
  firmware: { current?: string; available?: string; hardware_rev?: string; serial?: string };
  uptime_sec: number;
  online: boolean;
  last_seen: string;
  capabilities: string[];
  system_stats: { cpu_pct?: number; mem_used_bytes?: number; mem_total_bytes?: number; load1?: number };
  sensors: SensorReading[];
  poe_budget?: PoEBudget | null;
  pending_changes?: boolean;
}

export interface SfpDom {
  present: boolean;
  vendor?: string;
  part_number?: string;
  serial?: string;
  wavelength_nm?: number;
  temp_c?: number;
  voltage_v?: number;
  bias_ma?: number;
  tx_power_dbm?: number;
  rx_power_dbm?: number;
  alert_state: 'ok' | 'warn' | 'crit' | 'unknown';
}

export interface Link {
  device_id: string;
  index: number;
  if_name: string;
  label?: string;
  mac?: string;
  admin_up: boolean;
  link_up: boolean;
  oper_state: string;
  speed_mbps: number;
  duplex?: string;
  autoneg?: boolean;
  mtu?: number;
  media: string;
  is_fiber: boolean;
  isolated?: boolean;
  lag_group_id?: string;
  stp_state?: string;
  stp_role?: string;
  eee?: boolean;
  flow_control?: { rx: boolean; tx: boolean; autoneg?: boolean };
  pvid?: number;
  tagged_vlans: number[];
  poe?: { supported: boolean; enabled: boolean; state?: string; watts?: number; class?: string };
  sfp?: SfpDom;
  counters?: {
    rx_bytes: number;
    tx_bytes: number;
    rx_packets?: number;
    tx_packets?: number;
    rx_errors: number;
    tx_errors: number;
    rx_dropped: number;
    tx_dropped: number;
  };
  rates?: { rx_bps: number; tx_bps: number };
}

export interface LagGroup {
  device_id: string;
  id: string;
  name: string;
  mode: string;
  member_links: string[];
  link_up: boolean;
  speed_mbps: number;
}

export interface IPAddress {
  family: string;
  address: string;
  prefix_length: number;
  scope?: string;
  source?: string;
}

export interface Segment {
  device_id: string;
  id: string;
  name: string;
  kind: string;
  parent_link?: string;
  vlan_id?: number;
  zone_id?: string;
  mtu?: number;
  admin_up: boolean;
  oper_state: string;
  addresses: IPAddress[];
  ha_state?: { protocol: string; vhid: number; state: string; vip: string };
  is_management?: boolean;
}

export interface Zone {
  device_id: string;
  id: string;
  name: string;
  segment_ids: string[];
}

export interface GatewayStatus {
  device_id: string;
  name: string;
  segment_name?: string;
  family?: string;
  gateway_ip: string;
  monitor_ip?: string;
  online: boolean;
  rtt_ms?: number;
  loss_pct?: number;
  is_default?: boolean;
}

export interface Route {
  device_id: string;
  family: string;
  destination: string;
  gateway?: string;
  segment_name?: string;
  protocol: string;
  metric?: number;
}

export interface VpnTunnel {
  device_id: string;
  id: string;
  name: string;
  kind: string;
  peer?: string;
  endpoint?: string;
  up: boolean;
  last_handshake?: string;
  rx_bytes: number;
  tx_bytes: number;
}

export interface FirewallState {
  device_id: string;
  engine: string;
  states_used?: number;
  states_max?: number;
  policy_rule_count?: number;
  nat_rule_count?: number;
  pending_saved_changes?: boolean;
  saved_version?: string;
  services: { name: string; state: string; summary?: string }[];
}

export interface WirelessSsid {
  name: string;
  enabled: boolean;
  vlan_id?: number;
  security?: string;
  client_count: number;
}

export interface WirelessRadio {
  device_id: string;
  name: string;
  band: string;
  channel: number;
  width_mhz: number;
  tx_power_dbm: number;
  noise_floor_dbm?: number;
  utilization_pct?: number;
  ssids: WirelessSsid[];
}

export interface WirelessUplink {
  device_id: string;
  peer_device_id?: string;
  peer_mac: string;
  band?: string;
  signal_dbm: number;
  tx_rate_mbps?: number;
  rx_rate_mbps?: number;
}

export interface Client {
  mac: string;
  ips: string[];
  hostname?: string;
  vendor_oui?: string;
  online: boolean;
  connection_type: string;
  attached_device_id?: string;
  attached_link?: string;
  ssid?: string;
  vlan?: number;
  signal_dbm?: number;
  tx_rate_mbps?: number;
  rx_rate_mbps?: number;
  last_seen: string;
}

export interface DiagnosticResult {
  id: string;
  kind: string;
  device_id?: string;
  target: string;
  ok: boolean;
  started_at: string;
  duration_ms: number;
  summary: string;
  structured?: Record<string, unknown>;
  raw_output?: string;
  error?: string;
}

export interface DeviceDetail {
  device: Device;
  links: Link[];
  lag_groups: LagGroup[];
  segments: Segment[];
  zones: Zone[];
  gateways: GatewayStatus[];
  routes: Route[];
  vpns: VpnTunnel[];
  firewall?: FirewallState;
  radios: WirelessRadio[];
  wireless_uplinks: WirelessUplink[];
  clients: Client[];
  recent_diagnostics: DiagnosticResult[];
}

export interface NetworkState {
  devices: Device[];
  links: Link[];
  lag_groups: LagGroup[];
  segments: Segment[];
  zones: Zone[];
  gateways: GatewayStatus[];
  routes: Route[];
  vpns: VpnTunnel[];
  firewalls: FirewallState[];
  radios: WirelessRadio[];
  wireless_uplinks: WirelessUplink[];
  clients: Client[];
}

export interface TopologyNode {
  id: string;
  kind: 'device' | 'client';
  role?: string;
  driver?: string;
  label: string;
  mac?: string;
  ip?: string;
  online: boolean;
}

export interface TopologyEdge {
  id: string;
  from_id: string;
  from_link?: string;
  to_id: string;
  to_link?: string;
  kind: string;
  evidence: string;
  confidence: number;
  speed_mbps?: number;
  member_links: string[];
}

export interface TopologyGraph {
  generated_at: string;
  root_ids: string[];
  nodes: TopologyNode[];
  edges: TopologyEdge[];
}

export type VLANMode = 'tagged' | 'untagged' | 'none' | 'unknown';
export type HopState = 'carried' | 'dead_end' | 'mismatch' | 'leak' | 'unverified';
export type FindingKind = 'leak' | 'dead_end' | 'tag_mismatch';

export interface VLANFlowHop {
  edge_id: string;
  from_id: string;
  from_link?: string;
  to_id: string;
  to_link?: string;
  kind: string;
  from_mode: VLANMode;
  to_mode: VLANMode;
  state: HopState;
  from_pvid?: number;
  from_tagged: number[];
  to_pvid?: number;
  to_tagged: number[];
}

export interface VLANFlowGateway {
  device_id: string;
  segment_id: string;
  segment_name: string;
  parent_link?: string;
  zone_id?: string;
  zone_name?: string;
  addresses: IPAddress[];
  is_management?: boolean;
}

export interface VLANFlowAccessPort {
  device_id: string;
  link: string;
  label?: string;
  oper_state: string;
  reachable: boolean;
}

export interface VLANFlowSSID {
  device_id: string;
  radio: string;
  name: string;
  client_count: number;
  reachable: boolean;
}

export interface VLANFlowVLAN {
  id: number;
  name?: string;
  gateways: VLANFlowGateway[];
  devices: string[];
  reached: string[];
  hops: VLANFlowHop[];
  access_ports: VLANFlowAccessPort[];
  ssids: VLANFlowSSID[];
  clients: number;
}

export interface VLANFlowFinding {
  kind: FindingKind;
  severity: 'crit' | 'warn';
  vlans: number[];
  edge_id: string;
  device_id: string;
  link: string;
  peer_id: string;
  peer_link: string;
  message: string;
  stranded: string[];
}

export interface VLANFlow {
  generated_at: string;
  vlans: VLANFlowVLAN[];
  findings: VLANFlowFinding[];
}

export interface TargetConfig {
  id: string;
  name: string;
  driver: string;
  endpoint: string;
  enabled: boolean;
  poll_interval: number;
}

export interface NetplaneEvent {
  id: string;
  type: string;
  severity: 'info' | 'warn' | 'crit';
  timestamp: string;
  device_id?: string;
  link_name?: string;
  summary: string;
}

export interface NetplaneApiConfig {
  /** API prefix relative to the app root, e.g. `"api/v1/fleet"` in Andaru or `"api/v1"` standalone. */
  apiPrefix?: string;
  /** CSRF token sent as `X-Andaru-CSRF` on state-changing requests. */
  csrfToken?: string;
  /** Called on HTTP 401 instead of prompting for a standalone API token. */
  onUnauthorized?: () => void;
}

let apiConfig: NetplaneApiConfig | null = null;

/**
 * Configures how Netplane API requests are routed and authenticated. Pass `null` to restore
 * standalone defaults (`api/v1` with cookie-token prompt).
 */
export function configureNetplaneApi(config: NetplaneApiConfig | null): void {
  apiConfig = config;
}

// Fired after a token is stored, so the event stream can reconnect at once.
export const AUTH_EVENT = 'netplane-auth';

// One shared prompt so parallel 401s ask for the token only once.
let tokenPrompt: Promise<boolean> | null = null;
// Bumped on each stored token, so a 401 sent before it retries with the new cookie instead of prompting again.
let authGen = 0;

export function apiUrl(path: string): string {
  const prefix = (apiConfig?.apiPrefix ?? 'api/v1').replace(/^\/+|\/+$/g, '');
  return `${apiBasePath()}${prefix}${path}`;
}

// promptForToken stores the API token in a SameSite=Strict cookie, which fetch and
// EventSource both send; EventSource cannot set an Authorization header.
function promptForToken(): Promise<boolean> {
  tokenPrompt ??= Promise.resolve().then(() => {
    const token = window.prompt('This netplane server requires an API token:');
    if (!token) return false;
    // Scope to the app's base path so other apps on the origin (e.g. other HA ingress add-ons) never get it.
    const path = apiBasePath();
    document.cookie = `netplane_token=${encodeURIComponent(token)}; path=${path}; SameSite=Strict`;
    authGen++;
    window.dispatchEvent(new Event(AUTH_EVENT));
    return true;
  }).finally(() => {
    tokenPrompt = null;
  });
  return tokenPrompt;
}

async function fetchJSON<T>(path: string, init?: RequestInit, retried = false): Promise<T> {
  const gen = authGen;
  const method = (init?.method ?? 'GET').toUpperCase();
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
  };
  if (apiConfig?.csrfToken && method !== 'GET' && method !== 'HEAD' && method !== 'OPTIONS') {
    headers['X-Andaru-CSRF'] = apiConfig.csrfToken;
  }
  const resp = await fetch(apiUrl(path), {
    credentials: 'same-origin',
    ...init,
    headers: {
      ...headers,
      ...(init?.headers ?? {}),
    },
  });
  if (resp.status === 401) {
    if (apiConfig?.onUnauthorized) {
      apiConfig.onUnauthorized();
      throw new Error('HTTP 401: authentication required');
    }
    if (!retried && (gen !== authGen || (await promptForToken()))) {
      return fetchJSON<T>(path, init, true);
    }
  }
  if (!resp.ok) {
    const text = await resp.text();
    throw new Error(`HTTP ${resp.status}: ${text}`);
  }
  return resp.json() as Promise<T>;
}

export const api = {
  getState: () => fetchJSON<NetworkState>('/state'),
  getDevice: (id: string) => fetchJSON<DeviceDetail>(`/devices/${encodeURIComponent(id)}`),
  getTopology: () => fetchJSON<TopologyGraph>('/topology'),
  getVLANFlow: () => fetchJSON<VLANFlow>('/vlans/flow'),
  listTargets: () => fetchJSON<{ items: TargetConfig[] }>('/targets').then((r) => r.items),
  pollTarget: (id: string) => fetchJSON<unknown>(`/targets/${encodeURIComponent(id)}/poll`, { method: 'POST' }),
  listDiagnostics: (limit = 30) =>
    fetchJSON<{ items: DiagnosticResult[] }>(`/diagnostics?limit=${limit}`).then((r) => r.items),
  runDiagnostic: (req: { kind: string; device_id?: string; target: string; params?: Record<string, string> }) =>
    fetchJSON<DiagnosticResult>('/diagnostics', { method: 'POST', body: JSON.stringify(req) }),
};

export function formatSpeed(mbps?: number): string {
  if (!mbps) return '—';
  if (mbps >= 1000) {
    const g = mbps / 1000;
    return `${Number.isInteger(g) ? g : g.toFixed(1)}G`;
  }
  return `${mbps}M`;
}

export function formatBps(bps?: number): string {
  if (!bps || bps <= 0) return '0 bps';
  if (bps >= 1e9) return `${(bps / 1e9).toFixed(2)} Gbps`;
  if (bps >= 1e6) return `${(bps / 1e6).toFixed(1)} Mbps`;
  if (bps >= 1e3) return `${(bps / 1e3).toFixed(0)} kbps`;
  return `${Math.round(bps)} bps`;
}

export function formatUptime(sec?: number): string {
  if (!sec) return '—';
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  return d > 0 ? `${d}d ${h}h` : `${h}h ${m}m`;
}

export function formatBytes(b?: number): string {
  if (!b || b <= 0) return '0 B';
  if (b >= 1e12) return `${(b / 1e12).toFixed(2)} TB`;
  if (b >= 1e9) return `${(b / 1e9).toFixed(1)} GB`;
  if (b >= 1e6) return `${(b / 1e6).toFixed(1)} MB`;
  if (b >= 1e3) return `${(b / 1e3).toFixed(0)} KB`;
  return `${b} B`;
}
