import { Alert, Button, Code, Group, Image, Paper, PasswordInput, Stack, Text, TextInput, Title } from '@mantine/core';
import { useRef, useState, type FormEvent } from 'react';
import mark from '../andaru-icon.svg';
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
      <div className="login-page">
        <Paper className="login-card" component="main" withBorder p="xl">
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
      </div>
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
    <div className="login-page">
      <Paper className="login-card" component="main" withBorder p="xl">
        <form onSubmit={submit}>
          <Stack>
            <Group gap="sm" wrap="nowrap">
              <Image src={mark} alt="" w={40} h={40} />
              <div>
                <Title order={1} size="h2">Andaru</Title>
                <Text c="dimmed">Sign in with an Andaru account.</Text>
              </div>
            </Group>
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
    </div>
  );
}
