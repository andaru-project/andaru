import type {
  Applies,
  Apply,
  AuditEvent,
  ErrorDetail,
  Finding,
  History,
  LinkStatus,
  Page,
  PrefixView,
  Preferences,
  Preview,
  Role,
  SavedObject,
  SavedRolePage,
  Session,
  Snapshot,
  SystemInfo,
  Token,
  TokenIssueResponse,
  Traffic,
  Usage,
  User,
  VersionDetail,
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

// reportError signs the session out on a 401 and shows any other error's message.
export function reportError(error: unknown, onSessionLost: () => void, setMessage: (message: string) => void) {
  if (error instanceof ApiError && error.status === 401) onSessionLost();
  else setMessage(apiErrorMessage(error));
}

function operationID() {
  return crypto.randomUUID();
}

const readTimeoutMilliseconds = 30_000;

export class ApiClient {
  // onConnection hears whether each request got a response, for the shell's connection state.
  constructor(private csrfToken = '', private readonly onConnection?: (connected: boolean) => void) {}

  setCSRFToken(token: string) {
    this.csrfToken = token;
  }

  clearSession() {
    this.csrfToken = '';
  }

  private async request<T>(path: string, init: RequestInit = {}, csrf = false): Promise<T> {
    const headers = new Headers(init.headers);
    headers.set('Accept', 'application/json');
    if (init.body) headers.set('Content-Type', 'application/json');
    if (csrf && this.csrfToken) headers.set('X-Andaru-CSRF', this.csrfToken);

    // A read that never answers would hold its page's polling for good; a write may take as long as it takes.
    // No caller passes a signal or names GET, so a request without a method is a read.
    const signal = init.method ? undefined : AbortSignal.timeout(readTimeoutMilliseconds);
    const disconnected = () => {
      this.onConnection?.(false);
      return new ApiError(0, 'DISCONNECTED', 'Cannot reach the Andaru service. Check the connection and try again.');
    };
    let response: Response;
    try {
      response = await fetch(path, { ...init, headers, signal, credentials: 'same-origin', cache: 'no-store' });
    } catch {
      throw disconnected();
    }

    this.onConnection?.(true);
    if (response.status === 204) return undefined as T;
    let body: unknown;
    try {
      body = await response.json();
    } catch {
      // The limit can also pass while the body arrives; that is no answer, not an empty one.
      if (signal?.aborted) throw disconnected();
      body = undefined;
    }
    if (!response.ok) {
      const envelope = body as ErrorEnvelope | undefined;
      const code = envelope?.error?.code ?? `HTTP_${response.status}`;
      const message = envelope?.error?.message ?? 'The server refused the request.';
      if (response.status === 401) this.clearSession();
      throw new ApiError(response.status, code, message, envelope?.error?.details ?? [], envelope?.error?.findings ?? []);
    }
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
    return this.request<SavedRolePage>('/api/v1/config/roles');
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

  traffic() {
    return this.request<Traffic>('/api/v1/status/traffic');
  }

  system() {
    return this.request<SystemInfo>('/api/v1/status/system');
  }

  prefixes() {
    return this.request<PrefixView>('/api/v1/status/prefixes');
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

  listAudit(cursor = '') {
    const query = new URLSearchParams({ limit: '100' });
    if (cursor) query.set('cursor', cursor);
    return this.request<Page<AuditEvent>>(`/api/v1/audit/events?${query}`);
  }
}
