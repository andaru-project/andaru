import { Badge, Group, Stack, Text, VisuallyHidden } from '@mantine/core';
import { useCallback } from 'react';
import type { ApiClient } from '../api';
import { usePolledResource } from '../useResource';
import { Loaded } from './Loaded';

const shown = 5;

// The last few events of the security audit, newest first. Reading the log is itself audited, and this widget
// reads it every 10 seconds: a successful read is left out, or the viewer's own reads would take a line. One
// account's repeated reads make one event, so polling does not flood the log; a few more are asked for than
// shown, so the lines are filled after the reads are left out.
export function RecentActivityWidget({ api, onSessionLost }: { api: ApiClient; onSessionLost(): void }) {
  const resource = usePolledResource(useCallback(() => api.listAudit('', shown * 2), [api]), onSessionLost, 10_000);
  return (
    <Loaded resource={resource} what="recent activity">
      {(page) => {
        const events = page.items.filter((event) => !(event.action === 'audit.queried' && event.result === 'success')).slice(0, shown);
        return events.length === 0 ? <Text size="sm" c="dimmed">No audit events yet.</Text> : (
          <Stack gap="xs">
            {events.map((event) => (
              <div key={event.id}>
                <Group justify="space-between" gap="xs" wrap="nowrap">
                  <Text size="sm" fw={600}>{event.action}</Text>
                  {event.result !== 'success' && <Badge size="sm" color="red" variant="light" tt="none">{event.result}</Badge>}
                </Group>
                <Text size="xs" c="dimmed">
                  {new Date(event.at).toLocaleString()} · {event.actor.kind}:{event.actor.id}
                  {event.count > 1 && <> · <span aria-hidden="true">×{event.count}</span><VisuallyHidden>repeated {event.count} times</VisuallyHidden></>}
                </Text>
              </div>
            ))}
          </Stack>
        );
      }}
    </Loaded>
  );
}
