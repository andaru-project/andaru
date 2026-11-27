// SPDX-License-Identifier: AGPL-3.0-or-later
import { Alert, Button, Group, Image, Paper, PasswordInput, Stack, Text, Title } from '@mantine/core';
import { useRef, useState, type FormEvent } from 'react';
import mark from '../../andaru-icon.svg';

interface SignInScreenProps {
  notice?: string;
  onSignIn(token: string): Promise<void>;
}

// Andaru's sign-in page, asking for the API token the server was started with.
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
      setError(requestError instanceof Error ? requestError.message : String(requestError));
      setToken('');
      requestAnimationFrame(() => tokenRef.current?.focus());
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="login-page">
      <Paper className="login-card" component="main" withBorder p="xl">
        <form onSubmit={submit}>
          <Stack>
            <Group gap="sm" wrap="nowrap">
              <Image src={mark} alt="" w={40} h={40} />
              <div>
                <Title order={1} size="h2">Netplane</Title>
                <Text c="dimmed">Sign in with this server's API token.</Text>
              </div>
            </Group>
            {notice && <Alert color="yellow" role="alert">{notice}</Alert>}
            {error && <Alert color="red" role="alert" title="Sign-in failed">{error}</Alert>}
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
            <Button loading={busy} type="submit">Sign in</Button>
          </Stack>
        </form>
      </Paper>
    </div>
  );
}
