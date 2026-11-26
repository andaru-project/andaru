import type {
  AliasKinds,
  AliasSearchParams,
  AliasSearchResponse,
  Applies,
  Apply,
  AuditEvent,
  BackupCreateOptions,
  BackupDownload,
  BackupInspectionResponse,
  BackupStore,
  ConfigBatchOperation,
  ConfigChangesResult,
  Dhcp4Entry,
  Dhcp6Entry,
  DnsService,
  ErrorDetail,
  Finding,
  History,
  LeaseView,
  LinkStatus,
  LldpNeighbor,
  NotificationList,
  OneToOneNat,
  OrderedList,
  OutboundNat,
  PortForward,
  SetupBootstrapRequest,
  Page,
  PrefixView,
  Preferences,
  Preview,
  RestorePreviewResponse,
  RestoreSavedResponse,
  Role,
  Rule,
  SavedList,
  SavedObject,
  Session,
  Snapshot,
  SystemInfo,
  Token,
  TokenIssueResponse,
  Traffic,
  UpdateStatus,
  UpgradeView,
  Usage,
  User,
  VersionDetail,
  VirtualAddress,
  Zone,
} from './types';

interface ErrorEnvelope {
  error?: { code?: string; message?: string; details?: ErrorDetail[]; findings?: Finding[] };
}

export class ApiError extends Error {
  constructor(
    public readonly status: number,
    public readonly code: string,
    message: string,
    public readonly details: ErrorDetail[] = [],
    // findings explain a refused apply (422 APPLY_REFUSED) in the preview's vocabulary.
    public readonly findings: Finding[] = [],
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

export function apiErrorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    const details = error.details.map((detail) => detail.message).filter(Boolean).join(' ');
    return details ? `${error.message} ${details}` : error.message;
  }
  return 'The request could not be completed.';
}

// fieldErrors sorts an engine refusal by the form field each detail is about. The engine names fields by path
// ("/from/zones/0"); field picks the form's field out of one, and a detail about no field of the form goes
// under "", to show above it. A field refused more than once for one reason says it once.
export function fieldErrors(error: unknown, field: RegExp | ((path: string) => string)) {
  const said = new Map<string, Set<string>>();
  if (!(error instanceof ApiError)) return new Map<string, string>();
  for (const detail of error.details) {
    const path = detail.path ?? '';
    const name = typeof field === 'function' ? field(path) : field.exec(path)?.[1] ?? '';
    said.set(name, (said.get(name) ?? new Set()).add(detail.message));
  }
  return new Map([...said].map(([name, messages]) => [name, [...messages].join(' ')]));
}

// saveFailure is what a side panel says above its form when a save was refused. Someone else's change
// underneath keeps what was typed and says what happened; refusals beside their fields are also said once,
// where the form begins, for whoever hears the page.
export function saveFailure(error: unknown, fields: Map<string, string>, what: string) {
  if (error instanceof ApiError && error.status === 412) return `This ${what} was changed by someone else while it was open here. Close the panel to see what is saved, then make the change again.`;
  if (error instanceof ApiError && error.status === 404) return `This ${what} was deleted by someone else while it was open here. Close the panel; to keep it, add it again.`;
  if (fields.size === 0 || fields.has('')) return fields.get('') ?? apiErrorMessage(error);
  return 'Some of the fields below were refused.';
}

// reportError signs the session out on a 401 and shows any other error's message.
export function reportError(error: unknown, onSessionLost: () => void, setMessage: (message: string) => void) {
  if (error instanceof ApiError && error.status === 401) onSessionLost();
  else setMessage(apiErrorMessage(error));
}

function operationID() {
  return crypto.randomUUID();
}

const readTimeoutMilliseconds = 30_000;

