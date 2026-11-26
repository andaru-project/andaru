import { Alert, Badge, Button, Group, Modal, Paper, ScrollArea, Stack, Table, Text, Title } from '@mantine/core';
import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiClient, ApiError, reportError } from '../api';
import { formatTimestamp, useTimeZone } from '../timezone';
import type { Session, WebSessionItem } from '../types';
import { useResource } from '../useResource';
import { Loaded } from './Loaded';

interface SessionScreenProps {
  api: ApiClient;
  session: Session;
  onSessionLost(): void;
}

function shortSessionId(id: string): string {
  return id.length > 8 ? `…${id.slice(-8)}` : id;
}

export function SessionScreen({ api, session, onSessionLost }: SessionScreenProps) {
  const timeZone = useTimeZone();
  const load = useCallback(async () => (await api.listSessions()).items, [api]);
  const resource = useResource(load, onSessionLost);
  const [revokingId, setRevokingId] = useState<string | null>(null);
  const [confirmCurrent, setConfirmCurrent] = useState<WebSessionItem | null>(null);
  const [error, setError] = useState('');
  const revokingRef = useRef(false);
  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);

  async function revoke(item: WebSessionItem) {
    if (revokingRef.current) return;
    revokingRef.current = true;
    setRevokingId(item.id);
    setError('');
    try {
      try {
        await api.revokeSession(item.id);
      } catch (requestError) {
        // 404 means the session is already gone; treat it the same as a successful revocation.
        if (!(requestError instanceof ApiError && requestError.status === 404)) throw requestError;
      }
      if (item.current) {
        if (mountedRef.current) setConfirmCurrent(null);
        onSessionLost();
        return;
      }
      if (!mountedRef.current) return;
      setConfirmCurrent(null);
      await resource.reload();
    } catch (requestError) {
      if (requestError instanceof ApiError && requestError.status === 401) {
        onSessionLost();
        return;
      }
      if (!mountedRef.current) return;
      // Close the confirmation modal so the background error alert is visible and focused.
      setConfirmCurrent(null);
      reportError(requestError, onSessionLost, setError);
    } finally {
      revokingRef.current = false;
      if (mountedRef.current) setRevokingId(null);
    }
  }

  return (
    <Stack>
      <div>
        <Title order={2}>Current session</Title>
        <Text c="dimmed">Permissions shown here are the live capabilities enforced by the engine.</Text>
      </div>
      <div className="widget-grid">
        <Paper withBorder p="md">
          <Text c="dimmed" size="sm">Account</Text>
          <Text fw={600}>{session.account.login}</Text>
        </Paper>
        <Paper withBorder p="md">
          <Text c="dimmed" size="sm">Expires</Text>
          <Text fw={600} className="tabular">{formatTimestamp(session.expiresAt, timeZone)}</Text>
        </Paper>
        <Paper withBorder p="md">
          <Text c="dimmed" size="sm">Idle timeout</Text>
          <Text fw={600}>{Math.round(session.idleTimeoutSeconds / 60)} minutes</Text>
        </Paper>
      </div>
      <Paper withBorder p="md">
        <Text fw={600} mb="sm">Effective live capabilities</Text>
        <Group gap="xs">
          {session.capabilities.length
            ? session.capabilities.map((capability) => <Badge key={capability} variant="light">{capability}</Badge>)
            : <Text c="dimmed">No capabilities</Text>}
        </Group>
      </Paper>

      <Paper withBorder p="md">
        <Stack>
          <Group justify="space-between" align="flex-end" wrap="wrap">
            <div>
              <Title order={3}>Active sessions</Title>
              <Text size="sm" c="dimmed">Up to five active browser sessions for this account. Revoke any session you no longer recognize.</Text>
            </div>
            <Button size="xs" variant="default" loading={resource.loading} onClick={() => void resource.reload()}>Refresh</Button>
          </Group>
          {error && <Alert color="red" role="alert" title="Session revoke failed">{error}</Alert>}
          <Loaded resource={resource} what="active sessions">
            {(items) => (items.length === 0 ? (
              <Alert role="status">No active sessions were reported.</Alert>
            ) : (
              <ScrollArea>
                <Table className="data-table" striped highlightOnHover verticalSpacing="xs">
                  <Table.Thead>
                    <Table.Tr>
                      <Table.Th scope="col">Session</Table.Th>
                      <Table.Th scope="col">Created</Table.Th>
                      <Table.Th scope="col">Last seen</Table.Th>
                      <Table.Th scope="col">Expires</Table.Th>
                      <Table.Th scope="col">Action</Table.Th>
                    </Table.Tr>
                  </Table.Thead>
                  <Table.Tbody>
                    {items.map((item, idx) => (
                      <Table.Tr key={item.id}>
                        <Table.Td data-label="Session">
                          <Group gap="xs" wrap="nowrap">
                            {item.current ? (
                              <Badge size="sm" color="green" variant="light">Current</Badge>
                            ) : (
                              <Text size="sm">Session {idx + 1}</Text>
                            )}
                            <Text size="xs" c="dimmed" ff="monospace">{shortSessionId(item.id)}</Text>
                          </Group>
                        </Table.Td>
                        <Table.Td data-label="Created" className="tabular">{formatTimestamp(item.createdAt, timeZone)}</Table.Td>
                        <Table.Td data-label="Last seen" className="tabular">{formatTimestamp(item.lastSeenAt, timeZone)}</Table.Td>
                        <Table.Td data-label="Expires" className="tabular">{formatTimestamp(item.expiresAt, timeZone)}</Table.Td>
                        <Table.Td data-label="Action">
                          <Button
                            size="compact-xs"
                            variant="subtle"
                            color="red"
                            loading={revokingId === item.id}
                            disabled={revokingId !== null && revokingId !== item.id}
                            aria-label={item.current ? 'Revoke current session' : `Revoke session ending in ${item.id.slice(-8)}`}
                            onClick={() => {
                              if (item.current) setConfirmCurrent(item);
                              else void revoke(item);
                            }}
                          >
                            Revoke
                          </Button>
                        </Table.Td>
                      </Table.Tr>
                    ))}
                  </Table.Tbody>
                </Table>
              </ScrollArea>
            ))}
          </Loaded>
        </Stack>
      </Paper>

      <Modal
        opened={confirmCurrent !== null}
        onClose={() => { if (!revokingRef.current) setConfirmCurrent(null); }}
        closeOnClickOutside={revokingId === null}
        closeOnEscape={revokingId === null}
        title="Revoke current session"
        centered
        lockScroll={false}
        closeButtonProps={{ 'aria-label': 'Close', disabled: revokingId !== null }}
      >
        <Stack>
          <Text size="sm">Revoking your current session signs you out of this browser immediately.</Text>
          <Group justify="flex-end" gap="xs">
            <Button variant="default" disabled={revokingId !== null} onClick={() => setConfirmCurrent(null)}>Cancel</Button>
            <Button color="red" loading={revokingId !== null} onClick={() => { if (confirmCurrent) void revoke(confirmCurrent); }}>Revoke session</Button>
          </Group>
        </Stack>
      </Modal>
    </Stack>
  );
}
