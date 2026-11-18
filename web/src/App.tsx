import { Alert, Badge, Button, Container, Group, Loader, Paper, SimpleGrid, Stack, Text, Title } from '@mantine/core';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { ApiClient, ApiError, apiErrorMessage } from './api';
import { AuditScreen } from './screens/AuditScreen';
import { LoginScreen } from './screens/LoginScreen';
import { RolesScreen } from './screens/RolesScreen';
import { TokensScreen } from './screens/TokensScreen';
import { UsersScreen } from './screens/UsersScreen';
import type { Session } from './types';

type Page = 'session' | 'users' | 'roles' | 'tokens' | 'audit';

export function App() {
  const api = useMemo(() => new ApiClient(), []);
  const [session, setSession] = useState<Session | null>(null);
  const [page, setPage] = useState<Page>('session');
  const [setupRequired, setSetupRequired] = useState(false);
  const [notice, setNotice] = useState('');
  const [loading, setLoading] = useState(true);
  const [disconnected, setDisconnected] = useState(false);

  const connect = useCallback(async () => {
    setLoading(true);
    setDisconnected(false);
    try {
      setSession(await api.currentSession());
      setNotice('');
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        try {
          const startup = await api.startupStatus();
          setSetupRequired(startup.setupRequired);
          setSession(null);
        } catch (startupError) {
          setDisconnected(startupError instanceof ApiError && startupError.status === 0);
          if (!(startupError instanceof ApiError && startupError.status === 0)) setNotice(apiErrorMessage(startupError));
        }
      } else if (error instanceof ApiError && error.status === 0) {
        setDisconnected(true);
      } else {
        setNotice(apiErrorMessage(error));
      }
    } finally {
      setLoading(false);
    }
  }, [api]);

  // The fetch result is external state. The rule treats the request's setState as derived state.
  // eslint-disable-next-line react-hooks/set-state-in-effect -- fetch on mount, not derived state
  useEffect(() => { void connect(); }, [connect]);

  const sessionLost = useCallback(() => {
    api.clearSession();
    setSession(null);
    setSetupRequired(false);
    setPage('session');
    setNotice('Your session expired or was revoked. Sign in again to continue.');
  }, [api]);

  async function login(loginName: string, password: string) {
    const current = await api.login(loginName, password);
    setSession(current);
    setPage('session');
    setNotice('');
    return current;
  }

  async function logout() {
    try {
      await api.logout();
      setNotice('You signed out.');
    } catch (error) {
      setNotice(apiErrorMessage(error));
    } finally {
      setSession(null);
      setPage('session');
    }
  }

  if (loading) return <Container py="xl"><Loader aria-label="Connecting to Andaru" /></Container>;
  if (disconnected) return <Container size="sm" py="xl"><Alert color="red" role="alert" title="Cannot reach the Andaru service"><Stack><Text>The management service is disconnected. No change was sent.</Text><Button onClick={() => void connect()}>Retry connection</Button></Stack></Alert></Container>;
  if (!session) return <LoginScreen notice={notice} setupRequired={setupRequired} onLogin={login} />;

  const capabilities = new Set(session.capabilities);
  const navigation: Array<{ page: Page; label: string; visible: boolean }> = [
    { page: 'session', label: 'Session', visible: true },
    { page: 'users', label: 'Users', visible: capabilities.has('users.read') },
    { page: 'roles', label: 'Roles', visible: capabilities.has('users.read') || capabilities.has('configuration.read') },
    { page: 'tokens', label: 'Tokens', visible: capabilities.has('tokens.manageSelf') || capabilities.has('tokens.manageAll') },
    { page: 'audit', label: 'Audit', visible: capabilities.has('audit.read') },
  ];

  return (
    <>
      <header className="app-header"><Container size="xl"><Group justify="space-between"><div><Title order={1} size="h2">Andaru</Title><Text c="dimmed" size="sm">Identity administration</Text></div><Group><Text size="sm">{session.account.displayName || session.account.login}</Text><Button variant="default" onClick={() => void logout()}>Sign out</Button></Group></Group></Container></header>
      <Container component="main" size="xl" py="lg">
        <Stack>
          <Paper component="nav" aria-label="Administration sections" withBorder p="xs"><Group gap="xs">{navigation.filter((item) => item.visible).map((item) => <Button key={item.page} aria-current={page === item.page ? 'page' : undefined} variant={page === item.page ? 'filled' : 'subtle'} onClick={() => setPage(item.page)}>{item.label}</Button>)}</Group></Paper>
          {notice && <Alert color="yellow" role="status">{notice}</Alert>}
          {page === 'session' && <Stack><div><Title order={2}>Current session</Title><Text c="dimmed">Permissions shown here are the live capabilities enforced by the engine.</Text></div><SimpleGrid cols={{ base: 1, sm: 3 }}><Paper withBorder p="md"><Text c="dimmed" size="sm">Account</Text><Text fw={600}>{session.account.login}</Text></Paper><Paper withBorder p="md"><Text c="dimmed" size="sm">Expires</Text><Text fw={600}>{new Date(session.expiresAt).toLocaleString()}</Text></Paper><Paper withBorder p="md"><Text c="dimmed" size="sm">Idle timeout</Text><Text fw={600}>{Math.round(session.idleTimeoutSeconds / 60)} minutes</Text></Paper></SimpleGrid><Paper withBorder p="md"><Text fw={600} mb="sm">Effective live capabilities</Text><Group gap="xs">{session.capabilities.length ? session.capabilities.map((capability) => <Badge key={capability} variant="light">{capability}</Badge>) : <Text c="dimmed">No capabilities</Text>}</Group></Paper></Stack>}
          {page === 'users' && <UsersScreen api={api} session={session} onSessionLost={sessionLost} />}
          {page === 'roles' && <RolesScreen api={api} session={session} onSessionLost={sessionLost} />}
          {page === 'tokens' && <TokensScreen api={api} session={session} onSessionLost={sessionLost} />}
          {page === 'audit' && <AuditScreen api={api} onSessionLost={sessionLost} />}
        </Stack>
      </Container>
    </>
  );
}
