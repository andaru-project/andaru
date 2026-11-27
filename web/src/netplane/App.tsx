// SPDX-License-Identifier: AGPL-3.0-or-later
import { Container, Loader } from '@mantine/core';
import { useCallback, useEffect, useState } from 'react';
import { hasAccess, hasToken, signIn, signOut } from './api';
import { SignInScreen } from './screens/SignInScreen';
import { Shell } from './Shell';

export function App() {
  // Signed in means the server answers this browser; a server without a token answers everyone.
  const [signedIn, setSignedIn] = useState<boolean | null>(null);
  // Whether the server answers only with the stored token, so signing out means something.
  const [tokenNeeded, setTokenNeeded] = useState(false);
  const [notice, setNotice] = useState('');

  const connect = useCallback(async (signedOutNotice = '') => {
    setSignedIn(null);
    try {
      const answered = await hasAccess();
      // A server that also answers without the cookie needs no token: the cookie, perhaps one at a parent path, goes.
      const stored = answered && hasToken();
      const needed = stored && !(await hasAccess('omit'));
      if (stored && !needed) signOut();
      setTokenNeeded(needed);
      setSignedIn(answered);
      if (!answered) setNotice(signedOutNotice);
    } catch {
      // An unreachable server is the shell's to show: its connection badge and fleet errors say so.
      setTokenNeeded(hasToken());
      setSignedIn(true);
    }
  }, []);

  // The fetch result is external state. The rule treats the request's setState as derived state.
  // eslint-disable-next-line react-hooks/set-state-in-effect -- fetch on mount, not derived state
  useEffect(() => { void connect(); }, [connect]);

  const sessionLost = useCallback(() => {
    const stored = hasToken();
    signOut();
    setSignedIn(false);
    setNotice(stored ? 'The server no longer accepts the stored token. Sign in again to continue.' : 'This Netplane server requires an API token.');
  }, []);

  async function login(token: string) {
    await signIn(token);
    setNotice('');
    setTokenNeeded(true);
    setSignedIn(true);
  }

  if (signedIn === null) return <Container py="xl"><Loader aria-label="Connecting to Netplane" /></Container>;
  if (!signedIn) return <SignInScreen notice={notice} onSignIn={login} />;
  // Signing out checks again: the sign-in page shows only if the server still asks for the token.
  return <Shell onSignOut={tokenNeeded ? () => { signOut(); void connect('You signed out.'); } : undefined} onSessionLost={sessionLost} />;
}
