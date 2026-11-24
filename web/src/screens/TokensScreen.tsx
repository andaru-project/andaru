import { Alert, Badge, Button, Group, Loader, Modal, ScrollArea, Select, Stack, Table, Text, Textarea, Title } from '@mantine/core';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { ApiClient, ApiError, apiErrorMessage } from '../api';
import { formatTimestamp, useTimeZone } from '../timezone';
import type { Page, Role, Session, Token, TokenIssueResponse, User } from '../types';
import { TokenDialog } from './TokenDialog';

interface TokensScreenProps { api: ApiClient; session: Session; onSessionLost(): void }

export function TokensScreen({ api, session, onSessionLost }: TokensScreenProps) {
  const timeZone = useTimeZone();
  const canManageAll = session.capabilities.includes('tokens.manageAll');
  const ownUser = useMemo<User>(() => ({ id: session.account.id, login: session.account.login, displayName: session.account.displayName, kind: 'human', enabled: true, roleIds: [], revision: 0, createdAt: 0, updatedAt: 0 }), [session.account]);
  const [users, setUsers] = useState<User[]>([ownUser]);
  const [roles, setRoles] = useState<Role[]>([]);
  const [nextUsers, setNextUsers] = useState('');
  const [accountId, setAccountId] = useState(session.account.id);
  const [tokens, setTokens] = useState<Token[]>([]);
  const [nextTokens, setNextTokens] = useState('');
  const [creating, setCreating] = useState(false);
  const [issued, setIssued] = useState<TokenIssueResponse | null>(null);
  const [revoke, setRevoke] = useState<Token | null>(null);
  const [copied, setCopied] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const handleError = useCallback((requestError: unknown) => {
    if (requestError instanceof ApiError && requestError.status === 401) onSessionLost();
    else setError(apiErrorMessage(requestError));
  }, [onSessionLost]);

  const loadTokens = useCallback(async (target: string) => {
    const page = await api.listTokens(target);
    setTokens(page.items);
    setNextTokens(page.nextCursor ?? '');
  }, [api]);

  useEffect(() => {
    async function load() {
      setLoading(true);
      setError('');
      try {
        let userPage: Page<User> = { items: [ownUser] };
        let liveRoles: Role[] = [];
        if (canManageAll) [userPage, liveRoles] = await Promise.all([api.listUsers(), api.allLiveRoles()]);
        setUsers(userPage.items.some((user) => user.id === ownUser.id) ? userPage.items : [ownUser, ...userPage.items]);
        setNextUsers(userPage.nextCursor ?? '');
        setRoles(liveRoles);
        await loadTokens(session.account.id);
      } catch (requestError) {
        handleError(requestError);
      } finally {
        setLoading(false);
      }
    }
    void load();
  }, [api, canManageAll, handleError, loadTokens, ownUser, session.account.id]);

  const target = users.find((user) => user.id === accountId) ?? ownUser;
  const availableCapabilities = useMemo(() => {
    if (target.id === session.account.id) return session.capabilities;
    const targetCapabilities = new Set(target.roleIds.flatMap((roleId) => roles.find((role) => role.id === roleId)?.capabilities ?? []));
    return session.capabilities.filter((capability) => targetCapabilities.has(capability));
  }, [roles, session.account.id, session.capabilities, target]);

  async function selectAccount(value: string | null) {
    if (!value) return;
    setAccountId(value);
    setLoading(true);
    try {
      await loadTokens(value);
    } catch (requestError) {
      handleError(requestError);
    } finally {
      setLoading(false);
    }
  }

  async function loadMoreUsers() {
    try {
      const page = await api.listUsers(nextUsers);
      setUsers((current) => [...current, ...page.items.filter((item) => !current.some((existing) => existing.id === item.id))]);
      setNextUsers(page.nextCursor ?? '');
    } catch (requestError) {
      handleError(requestError);
    }
  }

  async function loadMoreTokens() {
    try {
      const page = await api.listTokens(accountId, nextTokens);
      setTokens((current) => [...current, ...page.items]);
      setNextTokens(page.nextCursor ?? '');
    } catch (requestError) {
      handleError(requestError);
    }
  }

  async function revokeToken() {
    if (!revoke) return;
    try {
      await api.revokeToken(revoke.selector);
      setRevoke(null);
      await loadTokens(accountId);
    } catch (requestError) {
      handleError(requestError);
    }
  }

  const bearer = issued?.secret ? `${issued.selector}.${issued.secret}` : '';
  async function copyBearer() {
    if (!bearer || !navigator.clipboard) return;
    try {
      await navigator.clipboard.writeText(bearer);
      setCopied(true);
    } catch {
      setCopied(false);
    }
  }

  return (
    <Stack>
      <Group justify="space-between" align="flex-end"><div><Title order={2}>API tokens</Title><Text c="dimmed">Scoped bearer credentials. Secrets are never shown again.</Text></div><Button onClick={() => setCreating(true)}>Create token</Button></Group>
      {error && <Alert color="red" role="alert" title="Token request failed">{error}</Alert>}
      {canManageAll && <Group align="flex-end"><Select data={users.map((user) => ({ value: user.id, label: `${user.displayName || user.login} (${user.login})` }))} label="Token owner" searchable value={accountId} onChange={(value) => void selectAccount(value)} />{nextUsers && <Button variant="default" onClick={() => void loadMoreUsers()}>Load more users</Button>}</Group>}
      {loading ? <Loader aria-label="Loading tokens" /> : tokens.length === 0 ? <Alert role="status">No tokens for this account.</Alert> : (
        <ScrollArea><Table className="data-table" striped><Table.Thead><Table.Tr><Table.Th>Name</Table.Th><Table.Th>Scope</Table.Th><Table.Th>Expiration</Table.Th><Table.Th>Last used</Table.Th><Table.Th>Actions</Table.Th></Table.Tr></Table.Thead><Table.Tbody>{tokens.map((token) => <Table.Tr key={token.selector}><Table.Td data-label="Name"><Text fw={600}>{token.name}</Text><Text c="dimmed" size="xs">{token.selector}</Text></Table.Td><Table.Td data-label="Scope">{token.scope.join(', ') || 'No capabilities'}</Table.Td><Table.Td data-label="Expiration" className="tabular">{token.noExpiry ? <Badge color="yellow">No expiry</Badge> : token.expiresAt ? formatTimestamp(token.expiresAt, timeZone) : '—'}</Table.Td><Table.Td data-label="Last used" className="tabular">{token.lastUsedAt ? formatTimestamp(token.lastUsedAt, timeZone) : 'Never'}</Table.Td><Table.Td data-label="Actions">{token.revokedAt ? <Badge color="gray">Revoked</Badge> : <Button aria-label={`Revoke ${token.name}`} color="red" size="xs" variant="subtle" onClick={() => setRevoke(token)}>Revoke</Button>}</Table.Td></Table.Tr>)}</Table.Tbody></Table></ScrollArea>
      )}
      {nextTokens && <Button variant="default" onClick={() => void loadMoreTokens()}>Load more tokens</Button>}
      {creating && <TokenDialog account={target} capabilities={availableCapabilities} onClose={() => setCreating(false)} onSessionLost={onSessionLost} onSave={(input) => api.createToken(input)} onIssued={(response) => { setIssued(response); void loadTokens(accountId); }} />}
      <Modal opened={Boolean(issued)} onClose={() => setIssued(null)} title="Save this bearer token now" centered size="lg" closeOnClickOutside={false}><Stack><Alert color="yellow" role="alert">This is the only time the complete bearer token will be shown. Store it in the client’s secret manager. Do not put it in a URL, browser storage or logs.</Alert>{issued?.secretUnavailable ? <Alert color="red">The token was created, but its one-time secret is no longer available. Revoke it and create another token.</Alert> : <><Textarea aria-label="One-time bearer token" autosize readOnly value={bearer} /><Button variant="default" onClick={() => void copyBearer()}>{copied ? 'Copied' : 'Copy token'}</Button></>}<Button onClick={() => { setIssued(null); setCopied(false); }}>I saved the secret</Button></Stack></Modal>
      <Modal opened={Boolean(revoke)} onClose={() => setRevoke(null)} title="Revoke token" centered><Stack><Text>Revoke <strong>{revoke?.name}</strong>? Clients using it will be denied immediately.</Text><Group justify="flex-end"><Button variant="default" onClick={() => setRevoke(null)}>Cancel</Button><Button color="red" onClick={() => void revokeToken()}>Revoke token</Button></Group></Stack></Modal>
    </Stack>
  );
}