function combineSignals(caller?: AbortSignal, timeout?: AbortSignal): { signal?: AbortSignal; cleanup(): void } {
  if (!caller) return { signal: timeout, cleanup: () => {} };
  if (!timeout) return { signal: caller, cleanup: () => {} };
  if (typeof AbortSignal.any === 'function') {
    return { signal: AbortSignal.any([caller, timeout]), cleanup: () => {} };
  }
  const controller = new AbortController();
  const onCallerAbort = () => controller.abort(caller.reason);
  const onTimeoutAbort = () => controller.abort(timeout.reason);
  if (caller.aborted) {
    controller.abort(caller.reason);
    return { signal: controller.signal, cleanup: () => {} };
  }
  if (timeout.aborted) {
    controller.abort(timeout.reason);
    return { signal: controller.signal, cleanup: () => {} };
  }
  caller.addEventListener('abort', onCallerAbort, { once: true });
  timeout.addEventListener('abort', onTimeoutAbort, { once: true });
  return {
    signal: controller.signal,
    cleanup() {
      caller.removeEventListener('abort', onCallerAbort);
      timeout.removeEventListener('abort', onTimeoutAbort);
    },
  };
}

// A read with no user input since the previous request is a poll: the engine does not count it as activity, so a
// screen left open still signs out after the idle timeout. Loading the page counts as input.
// The first request after input carries it to the engine; the others it starts can go as polls.
let input = true;
for (const type of ['pointerdown', 'keydown', 'wheel']) {
  document.addEventListener(type, () => { input = true; }, { capture: true, passive: true });
}

export function consumeUserActivity(): boolean {
  const carried = input;
  input = false;
  return carried;
}

export class ApiClient {
  // onConnection hears whether each request got a response, for the shell's connection state.
  constructor(private csrfToken = '', private readonly onConnection?: (connected: boolean) => void) {}

  setCSRFToken(token: string) {
    this.csrfToken = token;
  }

  clearSession() {
    this.csrfToken = '';
  }

  private disconnectedError() {
    this.onConnection?.(false);
    return new ApiError(0, 'DISCONNECTED', 'Cannot reach the Andaru service. Check the connection and try again.');
  }

  private toApiError(response: Response, body: unknown): ApiError {
    const envelope = body as ErrorEnvelope | undefined;
    const code = envelope?.error?.code ?? `HTTP_${response.status}`;
    const message = envelope?.error?.message ?? 'The server refused the request.';
    if (response.status === 401) this.clearSession();
    return new ApiError(response.status, code, message, envelope?.error?.details ?? [], envelope?.error?.findings ?? []);
  }

  private async send(
    path: string,
    init: RequestInit = {},
    csrf = false,
    accept = 'application/json',
  ): Promise<{ response: Response; carried: boolean; callerSignal?: AbortSignal; signal?: AbortSignal; cleanup(): void }> {
    const headers = new Headers(init.headers);
    headers.set('Accept', accept);
    if (init.body && !(init.body instanceof FormData)) headers.set('Content-Type', 'application/json');
    if (csrf && this.csrfToken) headers.set('X-Andaru-CSRF', this.csrfToken);
    // No caller passes a signal or names GET, so a request without a method is a read.
    const read = !init.method;
    const carried = input;
    if (read && !carried) headers.set('X-Andaru-Background', '1');
    input = false;

    // A read that never answers would hold its page's polling for good; a write may take as long as it takes.
    // Combine an optional caller AbortSignal so cancelling a superseded search never marks the session disconnected.
    const callerSignal = init.signal ?? undefined;
    const timeoutSignal = read ? AbortSignal.timeout(readTimeoutMilliseconds) : undefined;
    const { signal, cleanup } = combineSignals(callerSignal, timeoutSignal);
    let response: Response;
    try {
      response = await fetch(path, { ...init, headers, signal, credentials: 'same-origin', cache: 'no-store' });
    } catch (err) {
      cleanup();
      if (callerSignal?.aborted) {
        input ||= carried;
        throw err;
      }
      // Most likely the engine never received it, so the input waits for the next request. A read that timed out
      // may have reached it, and restoring there would let one hung read keep the session alive.
      if (!signal?.aborted) input ||= carried;
      throw this.disconnectedError();
    }
    this.onConnection?.(true);
    return { response, carried, callerSignal, signal, cleanup };
  }

