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
  // The Andaru version that saved it; absent when the Andaru that saved it did not record it.
  writtenBy?: string;
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
// complete says the answer is the whole history, to replace what is held, and not readings to add to it.
export interface Traffic { intervalSeconds: number; complete: boolean; interfaces: Array<{ name: string; samples: TrafficSample[] }> }

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

// stateLabel is a link's operational state in words: the kernel's own, but for the one it runs together.
export const stateLabel = (operState: string) => operState === 'LOWERLAYERDOWN' ? 'lower layer down' : operState.toLowerCase();

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
  value: {
    links: SavedLink[];
    segments: Segment[];
    zones: Array<{ id: string; name: string }>;
    management?: { segment: string; allowedSources?: string[] };
    aliases?: {
      addresses: Array<Partial<AddressAlias> & { id: string; name: string }>;
      ports: Array<Partial<PortAlias> & { id: string; name: string }>;
    };
    rules?: Rule[];
    nat?: {
      portForwards?: Array<{ id: string; name: string; enabled: boolean; noRedirect?: boolean; filterRule?: string; from?: string[]; to?: { address?: string } }>;
      oneToOne?: Array<{ id: string; name: string; destination?: string[] }>;
    };
  };
}

// An address alias: one host, network or range, or a group of other address aliases.
export interface AddressAlias {
  id: string;
  name: string;
  description?: string;
  kind: 'host' | 'network' | 'range' | 'group';
  address?: string;
  prefix?: string;
  first?: string;
  last?: string;
  members?: string[];
}

// A port alias: TCP or UDP ports, ICMP or ICMPv6 types, or a group of other port aliases.
export interface PortAlias {
  id: string;
  name: string;
  description?: string;
  protocol: 'tcp' | 'udp' | 'icmp' | 'icmpv6' | 'group';
  ports?: string[];
  types?: string[];
  members?: string[];
}

export interface AliasKinds { addresses: AddressAlias; ports: PortAlias }

// A collection as the engine serves it.
export interface SavedList<T> {
  state: 'saved';
  savedVersion: string;
  items: Array<SavedObject<T>>;
}

// The firewall itself is a zone no configuration stores: a rule names it as its destination to reach the box.
export const firewallZone = 'zon_firewall';

// A side of a rule: the zones traffic arrives in or leaves by, and the address aliases within them. Both must
// match. No zones is any network, never the firewall; no addresses is every address; not is "none of these".
export interface RuleSide {
  zones: string[];
  addresses: string[];
  not?: boolean;
}

export interface Rule {
  id: string;
  name: string;
  description?: string;
  enabled: boolean;
  action: 'accept' | 'drop' | 'reject';
  from: RuleSide;
  to: RuleSide;
  ports: string[];
  log: boolean;
}

// An ordered list as the engine serves it: its items in order, and the version that guards the order.
export interface OrderedList<T> extends SavedList<T> {
  version: string;
}

// A decision a breaking change needs, as the saved head was read with it (settings safe mode).
export interface UpgradeChoice { id: string; label: string; description: string }
export interface UpgradeQuestion { id: string; question: string; choices: UpgradeChoice[]; provisional: string; objects: string[]; choice: string; answered: boolean }
export interface UpgradeView { savedVersion: string; questions: UpgradeQuestion[] }

export interface UpdateDeployment {
  image?: string;
  version?: string;
  digest?: string;
  timestamp?: string;
  signature?: string;
}

export type UpdateState =
  | 'unchecked'
  | 'unsupported'
  | 'up_to_date'
  | 'available'
  | 'staged'
  | 'checking'
  | 'staging'
  | 'rolling_back'
  | 'error';

export interface UpdateStatus {
  supported: boolean;
  state: UpdateState;
  booted?: UpdateDeployment;
  available?: UpdateDeployment;
  staged?: UpdateDeployment;
  rollback?: UpdateDeployment;
  rollbackQueued?: boolean;
  checkedAt?: string;
  error?: string;
  rebootNeeded: boolean;
}

export interface SetupBootstrapRequest {
  setupCode: string;
  login: string;
  displayName: string;
  password: string;
}

export interface ConfigBatchOperation {
  op: 'create' | 'replace' | 'delete';
  collection: string;
  localId?: string;
  id?: string;
  expectedVersion?: string;
  value?: Record<string, unknown>;
}

export interface ConfigChangesResult {
  savedVersion: string;
  created?: Record<string, string>;
}

export type NotificationSeverity = 'info' | 'warning' | 'critical';

export interface Notification {
  id: number;
  source: string;
  key: string;
  severity: NotificationSeverity;
  subject: string;
  detail?: string;
  link?: string;
  requires?: string;
  raised: string;
  updated: string;
}

export interface NotificationList {
  items: Notification[];
}
