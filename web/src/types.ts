export interface Session {
  account: { id: string; login: string; displayName: string };
  capabilities: string[];
  csrfToken: string;
  expiresAt: string;
  idleTimeoutSeconds: number;
}

export interface WebSessionItem {
  id: string;
  createdAt: string;
  lastSeenAt: string;
  expiresAt: string;
  current: boolean;
}

export interface WebSessionPage {
  items: WebSessionItem[];
}

export interface AuditExportDownload {
  blob: Blob;
  filename: string;
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
  unapplied: boolean;
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
  // The kernel link kind, absent for a physical device; and the hardware address saved Ethernet links name.
  kind?: string;
  permanentAddress?: string;
  addresses: Array<{ family: 'ipv4' | 'ipv6'; address: string; prefixLength: number; scope: string }>;
}

// What the web UI keeps for the account. The engine stores it and reads none of it; keys this version does not
// know belong to another version and are kept as they are.
// columns holds the arrangements made by hand, by column count; order is the last one read across, for any other.
export interface DashboardLayout { order?: string[]; hidden?: string[]; columns?: Record<string, string[][]> }
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

export type SafeModeReason = 'boot' | 'kernelArg' | 'settingsHold';

export interface SafeModeStatus {
  active: boolean;
  reasons: SafeModeReason[];
}