  private async request<T>(path: string, init: RequestInit = {}, csrf = false): Promise<T> {
    const { response, carried, callerSignal, signal, cleanup } = await this.send(path, init, csrf);
    let body: unknown;
    try {
      if (response.status === 204) return undefined as T;
      try {
        body = await response.json();
      } catch (err) {
        if (callerSignal?.aborted) {
          input ||= carried;
          throw err;
        }
        // The limit can also pass while the body arrives; that is no answer, not an empty one.
        if (signal?.aborted) throw this.disconnectedError();
        body = undefined;
      }
    } finally {
      cleanup();
    }
    if (!response.ok) throw this.toApiError(response, body);
    return body as T;
  }

  async login(login: string, password: string) {
    const session = await this.request<Session>('/api/v1/session', {
      method: 'POST', body: JSON.stringify({ login, password }),
    });
    this.setCSRFToken(session.csrfToken);
    return session;
  }

  async currentSession() {
    const session = await this.request<Session>('/api/v1/session');
    this.setCSRFToken(session.csrfToken);
    return session;
  }

  async logout() {
    try {
      await this.request<void>('/api/v1/session', { method: 'DELETE' }, true);
    } finally {
      this.clearSession();
    }
  }

  startupStatus() {
    return this.request<{ setupRequired: boolean }>('/api/v1/startup-status');
  }

  async bootstrapSetup(input: SetupBootstrapRequest) {
    const session = await this.request<Session>('/api/v1/setup/bootstrap', {
      method: 'POST',
      body: JSON.stringify(input),
    });
    this.setCSRFToken(session.csrfToken);
    return session;
  }

  setHostname(hostname: string) {
    return this.request<{ hostname: string }>('/api/v1/system/hostname', {
      method: 'POST',
      body: JSON.stringify({ hostname }),
    }, true);
  }

  applyConfigChanges(operations: ConfigBatchOperation[], expectedSavedVersion?: string) {
    const body: { operations: ConfigBatchOperation[]; expectedSavedVersion?: string } = { operations };
    if (expectedSavedVersion !== undefined) body.expectedSavedVersion = expectedSavedVersion;
    return this.request<ConfigChangesResult>('/api/v1/config/changes', {
      method: 'POST',
      body: JSON.stringify(body),
    }, true);
  }

  async singletonVersion(singleton: 'management' | 'dns' | 'nat/outbound'): Promise<string> {
    try {
      const obj = await this.request<{ version: string }>(`/api/v1/config/${singleton}`);
      return obj.version || '0';
    } catch (error) {
      if (error instanceof ApiError && error.status === 404) return '0';
      throw error;
    }
  }

  listUsers(cursor = '') {
    const query = new URLSearchParams({ limit: '100' });
    if (cursor) query.set('cursor', cursor);
    return this.request<Page<User>>(`/api/v1/users?${query}`);
  }

  getUser(id: string) {
    return this.request<User>(`/api/v1/users/${encodeURIComponent(id)}`);
  }

  createUser(input: { login: string; displayName: string; kind: User['kind']; enabled: boolean; roleIds: string[]; expectedRevision: number }) {
    return this.request('/api/v1/users', {
      method: 'POST', headers: { 'Idempotency-Key': operationID() }, body: JSON.stringify(input),
    }, true);
  }

  updateUser(id: string, input: Partial<Pick<User, 'login' | 'displayName' | 'kind' | 'enabled' | 'roleIds'>> & { expectedRevision: number }) {
    return this.request(`/api/v1/users/${encodeURIComponent(id)}`, {
      method: 'PATCH', headers: { 'Idempotency-Key': operationID() }, body: JSON.stringify(input),
    }, true);
  }

  deleteUser(id: string, revision: number) {
    return this.request(`/api/v1/users/${encodeURIComponent(id)}`, {
      method: 'DELETE', headers: { 'Idempotency-Key': operationID(), 'If-Match': String(revision) },
    }, true);
  }

  changePassword(id: string, input: { currentPassword: string; newPassword: string; expectedRevision: number }) {
    return this.request(`/api/v1/users/${encodeURIComponent(id)}/password`, {
      method: 'PUT', headers: { 'Idempotency-Key': operationID() }, body: JSON.stringify(input),
    }, true);
  }

  liveRoles(cursor = '') {
    const query = new URLSearchParams({ limit: '100' });
    if (cursor) query.set('cursor', cursor);
    return this.request<Page<Role>>(`/api/v1/roles?${query}`);
  }

