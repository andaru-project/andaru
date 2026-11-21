import { Alert, Anchor, Badge, Group, Loader, Paper, Stack, Text, Title } from '@mantine/core';
import { useCallback, type ReactNode } from 'react';
import { Link } from 'react-router';
import type { ApiClient } from '../api';
import { clock, pendingNotice, useCountdown, type ChangeState } from '../changes';
import { stateColor, type Session } from '../types';
import { useResource, useVisiblePoll, type Resource } from '../useResource';

interface DashboardScreenProps {
  api: ApiClient;
  session: Session;
  changes: Resource<ChangeState | undefined>;
  onSessionLost(): void;
}

interface WidgetProps { api: ApiClient; onSessionLost(): void }

const pollMilliseconds = 10_000;

function Widget({ id, title, to, children }: { id: string; title: string; to: string; children: ReactNode }) {
  return (
    <Paper component="section" withBorder p="md" aria-labelledby={`widget-${id}`}>
      <Title order={3} size="h5" mb="sm" id={`widget-${id}`}><Anchor component={Link} to={to} inherit>{title}</Anchor></Title>
      {children}
    </Paper>
  );
}

// Loaded shows one widget's own loading and failure, so a widget that fails never blanks the others.
function Loaded<T>({ resource, what, children }: { resource: Resource<T>; what: string; children(data: T): ReactNode }) {
  return (
    <Stack gap="xs">
      {resource.error && <Text className="error-text" size="sm" role="alert">{resource.error}</Text>}
      {resource.data !== undefined ? children(resource.data) : !resource.error && <Loader size="sm" aria-label={`Loading ${what}`} />}
    </Stack>
  );
}

function usePolled<T>(load: () => Promise<T>, onSessionLost: () => void) {
  const resource = useResource(load, onSessionLost);
  useVisiblePoll(resource.refresh, pollMilliseconds);
  return resource;
}

// The shell polls the change state for its notice; the widget shows the same reading.
function ChangesText({ state }: { state: ChangeState }) {
  const notice = pendingNotice(state);
  const remaining = useCountdown(notice?.kind === 'awaiting' ? notice.apply.deadline : undefined);
  if (!notice) return <Text>Nothing is pending: the saved configuration is the one that runs.</Text>;
  if (notice.kind === 'unapplied') return <Text>Saved changes are not applied yet.</Text>;
  if (notice.kind === 'activating') return <Text>Applying saved version {notice.apply.savedVersion}…</Text>;
  if (notice.kind === 'failed') return <Text>The last apply failed.</Text>;
  if (notice.kind === 'rolled_back') return <Text>The last apply was rolled back.</Text>;
  return <Text>Applied, awaiting confirmation: rolls back at {notice.apply.deadline ? clock(notice.apply.deadline) : 'an unknown time'}{remaining && ` (${remaining} left)`}.</Text>;
}

function InterfacesWidget({ api, onSessionLost }: WidgetProps) {
  const resource = usePolled(useCallback(() => api.links(), [api]), onSessionLost);
  return (
    <Loaded resource={resource} what="interfaces">
      {({ items }) => {
        const links = items.filter((link) => link.linkType !== 'loopback');
        if (links.length === 0) return <Text c="dimmed">The box reports no network links.</Text>;
        return (
          <ul className="plain-list">
            {links.map((link) => (
              <li key={link.name}>
                <Group gap="xs"><Text ff="monospace" fw={600}>{link.name}</Text><Badge color={stateColor[link.operState] ?? 'gray'}>{link.operState.toLowerCase()}</Badge></Group>
                {link.addresses.filter((address) => address.scope !== 'link').map((address) => <Text key={address.address} ff="monospace" size="sm">{address.address}/{address.prefixLength}</Text>)}
              </li>
            ))}
          </ul>
        );
      }}
    </Loaded>
  );
}

function PrefixesWidget({ api, onSessionLost }: WidgetProps) {
  const resource = usePolled(useCallback(() => api.prefixes(), [api]), onSessionLost);
  return (
    <Loaded resource={resource} what="IPv6 prefixes">
      {({ wans }) => {
        if (wans.length === 0) return <Text c="dimmed">No uplink asks for a delegated prefix.</Text>;
        return (
          <ul className="plain-list">
            {wans.map((wan) => (
              <li key={wan.interface}>
                <Group gap="xs"><Text ff="monospace" fw={600}>{wan.interface}</Text>{wan.current ? <Text ff="monospace">{wan.current.prefix}</Text> : <Badge color="red">no prefix held</Badge>}</Group>
                {/* A segment's subnet ID and a pool's are separate number spaces, so the prefix is the key. */}
                {[...wan.segments, ...wan.pools].filter((place) => place.prefix).map((place) => <Text key={place.prefix} ff="monospace" size="sm">{place.prefix} on {place.interface}</Text>)}
                {wan.notFitting.length > 0 && <Text className="error-text" size="sm">No room for: {wan.notFitting.join(', ')}</Text>}
              </li>
            ))}
          </ul>
        );
      }}
    </Loaded>
  );
}

export function DashboardScreen({ api, session, changes, onSessionLost }: DashboardScreenProps) {
  const widgets = [
    { id: 'changes', title: 'Changes', to: '/changes', capability: 'configuration.read', body: <Loaded resource={changes} what="changes">{(state) => state && <ChangesText state={state} />}</Loaded> },
    { id: 'interfaces', title: 'Interfaces', to: '/network/interfaces', capability: 'status.read', body: <InterfacesWidget api={api} onSessionLost={onSessionLost} /> },
    { id: 'prefixes', title: 'IPv6 prefixes', to: '/network/interfaces', capability: 'status.read', body: <PrefixesWidget api={api} onSessionLost={onSessionLost} /> },
  ].filter((widget) => session.capabilities.includes(widget.capability));

  return (
    <Stack>
      <Title order={2}>Dashboard</Title>
      {widgets.length === 0 ? <Alert role="status">Your account can see none of the dashboard's widgets.</Alert> : (
        <div className="widget-grid">
          {widgets.map(({ id, title, to, body }) => <Widget key={id} id={id} title={title} to={to}>{body}</Widget>)}
        </div>
      )}
    </Stack>
  );
}
