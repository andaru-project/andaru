import { Badge, Group, Stack, Text } from '@mantine/core';
import { useCallback } from 'react';
import type { ApiClient } from '../api';
import { usePolledResource } from '../useResource';
import { Loaded } from './Loaded';

// The last few events of the security audit, newest first. Reading the log is itself audited; one account's
// repeated reads make one event, so polling it does not flood it.
export function RecentActivityWidget({ api, onSessionLost }: { api: ApiClient; onSessionLost(): void }) {
  const resource = usePolledResource(useCallback(() => api.listAudit('', 5), [api]), onSessionLost, 10_000);
  return (
    <Loaded resource={resource} what="recent activity">
      {(page) => page.items.length === 0 ? <Text size="sm" c="dimmed">No audit events yet.</Text> : (
        <Stack gap="xs">
          {page.items.map((event) => (
            <div key={event.id}>
              <Group justify="space-between" gap="xs" wrap="nowrap">
                <Text size="sm" fw={600}>{event.action}</Text>
                {event.result !== 'success' && <Badge size="sm" color="red" variant="light" tt="none">{event.result}</Badge>}
              </Group>
              <Text size="xs" c="dimmed">
                {new Date(event.at).toLocaleString()} · {event.actor.kind}:{event.actor.id}{event.count > 1 ? ` · ×${event.count}` : ''}
              </Text>
            </div>
          ))}
        </Stack>
      )}
    </Loaded>
  );
}