// What the box is, as GET /api/v1/status/system serves it.
export interface SystemInfo {
  version: string;
  safeMode?: SafeModeStatus;
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

export type VirtualLinkKind = 'vlan' | 'bridge' | 'bond' | 'pppoe';
export type BondMode = 'balance-rr' | 'active-backup' | 'balance-xor' | 'broadcast' | '802.3ad' | 'balance-tlb' | 'balance-alb';
export type LacpRate = 'slow' | 'fast';
export type TransmitHashPolicy = 'layer2' | 'layer2+3' | 'layer3+4' | 'encap2+3' | 'encap3+4';
export type LldpReceiveMode = 'off' | 'receive' | 'routers-only';
export type LldpEmitMode = 'off' | 'nearest-bridge' | 'non-tpmr-bridge' | 'customer-bridge';
export type OffloadKey =
  | 'receiveChecksum' | 'transmitChecksum' | 'tcpSegmentation' | 'tcp6Segmentation' | 'genericSegmentation' | 'genericReceive'
  | 'genericReceiveHardware' | 'largeReceive' | 'vlanReceiveAcceleration' | 'vlanTransmitAcceleration' | 'vlanReceiveFilter' | 'ntupleFilter';

// An offload as a device runs it now; fixed: the device cannot change it; default: what Default runs on this card.
export interface OffloadState {
  active: boolean;
  fixed: boolean;
  default?: boolean;
}

export interface DeviceOffloads {
  interface: string;
  permanentAddress: string;
  offloads: Partial<Record<OffloadKey, OffloadState>>;
}
export type RaMode = 'disabled' | 'router' | 'unmanaged' | 'managed' | 'assisted' | 'stateless';
export type RaPriority = 'low' | 'medium' | 'high';

export interface RaConfig {
  mode?: string;
  priority?: string;
  routerLifetime?: number;
  validLifetime?: number;
  preferredLifetime?: number;
  dns?: { servers: string[]; searchDomains?: string[] };
  pref64?: string;
  enabled?: boolean;
  managed?: boolean;
  otherInfo?: boolean;
}

export interface SecretView {
  secret: string;
  isSet: boolean;
  updatedAt?: string;
  version: string;
  unresolved?: boolean;
}

export interface LldpNeighbor {
  ifIndex: number;
  interface: string;
  chassisId: string;
  portId: string;
  portDescription?: string;
  systemName?: string;
  systemDescription?: string;
  capabilities: string[];
}

export interface SavedLink {
  id: string;
  name: string;
  kind: 'ethernet' | 'vlan' | 'bridge' | 'bond' | 'pppoe';
  mtu: number;
  enabled: boolean;
  permanentMac?: string;
  parent?: string;
  vlanId?: number;
  username?: string;
  password?: SecretView;
  members?: string[];
  mode?: BondMode;
  stp?: boolean;
  stpPriority?: number;
  forwardDelay?: number;
  helloTime?: number;
  maxAge?: number;
  multicastSnooping?: boolean;
  lacpRate?: LacpRate;
  miiMonitorMs?: number;
  transmitHashPolicy?: TransmitHashPolicy;
  lldp?: LldpReceiveMode;
  emitLldp?: LldpEmitMode;
  offloads?: Partial<Record<OffloadKey, boolean>>;
}

export interface Segment {
  id: string;
  name: string;
  link: string;
  zone: string;
  addressing: {
    ipv4: { mode: string; addresses?: string[]; gateway?: string };
    ipv6: {
      mode: string;
      addresses?: string[];
      gateway?: string;
      from?: string;
      subnetId?: string;
      requestPrefixLength?: number;
      ra?: RaConfig;
    };
    dns?: { servers?: string[]; searchDomains?: string[] };
  };
}

export type DnsMode = 'recursive' | 'forward';
export type DnsUpstreamProtocol = 'do53' | 'dot';

export interface DnsUpstream {
  address: string;
  protocol: DnsUpstreamProtocol;
  tlsName?: string;
}

export interface DnsService {
  mode: DnsMode;
  upstreams?: DnsUpstream[];
  segments: string[];
  localDomain?: string;
  searchDomains?: string[];
  registerLeases?: boolean;
  virtualAddresses?: string[];
}

export type VirtualAddressKind = 'address' | 'proxyArp' | 'proxyNdp';

export interface VirtualAddress {
  id: string;
  name: string;
  description?: string;
  kind?: VirtualAddressKind;
  address: string;
  segment?: string;
  hostSegment?: string;
}

export type DhcpOptionType = 'string' | 'ip' | 'uint8' | 'uint16' | 'uint32' | 'boolean' | 'hex';

export interface DhcpOption {
  code: number;
  type: DhcpOptionType;
  data: string;
  alwaysSend?: boolean;
}

export interface DhcpRange {
  first: string;
  last: string;
}

export interface DhcpStaticLease {
  mac: string;
  address: string;
  hostname?: string;
}

export interface Dhcp4Entry {
  id: string;
  name: string;
  description?: string;
  segment: string;
  ranges: DhcpRange[];
  leaseTime?: number;
  gateway?: string;
  dnsServers?: string[];
  domainName?: string;
  domainSearch?: string[];
  ntpServers?: string[];
  nextServer?: string;
  bootFileName?: string;
  options?: DhcpOption[];
  staticLeases?: DhcpStaticLease[];
}

export interface Dhcp6StaticLease {
  duid: string;
  address?: string;
  prefix?: string;
  prefixId?: string;
  hostname?: string;
}

export interface Dhcp6PrefixDelegation {
  prefix?: string;
  subnetId?: string;
  length?: number;
  delegatedLength: number;
  excludedPrefix?: string;
}

export interface Dhcp6Entry {
  id: string;
  name: string;
  description?: string;
  segment: string;
  ranges?: DhcpRange[];
  leaseTime?: number;
  dnsServers?: string[];
  domainSearch?: string[];
  ntpServers?: string[];
  options?: DhcpOption[];
  staticLeases?: Dhcp6StaticLease[];
  prefixDelegation?: Dhcp6PrefixDelegation;
}

export type LeaseState = 'active' | 'declined' | 'expired' | 'released' | 'unknown';

export interface Lease4 {
  segment: string;
  address: string;
  mac: string;
  clientId?: string;
  hostname: string;
  state: LeaseState;
  cltt?: string;
  expiresAt?: string;
}

export interface Lease6 {
  segment: string;
  type: 'address' | 'prefix';
  address: string;
  duid: string;
  mac?: string;
  hostname: string;
  state: LeaseState;
  cltt?: string;
  expiresAt?: string;
}

export interface LeaseView {
  v4: Lease4[];
  v6: Lease6[];
}

export type OutboundNatMode = 'automatic' | 'none';

export interface OutboundNat {
  mode: OutboundNatMode;
}

export type NatAddress = { self: true; address?: never } | { address: string; self?: never };

export interface PortForward {
  id: string;
  name: string;
  description?: string;
  enabled: boolean;
  family?: 'ipv4' | 'ipv6';
  ingressZones: string[];
  protocol: 'tcp' | 'udp' | 'tcp-udp';
  from: string[];
  fromNegated?: boolean;
  to: NatAddress;
  toNegated?: boolean;
  ports: string;
  noRedirect?: boolean;
  target?: NatAddress;
  targetPort?: number;
  filterRule?: 'auto' | 'none';
  reflection?: boolean;
  log?: boolean;
}

export interface OneToOneNat {
  id: string;
  name: string;
  description?: string;
  enabled: boolean;
  zone: string;
  external?: string;
  internal: string;
  destination: string[];
  destinationNegated?: boolean;
  noBinat?: boolean;
  reflection?: boolean;
}

export type ZoneColor = 'green' | 'red' | 'blue' | 'orange' | 'teal' | 'pink' | 'cyan' | 'lime' | 'violet' | 'indigo';

export interface Zone {
  id: string;
  name: string;
  description?: string;
  color?: ZoneColor;
}

export interface ManagementConfig {
  segment: string;
  allowedSources?: string[];
}

export interface Snapshot {
  state: 'saved';
  savedVersion: string;
  unusedSecrets?: string[];
  value: {
    links: SavedLink[];
    segments: Segment[];
    zones: Zone[];
    management?: ManagementConfig;
    dhcp?: Dhcp4Entry[];
    dhcp6?: Dhcp6Entry[];
    dns?: DnsService;
    virtualAddresses?: VirtualAddress[];
    aliases?: {
      addresses: Array<Partial<AddressAlias> & { id: string; name: string }>;
      ports: Array<Partial<PortAlias> & { id: string; name: string }>;
    };
    rules?: Rule[];
    nat?: {
      outbound?: OutboundNat;
      portForwards?: PortForward[];
      oneToOne?: OneToOneNat[];
    };
  };
}

// An address alias: one host, network or range, or a group of other address aliases.
export interface AddressAlias {
  id: string;
  name: string;
  description?: string;
  kind: 'host' | 'network' | 'range' | 'group' | 'url' | 'hostnames';
  address?: string;
  prefix?: string;
  first?: string;
  last?: string;
  members?: string[];
  url?: string;
  names?: string[];
  refreshSeconds?: number;
}

export function isDynamicAddressAlias(alias: { kind?: string } | undefined): boolean {
  return alias?.kind === 'url' || alias?.kind === 'hostnames';
}

export interface DynamicAliasNameStatus {
  name: string;
  addresses?: string[] | null;
  error?: string;
  resolvedAt: string | null;
}

export interface DynamicAliasStatus {
  id: string;
  name: string;
  kind: 'url' | 'hostnames';
  lastSuccess: string | null;
  lastAttempt: string | null;
  entries: number;
  lastError?: string;
  failures: number;
  names?: DynamicAliasNameStatus[] | null;
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

export interface TextSegment {
  text: string;
  match: boolean;
}

export interface AliasSearchItem {
  id: string;
  name: string;
  description?: string;
  kind?: AddressAlias['kind'];
  protocol?: PortAlias['protocol'];
  summary: string;
  score: number;
  highlights: TextSegment[];
  summaryHighlights?: TextSegment[];
}

export interface AliasSearchResponse {
  state: 'saved';
  savedVersion: string;
  items: AliasSearchItem[];
}

export interface AliasSearchParams {
  type: keyof AliasKinds;
  q?: string;
  kind?: string;
  family?: 'ipv4' | 'ipv6';
  excludeGroups?: boolean;
  exclude?: string;
  limit?: number;
}

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

export interface UpdateDownloadEstimate {
  downloadBytes: number;
  downloadLayers: number;
  totalBytes?: number;
  totalLayers?: number;
}

export interface UpdateProgress {
  phase: string;
  description?: string;
  bytes?: number;
  bytesTotal?: number;
  steps?: number;
  stepsTotal?: number;
  stepsCached?: number;
  startedAt?: string;
  updatedAt?: string;
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
  download?: UpdateDownloadEstimate;
  progress?: UpdateProgress;
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

export type KnownNotificationChannelKind = 'webhook' | 'ntfy' | 'pushover' | 'telegram' | 'email';
export type NotificationChannelKind = KnownNotificationChannelKind | (string & {});
export type EmailChannelSecurity = 'tls' | 'starttls';

export interface NotificationChannelFilter {
  minSeverity?: NotificationSeverity;
  sources?: string[];
}

export interface WebhookChannelConfig {
  url: SecretView;
  authorization?: SecretView;
}

export interface NtfyChannelConfig {
  server: string;
  topic: string;
  token?: SecretView;
}

export interface PushoverChannelConfig {
  token: SecretView;
  user: SecretView;
}

export interface TelegramChannelConfig {
  token: SecretView;
  chatId: string;
}

export interface EmailChannelConfig {
  host: string;
  port: number;
  security: EmailChannelSecurity;
  username?: string;
  password?: SecretView;
  from: string;
  to: string[];
}

export interface NotificationChannel {
  id: string;
  name: string;
  description?: string;
  enabled?: boolean;
  kind: NotificationChannelKind;
  filter?: NotificationChannelFilter;
  webhook?: WebhookChannelConfig;
  ntfy?: NtfyChannelConfig;
  pushover?: PushoverChannelConfig;
  telegram?: TelegramChannelConfig;
  email?: EmailChannelConfig;
  [extraKind: string]: unknown;
}

export interface SecretWrite {
  secret: string;
  set?: string;
  expectedVersion?: string;
  unresolved?: boolean;
}

export interface NotificationChannelWrite {
  name: string;
  description?: string;
  enabled?: boolean;
  kind: NotificationChannelKind;
  filter?: NotificationChannelFilter;
  webhook?: {
    url: SecretWrite;
    authorization?: SecretWrite;
  };
  ntfy?: {
    server: string;
    topic: string;
    token?: SecretWrite;
  };
  pushover?: {
    token: SecretWrite;
    user: SecretWrite;
  };
  telegram?: {
    token: SecretWrite;
    chatId: string;
  };
  email?: {
    host: string;
    port: number;
    security: EmailChannelSecurity;
    username?: string;
    password?: SecretWrite;
    from: string;
    to: string[];
  };
}

export type BackupStore = 'configuration' | 'configurationSecrets' | 'users' | 'userCredentials';

export interface BackupCreateOptions {
  includeSecrets: boolean;
  includeCredentials?: boolean;
  passphrase?: string;
  password?: string;
}

export interface BackupDownload {
  blob: Blob;
  encrypted: boolean;
  savedVersion: string;
  identityRevision: string;
  capturedAt: string;
}

export interface BackupPayloadInfo {
  schemaVersion: number;
  size: number;
  sha256: string;
}

export interface BackupManifest {
  formatVersion: number;
  backupId: string;
  createdAt: string;
  andaruVersion: string;
  savedVersion: string;
  scopes: BackupStore[];
  payloads: Record<string, BackupPayloadInfo>;
  network: {
    live: number | null;
    pending: number | null;
    liveSavedVersion: string;
  };
  savedButUnappliedAreas: string[];
}

export interface BackupInspectionResponse {
  state: 'inspection';
  manifest: BackupManifest;
}

export interface RestoreCounts {
  created: number;
  updated: number;
  deleted: number;
}

export interface HardwareFinding {
  code: string;
  path: string;
  message: string;
}

export interface RecoveryAdministrator {
  id: string;
  login: string;
}

export interface RestorePreviewResponse {
  state: 'restore-preview';
  manifest: BackupManifest;
  expectedSavedVersion?: string;
  expectedIdentityRevision: number;
  configurationChanges: RestoreCounts;
  secretChanges: RestoreCounts;
  userChanges: RestoreCounts;
  credentialChanges: RestoreCounts;
  unresolvedSecrets: string[];
  blockingSecrets: string[];
  missingRoleIds: string[];
  requiresRootAdministrator: boolean;
  recoveryAdministrators: RecoveryAdministrator[];
  hardwareFindings: HardwareFinding[];
}

export interface RestoreSavedResponse {
  state: 'saved';
  savedVersion?: string;
  identityRevision?: number;
  unresolvedSecrets: string[];
  replayed: boolean;
}

export type AddonText = string | { i18n: string };

export interface AddonApplication {
  name: string;
  version: string;
}

export interface AddonEnumOption {
  value: string;
  title: AddonText;
  description?: AddonText;
}

export type AddonPropertyType =
  | 'string'
  | 'integer'
  | 'boolean'
  | 'enum'
  | 'string-list'
  | 'zone-reference'
  | 'secret-reference';

export interface AddonProperty {
  name: string;
  type: AddonPropertyType;
  format?: string;
  itemFormat?: string;
  pattern?: string;
  minLength?: number;
  maxLength?: number;
  minimum?: number;
  maximum?: number;
  minItems?: number;
  maxItems?: number;
  options?: AddonEnumOption[];
  required?: boolean;
  default?: unknown;
  title: AddonText;
  description?: AddonText;
}

export interface AddonInterfaceSpec {
  name: string;
  title: AddonText;
  zoneRequired?: boolean;
}

export interface AddonStorageSpec {
  name: string;
  purpose: string;
  mountPath: string;
  readOnly?: boolean;
  backup?: boolean;
}

export interface AddonEndpointSpec {
  name: string;
  protocol: string;
  port: number;
  portSetting?: string;
  exposure: string;
}

export interface AddonBusTopicSpec {
  topic: string;
  title: AddonText;
  description?: AddonText;
}

export interface AddonBusSpec {
  publish?: AddonBusTopicSpec[];
  subscribe?: AddonBusTopicSpec[];
}

export interface AddonLogSpec {
  name: string;
  title: AddonText;
  permission: string;
  source: { type: 'runtime' | 'file'; storage?: string; path?: string };
  format?: string;
}

export interface AddonWebUISpec {
  title: AddonText;
  permission: string;
  endpoint: string;
  hostnameSetting?: string;
  path: string;
}

export interface AddonManifest {
  manifestVersion: number;
  id: string;
  slug: string;
  name: string;
  description?: AddonText;
  version: string;
  application: AddonApplication;
  properties?: AddonProperty[];
  interfaces?: AddonInterfaceSpec[];
  storage?: AddonStorageSpec[];
  endpoints?: AddonEndpointSpec[];
  bus?: AddonBusSpec;
  logs?: AddonLogSpec[];
  webui?: AddonWebUISpec;
}

export type AddonBusGrantDecision = 'granted' | 'denied';

export interface AddonSavedInterface {
  name: string;
  zone?: string;
}

export interface AddonSavedBusGrants {
  publish?: Record<string, AddonBusGrantDecision>;
  subscribe?: Record<string, AddonBusGrantDecision>;
}

export interface AddonSavedEntry {
  id: string;
  enabled: boolean;
  settings?: Record<string, unknown>;
  interfaces?: AddonSavedInterface[];
  busGrants?: AddonSavedBusGrants;
}

export interface AddonStagedView {
  present: boolean;
  digest: string;
  stagedAt?: string;
  error?: string;
}

export interface AddonRuntimeView {
  applied: boolean;
  activeState: string;
  subState?: string;
  health: string;
  appliedImage?: string;
  updateAvailable: boolean;
}

export interface AddonWebUIView {
  title: AddonText;
  path: string;
  hostname?: string;
  url?: string;
  originConflict: boolean;
  conflictReason?: string;
}

export interface AddonSummary {
  id: string;
  slug: string;
  name: string;
  description?: AddonText;
  version: string;
  application: AddonApplication;
  catalogImage: string;
  installed: boolean;
  hasWebUI: boolean;
  unreviewedBusCount: number;
  staged: AddonStagedView;
  runtime: AddonRuntimeView;
  saved?: AddonSavedEntry;
}

export interface AddonListResponse {
  items: AddonSummary[];
  safeMode: SafeModeStatus;
}

export interface AddonDetailResponse extends AddonSummary {
  safeMode: SafeModeStatus;
  manifest: AddonManifest;
  webui?: AddonWebUIView;
}

export interface AddonLogsResponse {
  name: string;
  format: string;
  lines: string[];
  cursor?: string;
  truncated: boolean;
}
