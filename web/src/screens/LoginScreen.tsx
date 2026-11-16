import { Alert, Button, Code, Container, Paper, PasswordInput, Stack, Text, TextInput, Title } from '@mantine/core';
import { useRef, useState, type FormEvent } from 'react';
import { apiErrorMessage } from '../api';
import type { Session } from '../types';

interface LoginScreenProps {
  setupRequired: boolean;
  notice?: string;
  onLogin(login: string, password: string): Promise<Session>;
}

export function LoginScreen({ setupRequired, notice, onLogin }: LoginScreenProps) {
  const [login, setLogin] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const passwordRef = useRef<HTMLInputElement>(null);

  if (setupRequired) {
    return (
      <Container size="xs" py="xl">
        <Paper withBorder p="xl">
          <Stack>
            <Title order={1}>Setup required</Title>
            <Alert color="yellow" role="status" title="Local root console required">
              Initial administrator setup is intentionally unavailable in the web interface.
            </Alert>
            <Text>On the appliance console, run:</Text>
            <Code block>sudo andaru ctl users bootstrap-admin</Code>
            <Text c="dimmed" size="sm">Return here after the command completes.</Text>
          </Stack>
        </Paper>
      </Container>
    );
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError('');
    try {
      await onLogin(login, password);
    } catch (requestError) {
      setError(apiErrorMessage(requestError));
      setPassword('');
      requestAnimationFrame(() => passwordRef.current?.focus());
    } finally {
      setBusy(false);
    }
  }

  return (
    <Container size="xs" py="xl">
      <Paper component="main" withBorder p="xl">
        <form onSubmit={submit}>
          <Stack>
            <div>
              <Title order={1}>Andaru administration</Title>
              <Text c="dimmed">Sign in with an Andaru account.</Text>
            </div>
            {notice && <Alert color="yellow" role="alert">{notice}</Alert>}
            {error && <Alert color="red" role="alert" title="Sign-in failed">{error}</Alert>}
            <TextInput
              autoComplete="username"
              autoFocus
              label="Login"
              maxLength={64}
              required
              value={login}
              onChange={(event) => setLogin(event.currentTarget.value)}
            />
            <PasswordInput
              ref={passwordRef}
              autoComplete="current-password"
              error={error || undefined}
              label="Password"
              maxLength={512}
              required
              value={password}
              onChange={(event) => setPassword(event.currentTarget.value)}
            />
            <Button loading={busy} type="submit">Sign in</Button>
          </Stack>
        </form>
      </Paper>
    </Container>
  );
}