  async allLiveRoles() {
    const roles: Role[] = [];
    let cursor = '';
    do {
      const page = await this.liveRoles(cursor);
      roles.push(...page.items);
      cursor = page.nextCursor ?? '';
    } while (cursor);
    return roles;
  }

  savedRoles() {
    return this.request<SavedList<Role>>('/api/v1/config/roles');
  }

  capabilities(cursor = '') {
    const query = new URLSearchParams({ limit: '100' });
    if (cursor) query.set('cursor', cursor);
    return this.request<Page<string>>(`/api/v1/capabilities?${query}`);
  }

  createRole(role: Omit<Role, 'id' | 'builtIn'>) {
    return this.request<SavedObject<Role>>('/api/v1/config/roles', { method: 'POST', body: JSON.stringify(role) }, true);
  }

  updateRole(role: Role, version: string) {
    const value = { name: role.name, description: role.description ?? '', capabilities: role.capabilities };
    return this.request<SavedObject<Role>>(`/api/v1/config/roles/${encodeURIComponent(role.id)}`, {
      method: 'PUT', headers: { 'If-Match': `"${role.id}.${version}"` }, body: JSON.stringify(value),
    }, true);
  }

  deleteRole(id: string, version: string) {
    return this.request(`/api/v1/config/roles/${encodeURIComponent(id)}`, {
      method: 'DELETE', headers: { 'If-Match': `"${id}.${version}"` },
    }, true);
  }

  listApplies() {
    return this.request<Applies>('/api/v1/applies');
  }

  apply(savedVersion: string, confirmTimeoutSeconds: number) {
    return this.request<Apply>('/api/v1/applies', {
      method: 'POST', body: JSON.stringify({ savedVersion, confirmTimeoutSeconds }),
    }, true);
  }

  versions(limit: number, before = '') {
    const query = new URLSearchParams({ limit: String(limit) });
    if (before) query.set('before', before);
    return this.request<History>(`/api/v1/versions?${query}`);
  }

  version(id: string) {
    return this.request<VersionDetail>(`/api/v1/versions/${encodeURIComponent(id)}`);
  }

  previewNetwork(savedVersion = '') {
    const query = savedVersion ? `?${new URLSearchParams({ savedVersion })}` : '';
    return this.request<Preview>(`/api/v1/preview/network${query}`);
  }

  links() {
    return this.request<{ items: LinkStatus[] }>('/api/v1/status/links');
  }

  usage() {
    return this.request<Usage>('/api/v1/status/usage');
  }

  // since is the time of the newest reading held, as the box wrote it: only what came after is sent.
  traffic(since?: string) {
    return this.request<Traffic>(`/api/v1/status/traffic${since ? `?${new URLSearchParams({ since })}` : ''}`);
  }

  system() {
    return this.request<SystemInfo>('/api/v1/status/system');
  }

  prefixes() {
    return this.request<PrefixView>('/api/v1/status/prefixes');
  }

  leases() {
    return this.request<LeaseView>('/api/v1/status/leases');
  }

  lldpNeighbors() {
    return this.request<{ items: LldpNeighbor[] }>('/api/v1/status/lldp');
  }

  preferences() {
    return this.request<Preferences>('/api/v1/session/preferences');
  }

  savePreferences(preferences: Preferences) {
    return this.request<Preferences>('/api/v1/session/preferences', { method: 'PUT', body: JSON.stringify(preferences) }, true);
  }

  config() {
    return this.request<Snapshot>('/api/v1/config');
  }

  // One saved object with its version, for a change guarded against someone else's.
  configObject<T>(collection: string, id: string) {
    return this.request<SavedObject<T>>(`/api/v1/config/${collection}/${encodeURIComponent(id)}`);
  }

  aliases<K extends keyof AliasKinds>(kind: K) {
    return this.request<SavedList<AliasKinds[K]>>(`/api/v1/config/aliases/${kind}`);
  }

