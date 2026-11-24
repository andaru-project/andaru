import { Badge, Group, Paper, Stack, Text, Title } from '@mantine/core';
import { formatTimestamp, useTimeZone } from '../timezone';
import type { Session } from '../types';

export function SessionScreen({ session }: { session: Session }) {
  const timeZone = useTimeZone();
  return (
    <Stack>
      <div><Title order={2}>Current session</Title><Text c="dimmed">Permissions shown here are the live capabilities enforced by the engine.</Text></div>
      <div className="widget-grid"><Paper withBorder p="md"><Text c="dimmed" size="sm">Account</Text><Text fw={600}>{session.account.login}</Text></Paper><Paper withBorder p="md"><Text c="dimmed" size="sm">Expires</Text><Text fw={600} className="tabular">{formatTimestamp(session.expiresAt, timeZone)}</Text></Paper><Paper withBorder p="md"><Text c="dimmed" size="sm">Idle timeout</Text><Text fw={600}>{Math.round(session.idleTimeoutSeconds / 60)} minutes</Text></Paper></div>
      <Paper withBorder p="md"><Text fw={600} mb="sm">Effective live capabilities</Text><Group gap="xs">{session.capabilities.length ? session.capabilities.map((capability) => <Badge key={capability} variant="light">{capability}</Badge>) : <Text c="dimmed">No capabilities</Text>}</Group></Paper>
    </Stack>
  );
}
