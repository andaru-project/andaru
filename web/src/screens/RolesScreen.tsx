import { Alert, Badge, Button, Group, Loader, Modal, ScrollArea, Stack, Table, Text, Title } from '@mantine/core';
import { useCallback, useEffect, useState } from 'react';
import { ApiClient, ApiError, apiErrorMessage } from '../api';
import type { Applies, Role, SavedObject, Session } from '../types';
import { RoleDialog } from './RoleDialog';

interface RolesScreenProps { api: ApiClient; session: Session; onSessionLost(): void }

function equivalent(saved: SavedObject<Role>[], live: Role[]) {
  const canonical = (roles: Role[]) => JSON.stringify(roles.map(({ id, name, description = '', capabilities }) => ({ id, name, description, capabilities: [...capabilities].sort() })).sort((a, b) => a.id.localeCompare(b.id)));
  return canonical(saved.map((item) => item.value)) === canonical(live);
}

export function RolesScreen({ api, session, onSessionLost }: RolesScreenProps) {
  const canReadSaved = session.capabilities.includes('configuration.read');
  const canReadLive = session.capabilities.includes('users.read');
  const canWrite = session.capabilities.includes('configuration.write');
  const canApply = session.capabilities.includes('configuration.apply');
  const [saved, setSaved] = useState<SavedObject<Role>[]>([]);
  const [savedVersion, setSavedVersion] = useState('');
  const [live, setLive] = useState<Role[]>([]);
  const [capabilities, setCapabilities] = useState<string[]>([]);
  const [applies, setApplies] = useState<Applies | null>(null);
  const [editing, setEditing] = useState<SavedObject<Role> | 'new' | null>(null);
  const [deleting, setDeleting] = useState<SavedObject<Role> | null>(null);
  const [applyOpen, setApplyOpen] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const handleError = useCallback((requestError: unknown) => {
    if (requestError instanceof ApiError && requestError.status === 401) onSessionLost();
    else setError(apiErrorMessage(requestError));
  }, [onSessionLost]);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [savedPage, livePage, capabilityPage, applyPage] = await Promise.all([
        canReadSaved ? api.savedRoles() : Promise.resolve(null),
        canReadLive ? api.allLiveRoles() : Promise.resolve([] as Role[]),
        canReadLive ? api.capabilities() : Promise.resolve({ items: session.capabilities }),
        canReadSaved ? api.listApplies() : Promise.resolve(null),
      ]);
      setSaved(savedPage?.items ?? []);
      setSavedVersion(savedPage?.savedVersion ?? '');
      setLive(livePage);
      setCapabilities(capabilityPage.items);
      setApplies(applyPage);
    } catch (requestError) {
      handleError(requestError);
    } finally {
      setLoading(false);
    }
  }, [api, canReadLive, canReadSaved, handleError, session.capabilities]);

  // eslint-disable-next-line react-hooks/set-state-in-effect -- fetch on mount, not derived state
  useEffect(() => { void load(); }, [load]);

  async function saveRole(input: Omit<Role, 'id' | 'builtIn'>) {
    if (editing === 'new') await api.createRole(input);
    else if (editing) await api.updateRole({ ...input, id: editing.value.id }, editing.version);
    await load();
  }

  async function deleteRole() {
    if (!deleting) return;
    try {
      await api.deleteRole(deleting.value.id, deleting.version);
      setDeleting(null);
      await load();
    } catch (requestError) {
      handleError(requestError);
    }
  }

  async function startApply() {
    setApplyOpen(false);
    try {
      await api.apply(savedVersion);
      await load();
    } catch (requestError) {
      handleError(requestError);
    }
  }

  async function finishApply(action: 'confirmation' | 'rollback') {
    if (!applies?.pointers.pending) return;
    try {
      await api.finishApply(applies.pointers.pending, action);
      await load();
    } catch (requestError) {
      handleError(requestError);
    }
  }

  const active = equivalent(saved, live);
  const pending = applies?.pointers.pending;
  return (
    <Stack>
      <Group justify="space-between" align="flex-end"><div><Title order={2}>Roles</Title><Text c="dimmed">Versioned capability grants for Andaru accounts.</Text></div>{canWrite && <Button onClick={() => setEditing('new')}>Create role</Button>}</Group>
      <Alert color="blue" title="Saved roles are not live permissions">
        Saving a role never activates it. The effective live column shows permissions enforced now. Applying activates the entire saved configuration, not only roles.
      </Alert>
      {error && <Alert color="red" role="alert" title="Role request failed">{error}</Alert>}
      {canReadSaved && canReadLive && <Group><Badge color={active ? 'green' : 'yellow'}>{active ? 'Saved and live roles match' : 'Saved role changes are not active'}</Badge><Text size="sm">Saved version {savedVersion || 'unavailable'}</Text></Group>}
      {pending && <Alert color="yellow" title={`Apply ${pending} awaits confirmation`}><Stack gap="xs"><Text size="sm">A newly authenticated human administrator may be required after a role authorization change.</Text><Group><Button size="xs" onClick={() => void finishApply('confirmation')}>Confirm live configuration</Button><Button color="red" size="xs" variant="default" onClick={() => void finishApply('rollback')}>Roll back</Button></Group></Stack></Alert>}
      {loading ? <Loader aria-label="Loading roles" /> : (
        <ScrollArea><Table striped><Table.Thead><Table.Tr><Table.Th>Role</Table.Th><Table.Th>Saved draft</Table.Th><Table.Th>Effective live</Table.Th>{canWrite && <Table.Th>Actions</Table.Th>}</Table.Tr></Table.Thead><Table.Tbody>{(canReadSaved ? saved : live.map((role) => ({ value: role, version: '' }))).map((item) => {
          const liveRole = live.find((role) => role.id === item.value.id);
          return <Table.Tr key={item.value.id}><Table.Td><Text fw={600}>{item.value.name}</Text>{item.value.description && <Text c="dimmed" size="sm">{item.value.description}</Text>}{item.value.builtIn && <Badge mt="xs" variant="light">Built in</Badge>}</Table.Td><Table.Td><Text fw={600}>Saved draft</Text><Text size="sm">{item.value.capabilities.join(', ') || 'No capabilities'}</Text></Table.Td><Table.Td><Text fw={600}>Effective live</Text><Text size="sm">{liveRole ? liveRole.capabilities.join(', ') || 'No capabilities' : 'Not active'}</Text></Table.Td>{canWrite && <Table.Td>{!item.value.builtIn && <Group gap="xs"><Button aria-label={`Edit ${item.value.name}`} size="xs" variant="default" onClick={() => setEditing(item)}>Edit</Button><Button aria-label={`Delete ${item.value.name}`} color="red" size="xs" variant="subtle" onClick={() => setDeleting(item)}>Delete</Button></Group>}</Table.Td>}</Table.Tr>;
        })}</Table.Tbody></Table></ScrollArea>
      )}
      {canApply && savedVersion && <Button disabled={Boolean(pending) || active} onClick={() => setApplyOpen(true)}>Apply saved configuration</Button>}
      {editing && <RoleDialog key={editing === 'new' ? 'new' : editing.value.id} role={editing === 'new' ? undefined : editing.value} capabilities={capabilities} onClose={() => setEditing(null)} onSessionLost={onSessionLost} onSave={saveRole} />}
      <Modal opened={Boolean(deleting)} onClose={() => setDeleting(null)} title="Delete role draft" centered><Stack><Text>Delete the saved role <strong>{deleting?.value.name}</strong>? Live permissions do not change until the saved configuration is applied.</Text><Group justify="flex-end"><Button variant="default" onClick={() => setDeleting(null)}>Cancel</Button><Button color="red" onClick={() => void deleteRole()}>Delete role draft</Button></Group></Stack></Modal>
      <Modal opened={applyOpen} onClose={() => setApplyOpen(false)} title="Apply saved configuration" centered><Stack><Alert color="yellow">This activates every saved configuration change, including network changes. It can interrupt network connectivity and must be confirmed before the rollback deadline.</Alert><Group justify="flex-end"><Button variant="default" onClick={() => setApplyOpen(false)}>Cancel</Button><Button color="orange" onClick={() => void startApply()}>Apply and start confirmation timer</Button></Group></Stack></Modal>
    </Stack>
  );
}
