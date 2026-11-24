import { Alert, Button, Container, Loader, Stack, Text } from '@mantine/core';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { BrowserRouter } from 'react-router';
import { ApiClient, ApiError, apiErrorMessage } from './api';
import { LoginScreen } from './screens/LoginScreen';
import { SetupBootstrapScreen } from './screens/SetupBootstrapScreen';
import { Shell } from './Shell';
import type { Session } from './types';

export function App() {
  const [connected, setConnected] = useState(true);
  const api = useMemo(() => new ApiClient('', setConnected), []);
  const [session, setSession] = useState<Session | null>(null);
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
    setNotice('Your session expired or was revoked. Sign in again to continue.');
  }, [api]);

  async function login(loginName: string, password: string) {
    const current = await api.login(loginName, password);
    setSession(current);
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
    }
  }

  if (loading) return <Container py="xl"><Loader aria-label="Connecting to Andaru" /></Container>;
  if (disconnected) return <Container size="sm" py="xl"><Alert color="red" role="alert" title="Cannot reach the Andaru service"><Stack><Text>The management service is disconnected. No change was sent.</Text><Button onClick={() => void connect()}>Retry connection</Button></Stack></Alert></Container>;
  if (!session && setupRequired) {
    return (
      <SetupBootstrapScreen
        api={api}
        onBootstrapped={(created) => {
          setSession(created);
          setSetupRequired(false);
          setNotice('');
          window.history.replaceState(null, '', '/setup');
        }}
        onAlreadyConfigured={(message) => {
          setSetupRequired(false);
          setNotice(message);
        }}
      />
    );
  }
  if (!session) return <LoginScreen notice={notice} setupRequired={false} onLogin={login} />;

  return <BrowserRouter><Shell api={api} session={session} connected={connected} onLogout={() => void logout()} onSessionLost={sessionLost} /></BrowserRouter>;
}