  searchAliases(params: AliasSearchParams, signal?: AbortSignal) {
    const query = new URLSearchParams({ type: params.type });
    if (params.q !== undefined && params.q !== '') query.set('q', params.q);
    if (params.kind) query.set('kind', params.kind);
    if (params.family) query.set('family', params.family);
    if (params.excludeGroups) query.set('excludeGroups', 'true');
    if (params.exclude) query.set('exclude', params.exclude);
    if (params.limit !== undefined) query.set('limit', String(params.limit));
    return this.request<AliasSearchResponse>(`/api/v1/config/aliases/search?${query.toString()}`, signal ? { signal } : {});
  }

  // An object is saved whole: created without a saved one, and otherwise replaced against its version.
  private save<T extends { id: string }>(path: string, value: Omit<T, 'id'>, saved?: SavedObject<T>) {
    if (!saved) return this.request<SavedObject<T>>(path, { method: 'POST', body: JSON.stringify(value) }, true);
    return this.request<SavedObject<T>>(`${path}/${encodeURIComponent(saved.value.id)}`, {
      method: 'PUT', headers: { 'If-Match': `"${saved.value.id}.${saved.version}"` }, body: JSON.stringify(value),
    }, true);
  }

  saveAlias<K extends keyof AliasKinds>(kind: K, alias: Omit<AliasKinds[K], 'id'>, saved?: SavedObject<AliasKinds[K]>) {
    return this.save(`/api/v1/config/aliases/${kind}`, alias, saved);
  }

  deleteAlias(kind: keyof AliasKinds, id: string, version: string) {
    return this.request(`/api/v1/config/aliases/${kind}/${encodeURIComponent(id)}`, { method: 'DELETE', headers: { 'If-Match': `"${id}.${version}"` } }, true);
  }

  zones() {
    return this.request<SavedList<Zone>>('/api/v1/config/zones');
  }

  saveZone(zone: Omit<Zone, 'id'>, saved?: SavedObject<Zone>) {
    return this.save('/api/v1/config/zones', zone, saved);
  }

  deleteZone(id: string, version: string) {
    return this.request(`/api/v1/config/zones/${encodeURIComponent(id)}`, { method: 'DELETE', headers: { 'If-Match': `"${id}.${version}"` } }, true);
  }

  virtualAddresses() {
    return this.request<SavedList<VirtualAddress>>('/api/v1/config/virtualAddresses');
  }

  saveVirtualAddress(vip: Omit<VirtualAddress, 'id'>, saved?: SavedObject<VirtualAddress>) {
    return this.save('/api/v1/config/virtualAddresses', vip, saved);
  }

  deleteVirtualAddress(id: string, version: string) {
    return this.request(`/api/v1/config/virtualAddresses/${encodeURIComponent(id)}`, { method: 'DELETE', headers: { 'If-Match': `"${id}.${version}"` } }, true);
  }

  rules() {
    return this.request<OrderedList<Rule>>('/api/v1/config/rules');
  }

  // A new rule goes to the end of the list.
  saveRule(rule: Omit<Rule, 'id'>, saved?: SavedObject<Rule>) {
    return this.save('/api/v1/config/rules', rule, saved);
  }

  deleteRule(id: string, version: string) {
    return this.request(`/api/v1/config/rules/${encodeURIComponent(id)}`, { method: 'DELETE', headers: { 'If-Match': `"${id}.${version}"` } }, true);
  }

  // The whole order, every rule once, guarded by the list's version: a 412 means someone else reordered.
  orderRules(ids: string[], version: string) {
    return this.request('/api/v1/config/rules/order', { method: 'PUT', headers: { 'If-Match': `"order.${version}"` }, body: JSON.stringify(ids) }, true);
  }

  async outboundNat(): Promise<SavedObject<OutboundNat> | null> {
    try {
      return await this.request<SavedObject<OutboundNat>>('/api/v1/config/nat/outbound');
    } catch (error) {
      if (error instanceof ApiError && error.status === 404) return null;
      throw error;
    }
  }

  saveOutboundNat(value: OutboundNat, version: string) {
    return this.request<SavedObject<OutboundNat>>('/api/v1/config/nat/outbound', {
      method: 'PUT', headers: { 'If-Match': `"nat.outbound.${version}"` }, body: JSON.stringify(value),
    }, true);
  }

