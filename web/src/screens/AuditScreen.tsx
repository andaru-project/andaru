import { Alert, Button, Loader, ScrollArea, Stack, Table, Text, Title } from '@mantine/core';
import { useCallback, useEffect, useState } from 'react';
import { ApiClient, ApiError, apiErrorMessage } from '../api';
import type { AuditEvent } from '../types';

interface AuditScreenProps { api: ApiClient; onSessionLost(): void }

export function AuditScreen({ api, onSessionLost }: AuditScreenProps) {
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [nextCursor, setNextCursor] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const handleError = useCallback((requestError: unknown) => {
    if (requestError instanceof ApiError && requestError.status === 401) onSessionLost();
    else setError(apiErrorMessage(requestError));
  }, [onSessionLost]);

  useEffect(() => {
    async function load() {
      try {
        const page = await api.listAudit();
        setEvents(page.items);
        setNextCursor(page.nextCursor ?? '');
      } catch (requestError) {
        handleError(requestError);
      } finally {
        setLoading(false);
      }
    }
    void load();
  }, [api, handleError]);

  async function loadMore() {
    setLoading(true);
    try {
      const page = await api.listAudit(nextCursor);
      setEvents((current) => [...current, ...page.items]);
      setNextCursor(page.nextCursor ?? '');
    } catch (requestError) {
      handleError(requestError);
    } finally {
      setLoading(false);
    }
  }

  return (
    <Stack>
      <div><Title order={2}>Security audit</Title><Text c="dimmed">Security-relevant actions and outcomes retained by the engine.</Text></div>
      {error && <Alert color="red" role="alert" title="Audit request failed">{error}</Alert>}
      {loading && events.length === 0 ? <Loader aria-label="Loading audit events" /> : events.length === 0 ? <Alert role="status">No audit events are visible.</Alert> : (
        <ScrollArea><Table className="data-table" striped highlightOnHover><Table.Thead><Table.Tr><Table.Th>Time</Table.Th><Table.Th>Action</Table.Th><Table.Th>Actor</Table.Th><Table.Th>Target</Table.Th><Table.Th>Result</Table.Th><Table.Th>Source</Table.Th><Table.Th>Details</Table.Th></Table.Tr></Table.Thead><Table.Tbody>{events.map((event) => <Table.Tr key={event.id}><Table.Td data-label="Time">{new Date(event.at).toLocaleString()}</Table.Td><Table.Td data-label="Action"><Text fw={600}>{event.action}</Text>{event.count > 1 && <Text size="xs">Repeated {event.count} times</Text>}</Table.Td><Table.Td data-label="Actor">{event.actor.kind}:{event.actor.id}</Table.Td><Table.Td data-label="Target">{event.target || '—'}</Table.Td><Table.Td data-label="Result">{event.result}</Table.Td><Table.Td data-label="Source">{event.source || 'local'}</Table.Td><Table.Td data-label="Details">{Object.entries(event.metadata).map(([key, value]) => `${key}: ${value}`).join(', ') || '—'}</Table.Td></Table.Tr>)}</Table.Tbody></Table></ScrollArea>
      )}
      {nextCursor && <Button loading={loading} variant="default" onClick={() => void loadMore()}>Load more events</Button>}
    </Stack>
  );
}
