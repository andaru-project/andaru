// SPDX-License-Identifier: AGPL-3.0-or-later
import { PasswordInput } from '@mantine/core';
import { useRef, useState, type FormEvent } from 'react';
import { apiErrorMessage } from '../../api';
import { SignInCard } from '../../screens/SignInCard';

interface SignInScreenProps {
  notice?: string;
  onSignIn(token: string): Promise<void>;
}

// Asks for the API token the standalone server was started with.
export function SignInScreen({ notice, onSignIn }: SignInScreenProps) {
  const [token, setToken] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const tokenRef = useRef<HTMLInputElement>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError('');
    try {
      await onSignIn(token);
    } catch (requestError) {
      setError(apiErrorMessage(requestError));
      setToken('');
      requestAnimationFrame(() => tokenRef.current?.focus());
    } finally {
      setBusy(false);
    }
  }

  return (
    <SignInCard title="Netplane" description="Sign in with this server's API token." notice={notice} error={error} busy={busy} onSubmit={submit}>
      <PasswordInput
        ref={tokenRef}
        autoComplete="current-password"
        autoFocus
        error={error || undefined}
        label="API token"
        required
        value={token}
        onChange={(event) => setToken(event.currentTarget.value)}
      />
    </SignInCard>
  );
}
