import { Alert, Badge, Button, Group, Loader, Modal, ScrollArea, Stack, Table, Text, Title } from '@mantine/core';
import { useCallback, useEffect, useState } from 'react';
import { ApiClient, ApiError, apiErrorMessage } from '../api';
import type { Role, Session, User } from '../types';
import { PasswordDialog } from './PasswordDialog';
import { UserDialog } from './UserDialog';

interface UsersScreenProps {
  api: ApiClient;
  session: Session;
  onSessionLost(): void;
}

export function UsersScreen({ api, session, onSessionLost }: UsersScreenProps) {
  const canManage = session.capabilities.includes('users.manage');
  const [users, setUsers] = useState<User[]>([]);
  const [roles, setRoles] = useState<Role[]>([]);
  const [nextCursor, setNextCursor] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [editing, setEditing] = useState<User | 'new' | null>(null);
  const [passwordUser, setPasswordUser] = useState<User | null>(null);
  const [deleting, setDeleting] = useState<User | null>(null);

  const handleError = useCallback((requestError: unknown) => {
    if (requestError instanceof ApiError && requestError.status === 401) onSessionLost();
    else setError(apiErrorMessage(requestError));
  }, [onSessionLost]);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [userPage, liveRoles] = await Promise.all([api.listUsers(), api.allLiveRoles()]);
      setUsers(userPage.items);
      setNextCursor(userPage.nextCursor ?? '');
      setRoles(liveRoles);
    } catch (requestError) {
      handleError(requestError);
    } finally {
      setLoading(false);
    }
  }, [api, handleError]);

  useEffect(() => { void load(); }, [load]);

  async function loadMore() {
    try {
      const page = await api.listUsers(nextCursor);
      setUsers((current) => [...current, ...page.items]);
      setNextCursor(page.nextCursor ?? '');
    } catch (requestError) {
      handleError(requestError);
    }
  }

  async function saveUser(input: { login: string; displayName: string; kind: User['kind']; enabled: boolean; roleIds: string[]; expectedRevision: number }) {
    if (editing === 'new') await api.createUser(input);
    else if (editing) await api.updateUser(editing.id, input);
    await load();
  }

  async function deleteUser() {
    if (!deleting) return;
    setError('');
    try {
      await api.deleteUser(deleting.id, deleting.revision);
      setDeleting(null);
      await load();
    } catch (requestError) {
      handleError(requestError);
    }
  }

  const revision = users[0]?.revision ?? 0;
  return (
    <Stack>
      <Group justify="space-between" align="flex-end">
        <div><Title order={2}>Users</Title><Text c="dimmed">Human accounts sign in here; service accounts use API tokens only.</Text></div>
        {canManage && <Button onClick={() => setEditing('new')}>Create user</Button>}
      </Group>
      {error && <Alert color="red" role="alert" title="User request failed">{error}</Alert>}
      {loading ? <Loader aria-label="Loading users" /> : users.length === 0 ? <Alert role="status">No users are visible.</Alert> : (
        <ScrollArea>
          <Table striped highlightOnHover>
            <Table.Thead><Table.Tr><Table.Th>Account</Table.Th><Table.Th>Kind</Table.Th><Table.Th>Status</Table.Th><Table.Th>Roles</Table.Th>{canManage && <Table.Th>Actions</Table.Th>}</Table.Tr></Table.Thead>
            <Table.Tbody>{users.map((user) => (
              <Table.Tr key={user.id}>
                <Table.Td><Text fw={600}>{user.displayName || user.login}</Text><Text c="dimmed" size="sm">{user.login}</Text></Table.Td>
                <Table.Td>{user.kind === 'service' ? 'Service' : 'Human'}</Table.Td>
                <Table.Td><Badge color={user.enabled ? 'green' : 'gray'}>{user.enabled ? 'Enabled' : 'Disabled'}</Badge></Table.Td>
                <Table.Td>{user.roleIds.map((id) => roles.find((role) => role.id === id)?.name ?? `${id} (missing)`).join(', ') || 'None'}</Table.Td>
                {canManage && <Table.Td><Group gap="xs" wrap="nowrap"><Button aria-label={`Edit ${user.login}`} size="xs" variant="default" onClick={() => setEditing(user)}>Edit</Button>{user.kind === 'human' && <Button aria-label={`Reset password for ${user.login}`} size="xs" variant="default" onClick={() => setPasswordUser(user)}>Password</Button>}<Button aria-label={`Delete ${user.login}`} color="red" size="xs" variant="subtle" onClick={() => setDeleting(user)}>Delete</Button></Group></Table.Td>}
              </Table.Tr>
            ))}</Table.Tbody>
          </Table>
        </ScrollArea>
      )}
      {nextCursor && <Button variant="default" onClick={() => void loadMore()}>Load more users</Button>}
      {editing && <UserDialog key={editing === 'new' ? 'new' : editing.id} user={editing === 'new' ? undefined : editing} revision={revision} roles={roles} onClose={() => setEditing(null)} onSessionLost={onSessionLost} onSave={saveUser} />}
      {passwordUser && <PasswordDialog user={passwordUser} onClose={() => setPasswordUser(null)} onSessionLost={onSessionLost} onSave={async (currentPassword, newPassword) => { await api.changePassword(passwordUser.id, { currentPassword, newPassword, expectedRevision: passwordUser.revision }); await load(); }} />}
      <Modal opened={Boolean(deleting)} onClose={() => setDeleting(null)} title="Delete user" centered>
        <Stack><Text>Delete <strong>{deleting?.login}</strong>? The account’s sessions and tokens will stop working. The server will refuse removal of the final recovery administrator.</Text><Group justify="flex-end"><Button variant="default" onClick={() => setDeleting(null)}>Cancel</Button><Button color="red" onClick={() => void deleteUser()}>Delete user</Button></Group></Stack>
      </Modal>
    </Stack>
  );
}
