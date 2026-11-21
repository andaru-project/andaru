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

interface PrefixPlace { interface: string; subnetId: string; prefix: string | null }

export interface PrefixView {
  wans: Array<{ interface: string; current: { prefix: string } | null; segments: PrefixPlace[]; pools: PrefixPlace[]; notFitting: string[] }>;
}

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