  portForwards() {
    return this.request<OrderedList<PortForward>>('/api/v1/config/nat/port-forwards');
  }

  savePortForward(entry: Omit<PortForward, 'id'>, saved?: SavedObject<PortForward>) {
    return this.save('/api/v1/config/nat/port-forwards', entry, saved);
  }

  deletePortForward(id: string, version: string) {
    return this.request(`/api/v1/config/nat/port-forwards/${encodeURIComponent(id)}`, { method: 'DELETE', headers: { 'If-Match': `"${id}.${version}"` } }, true);
  }

  orderPortForwards(ids: string[], version: string) {
    return this.request('/api/v1/config/nat/port-forwards/order', { method: 'PUT', headers: { 'If-Match': `"order.${version}"` }, body: JSON.stringify(ids) }, true);
  }

  oneToOneNats() {
    return this.request<OrderedList<OneToOneNat>>('/api/v1/config/nat/one-to-one');
  }

  saveOneToOneNat(entry: Omit<OneToOneNat, 'id'>, saved?: SavedObject<OneToOneNat>) {
    return this.save('/api/v1/config/nat/one-to-one', entry, saved);
  }

  deleteOneToOneNat(id: string, version: string) {
    return this.request(`/api/v1/config/nat/one-to-one/${encodeURIComponent(id)}`, { method: 'DELETE', headers: { 'If-Match': `"${id}.${version}"` } }, true);
  }

  orderOneToOneNats(ids: string[], version: string) {
    return this.request('/api/v1/config/nat/one-to-one/order', { method: 'PUT', headers: { 'If-Match': `"order.${version}"` }, body: JSON.stringify(ids) }, true);
  }

  saveDhcp4(entry: Omit<Dhcp4Entry, 'id'>, saved?: SavedObject<Dhcp4Entry>) {
    return this.save('/api/v1/config/dhcp', entry, saved);
  }

  deleteDhcp4(id: string, version: string) {
    return this.request(`/api/v1/config/dhcp/${encodeURIComponent(id)}`, { method: 'DELETE', headers: { 'If-Match': `"${id}.${version}"` } }, true);
  }

  saveDhcp6(entry: Omit<Dhcp6Entry, 'id'>, saved?: SavedObject<Dhcp6Entry>) {
    return this.save('/api/v1/config/dhcp6', entry, saved);
  }

  deleteDhcp6(id: string, version: string) {
    return this.request(`/api/v1/config/dhcp6/${encodeURIComponent(id)}`, { method: 'DELETE', headers: { 'If-Match': `"${id}.${version}"` } }, true);
  }

  async dns(): Promise<SavedObject<DnsService> | null> {
    try {
      return await this.request<SavedObject<DnsService>>('/api/v1/config/dns');
    } catch (error) {
      if (error instanceof ApiError && error.status === 404) return null;
      throw error;
    }
  }

  saveDns(value: DnsService, version: string) {
    return this.request<SavedObject<DnsService>>('/api/v1/config/dns', {
      method: 'PUT', headers: { 'If-Match': `"dns.${version}"` }, body: JSON.stringify(value),
    }, true);
  }

  deleteDns(version: string) {
    return this.request('/api/v1/config/dns', { method: 'DELETE', headers: { 'If-Match': `"dns.${version}"` } }, true);
  }

  // The head's questions after an upgrade, with the choices given so far. It saves nothing.
  upgradePreview(choices: Record<string, string> = {}) {
    return this.request<UpgradeView>('/api/v1/config/upgrade/preview', { method: 'POST', body: JSON.stringify({ choices }) }, true);
  }

  // Answers every question of the head the questions came from and saves the result.
  finishUpgrade(choices: Record<string, string>, expectedSavedVersion: string) {
    return this.request<{ savedVersion: string }>('/api/v1/config/upgrade', { method: 'POST', body: JSON.stringify({ choices, expectedSavedVersion }) }, true);
  }

  finishApply(id: string, action: 'confirmation' | 'rollback') {
    return this.request<Apply>(`/api/v1/applies/${encodeURIComponent(id)}/${action}`, { method: 'POST' }, true);
  }

