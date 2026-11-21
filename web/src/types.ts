export interface Session {
  account: { id: string; login: string; displayName: string };
  capabilities: string[];
  csrfToken: string;
  expiresAt: string;
  idleTimeoutSeconds: number;
}

export interface User {
  id: string;
  login: string;
  displayName: string;
  kind: 'human' | 'service';
  enabled: boolean;
  roleIds: string[];
  revision: number;
  createdAt: number;
  updatedAt: number;
}

export interface Role {
  id: string;
  name: string;
  description?: string;
  capabilities: string[];
  builtIn?: 'administrator' | 'read-only';
}

export interface Token {
  selector: string;
  accountId: string;
  name: string;
  scope: string[];
  createdAt: string;
  expiresAt?: string;
  noExpiry: boolean;
  revokedAt?: string;
  lastUsedAt?: string;
  revokeOnPasswordChange: boolean;
  revision: number;
}

export interface TokenIssueResponse {
  operationId: string;
  result: string;
  target: string;
  metadata: Record<string, string>;
  selector: string;
  secret?: string;
  secretUnavailable: boolean;
  token?: Token;
}

export interface AuditEvent {
  id: number;
  at: string;
  actor: { kind: string; id: string };
  source: string;
  action: string;
  target: string;
  result: string;
  operationId?: string;
  category: string;
  count: number;
  metadata: Record<string, string>;
}

export interface Page<T> {
  items: T[];
  nextCursor?: string;
}

export interface SavedObject<T> {
  value: T;
  version: string;
}

export interface SavedRolePage {
  state: 'saved';
  savedVersion: string;
  items: Array<SavedObject<Role>>;
}

export interface Apply {
  id: string;
  savedVersion: string;
  state: 'activating' | 'awaiting_confirmation' | 'confirmed' | 'rolled_back' | 'failed';
  previous: string | null;
  deadline?: string;
  reason: string;
  createdAt: string;
  confirmTimeoutSeconds: number;
  files: Array<{ path: string; mode: string }>;
}

export interface Applies {
  pointers: { confirmed: string | null; lastKnownGood: string | null; pending: string | null; live: string | null };
  items: Apply[];
}

export interface ErrorDetail {
  path?: string;
  message: string;
}

export interface VersionInfo {
  id: string;
  parentId?: string;
  createdAt: string;
  actor: string;
  changedObjectCount: number;
}

export interface History {
  state: 'saved';
  items: VersionInfo[];
  nextBefore?: string;
}

export interface VersionDetail extends VersionInfo {
  state: 'saved';
  // The whole declaration view; the UI only displays it.
  value: Record<string, unknown>;
  changes: Array<{ collection: string; id: string; created?: boolean; deleted?: boolean }>;
}

export interface Finding {
  severity: 'error' | 'warning';
  code: string;
  path: string;
  message: string;
}

export interface Preview {
  state: 'preview';
  savedVersion: string;
  realizable: boolean;
  resolved: Array<{ link: string; interface: string }>;
  findings: Finding[];
}

export interface LinkStatus {
  ifIndex: number;
  name: string;
  linkType: string;
  mtu: number;
  operState: string;
  flags: string[];
  linkAddress?: string;
  addresses: Array<{ family: 'ipv4' | 'ipv6'; address: string; prefixLength: number; scope: string }>;
}

// What the web UI keeps for the account. The engine stores it and reads none of it; keys this version does not
// know belong to another version and are kept as they are.
export interface DashboardLayout { order?: string[]; hidden?: string[] }
export interface Preferences { dashboard?: DashboardLayout; [key: string]: unknown }

// How hard the box is working, as GET /api/v1/status/usage serves it. The processor counters are cumulative.
export interface Usage {
  time: string;
  cpu: { total: number; idle: number };
  load: number[] | null;
  memory: { totalBytes: number; availableBytes: number; swapTotalBytes: number; swapFreeBytes: number };
  fileSystems: Array<{ mountPoint: string; type: string; totalBytes: number; availableBytes: number }>;
  connections: { count: number; limit: number } | null;
  temperatures: Array<{ sensor: string; celsius: number }>;
}

// One reading of an interface's cumulative counters, and the hour of them GET /api/v1/status/traffic serves.
export interface TrafficSample { at: string; rxBytes: number; txBytes: number; rxPackets: number; txPackets: number; rxErrors: number; txErrors: number; rxDropped: number; txDropped: number }
export interface Traffic { intervalSeconds: number; interfaces: Array<{ name: string; samples: TrafficSample[] }> }

// What the box is, as GET /api/v1/status/system serves it.
export interface SystemInfo {
  version: string;
  hostname: string;
  kernel: string;
  architecture: string;
  time: string;
  bootedAt: string | null;
  memoryBytes: number;
  guest: string;
  iommu: boolean;
  cpu: {
    model: string;
    cores: number;
    threads: number;
    microcode: string;
    level: string;
    features: { cryptography: string[]; security: string[]; virtualization: string[] };
    vulnerabilities: Array<{ name: string; status: string; state: 'not_affected' | 'mitigated' | 'vulnerable' | 'unknown' }>;
  };
  hardware: { vendor: string; product: string; serial: string; boardVendor: string; board: string; biosVendor: string; biosVersion: string; biosDate: string };
}

interface PrefixPlace { interface: string; subnetId: string; prefix: string | null }

export interface PrefixView {
  wans: Array<{ interface: string; current: { prefix: string } | null; segments: PrefixPlace[]; pools: PrefixPlace[]; notFitting: string[] }>;
}

// The badge colour of a link's operational state; any other state is grey.
export const stateColor: Record<string, string> = { UP: 'green', DOWN: 'red', LOWERLAYERDOWN: 'red' };

export interface SavedLink {
  id: string;
  name: string;
  kind: 'ethernet' | 'vlan' | 'bridge' | 'bond' | 'pppoe';
  mtu: number;
  enabled: boolean;
  permanentMac?: string;
}

export interface Segment {
  id: string;
  name: string;
  link: string;
  zone: string;
  addressing: { ipv4: { mode: string }; ipv6: { mode: string } };
}

export interface Snapshot {
  state: 'saved';
  savedVersion: string;
  value: { links: SavedLink[]; segments: Segment[]; zones: Array<{ id: string; name: string }>; management?: { segment: string } };
}
