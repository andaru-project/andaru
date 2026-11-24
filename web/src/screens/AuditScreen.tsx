import { Alert, Badge, Button, Group, Loader, ScrollArea, Stack, Table, Text, TextInput, Title } from '@mantine/core';
import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiClient, ApiError, apiErrorMessage } from '../api';
import { formatTimestamp, useTimeZone } from '../timezone';
import type { AuditEvent } from '../types';

interface AuditScreenProps { api: ApiClient; onSessionLost(): void }

export function AuditScreen({ api, onSessionLost }: AuditScreenProps) {
  const timeZone = useTimeZone();
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [nextCursor, setNextCursor] = useState('');
  const [filter, setFilter] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const requestId = useRef(0);
  useEffect(() => () => { requestId.current += 1; }, []);

  const handleError = useCallback((requestError: unknown) => {
    if (requestError instanceof ApiError && requestError.status === 401) onSessionLost();
    else setError(apiErrorMessage(requestError));
  }, [onSessionLost]);

  const load = useCallback(async () => {
    const id = ++requestId.current;
    setLoading(true);
    setError('');
    try {
      const page = await api.listAudit();
      if (id !== requestId.current) return;
      setEvents(page.items);
      setNextCursor(page.nextCursor ?? '');
    } catch (requestError) {
      if (id !== requestId.current) return;
      handleError(requestError);
    } finally {
      if (id === requestId.current) setLoading(false);
    }
  }, [api, handleError]);

  // eslint-disable-next-line react-hooks/set-state-in-effect -- fetch on mount, not derived state
  useEffect(() => { void load(); }, [load]);

  async function loadMore() {
    const id = ++requestId.current;
    setLoading(true);
    try {
      const page = await api.listAudit(nextCursor);
      if (id !== requestId.current) return;
      setEvents((current) => [...current, ...page.items]);
      setNextCursor(page.nextCursor ?? '');
    } catch (requestError) {
      if (id !== requestId.current) return;
      handleError(requestError);
    } finally {
      if (id === requestId.current) setLoading(false);
    }
  }

  const words = filter.toLowerCase().split(/\s+/).filter(Boolean);
  const matches = (event: AuditEvent) => {
    if (words.length === 0) return true;
    const text = [
      event.action,
      `${event.actor.kind}:${event.actor.id}`,
      event.target ?? '',
      event.result,
      event.source || 'local',
      ...Object.entries(event.metadata).map(([key, value]) => `${key}: ${value}`),
    ].join(' ').toLowerCase();
    return words.every((word) => text.includes(word));
  };
  const shown = events.filter(matches);

  return (
    <Stack>
      <Group justify="space-between" align="flex-end">
        <div><Title order={2}>Security audit</Title><Text c="dimmed">Security-relevant actions and outcomes retained by the engine, newest first.</Text></div>
        <Button size="xs" variant="default" loading={loading && events.length > 0} onClick={() => void load()}>Refresh</Button>
      </Group>
      {error && <Alert color="red" role="alert" title="Audit request failed">{error}</Alert>}
      {events.length > 0 && <TextInput label="Filter events" placeholder="An action, actor, target or result" value={filter} onChange={(event) => setFilter(event.currentTarget.value)} />}
      {loading && events.length === 0 ? <Loader aria-label="Loading audit events" /> : events.length === 0 ? <Alert role="status">No audit events are visible.</Alert> : shown.length === 0 ? <Text c="dimmed" size="sm">No loaded event matches the filter.</Text> : (
        <ScrollArea><Table className="data-table" striped highlightOnHover><Table.Thead><Table.Tr><Table.Th>Time</Table.Th><Table.Th>Action</Table.Th><Table.Th>Actor</Table.Th><Table.Th>Target</Table.Th><Table.Th>Result</Table.Th><Table.Th>Source</Table.Th><Table.Th>Details</Table.Th></Table.Tr></Table.Thead><Table.Tbody>{shown.map((event) => <Table.Tr key={event.id}><Table.Td data-label="Time" className="tabular">{formatTimestamp(event.at, timeZone)}</Table.Td><Table.Td data-label="Action"><Text fw={600}>{event.action}</Text>{event.count > 1 && <Text size="xs">Repeated {event.count} times, last at the time shown</Text>}</Table.Td><Table.Td data-label="Actor">{event.actor.kind}:{event.actor.id}</Table.Td><Table.Td data-label="Target">{event.target || '—'}</Table.Td><Table.Td data-label="Result"><Badge size="sm" className="plain-badge" color={event.result === 'success' ? 'green' : 'red'} variant="light">{event.result}</Badge></Table.Td><Table.Td data-label="Source">{event.source || 'local'}</Table.Td><Table.Td data-label="Details">{Object.entries(event.metadata).map(([key, value]) => `${key}: ${value}`).join(', ') || '—'}</Table.Td></Table.Tr>)}</Table.Tbody></Table></ScrollArea>
      )}
      {nextCursor && <Button loading={loading} variant="default" onClick={() => void loadMore()}>Load older events</Button>}
    </Stack>
  );
}