  listTokens(accountId: string, cursor = '') {
    const query = new URLSearchParams({ accountId, limit: '100' });
    if (cursor) query.set('cursor', cursor);
    return this.request<Page<Token>>(`/api/v1/tokens?${query}`);
  }

  createToken(input: { accountId: string; name: string; scope: string[]; expiresAt?: string; noExpiry: boolean; revokeOnPasswordChange?: boolean }) {
    return this.request<TokenIssueResponse>('/api/v1/tokens', {
      method: 'POST', headers: { 'Idempotency-Key': operationID() }, body: JSON.stringify(input),
    }, true);
  }

  revokeToken(selector: string) {
    return this.request(`/api/v1/tokens/${encodeURIComponent(selector)}`, {
      method: 'DELETE', headers: { 'Idempotency-Key': operationID() },
    }, true);
  }

  // Newest first; the cursor pages towards older events.
  listAudit(cursor = '', limit = 100) {
    const query = new URLSearchParams({ order: 'desc', limit: String(limit) });
    if (cursor) query.set('cursor', cursor);
    return this.request<Page<AuditEvent>>(`/api/v1/audit/events?${query}`);
  }

  updateStatus() {
    return this.request<UpdateStatus>('/api/v1/system/update');
  }

  checkUpdate() {
    return this.request<UpdateStatus>('/api/v1/system/update/check', { method: 'POST' }, true);
  }

  stageUpdate() {
    return this.request<UpdateStatus>('/api/v1/system/update/stage', { method: 'POST' }, true);
  }

  rollbackUpdate() {
    return this.request<UpdateStatus>('/api/v1/system/update/rollback', { method: 'POST' }, true);
  }

  rebootSystem() {
    return this.request<{ rebooting: boolean }>('/api/v1/system/update/reboot', { method: 'POST' }, true);
  }

  notifications() {
    return this.request<NotificationList>('/api/v1/notifications');
  }

  dismissNotification(id: number) {
    return this.request<void>(`/api/v1/notifications/${id}/dismiss`, { method: 'POST' }, true);
  }

  async createBackup(options: BackupCreateOptions): Promise<BackupDownload> {
    const { response, cleanup } = await this.send(
      '/api/v1/backups',
      { method: 'POST', body: JSON.stringify(options) },
      true,
      'application/zip, application/octet-stream, application/json',
    );
    try {
      if (!response.ok) {
        let body: unknown;
        try {
          body = await response.json();
        } catch {
          body = undefined;
        }
        throw this.toApiError(response, body);
      }
      let blob: Blob;
      try {
        blob = await response.blob();
      } catch {
        throw this.disconnectedError();
      }
      const contentType = response.headers.get('Content-Type') ?? 'application/octet-stream';
      return {
        blob,
        encrypted: contentType.toLowerCase().startsWith('application/octet-stream'),
        savedVersion: response.headers.get('Andaru-Saved-Version') ?? '',
        identityRevision: response.headers.get('Andaru-Identity-Revision') ?? '',
        capturedAt: response.headers.get('Andaru-Captured-At') ?? '',
      };
    } finally {
      cleanup();
    }
  }

  private postRestoreForm<T>(path: string, archive: Blob, options: Record<string, unknown>) {
    const body = new FormData();
    body.append('archive', archive);
    body.append('options', JSON.stringify(options));
    return this.request<T>(path, { method: 'POST', body }, true);
  }

  inspectBackup(archive: Blob, passphrase?: string) {
    return this.postRestoreForm<BackupInspectionResponse>('/api/v1/restores/preview', archive, passphrase ? { passphrase } : {});
  }

  previewRestore(archive: Blob, options: { passphrase?: string; stores: BackupStore[] }) {
    return this.postRestoreForm<RestorePreviewResponse>('/api/v1/restores/preview', archive, options);
  }

  executeRestore(
    archive: Blob,
    options: {
      passphrase?: string;
      stores: BackupStore[];
      operationId: string;
      expectedSavedVersion?: string;
      expectedIdentityRevision?: number;
    },
  ) {
    return this.postRestoreForm<RestoreSavedResponse>('/api/v1/restores', archive, options);
  }
}
