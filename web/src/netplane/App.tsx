// SPDX-License-Identifier: AGPL-3.0-or-later
import { Container, Loader } from '@mantine/core';
import { useCallback, useEffect, useState } from 'react';
import { hasAccess, hasToken, signIn, signOut } from './api';
import { SignInScreen } from './screens/SignInScreen';
import { Shell } from './Shell';

export function App() {
  // Signed in means the server answers this browser; a server without a token answers everyone.
  const [signedIn, setSignedIn] = useState<boolean | null>(null);
  const [notice, setNotice] = useState('');

  const connect = useCallback(async (signedOutNotice = '') => {
    setSignedIn(null);
    try {
      const answered = await hasAccess();
      setSignedIn(answered);
      if (!answered) setNotice(signedOutNotice);
    } catch {
      // An unreachable server is the shell's to show: its connection badge and fleet errors say so.
      setSignedIn(true);
    }
  }, []);

  // The fetch result is external state. The rule treats the request's setState as derived state.
  // eslint-disable-next-line react-hooks/set-state-in-effect -- fetch on mount, not derived state
  useEffect(() => { void connect(); }, [connect]);

  const sessionLost = useCallback(() => {
    signOut();
    setSignedIn(false);
    setNotice('The server no longer accepts the stored token. Sign in again to continue.');
  }, []);

  async function login(token: string) {
    await signIn(token);
    setNotice('');
    setSignedIn(true);
  }

  if (signedIn === null) return <Container py="xl"><Loader aria-label="Connecting to Netplane" /></Container>;
  if (!signedIn) return <SignInScreen notice={notice} onSignIn={login} />;
  // Sign out is offered only with a token; signing out checks again, since a server without one needs no sign-in.
  return <Shell onSignOut={hasToken() ? () => { signOut(); void connect('You signed out.'); } : undefined} onSessionLost={sessionLost} />;
}
