import { Alert, Button, Code, Group, Image, Paper, PasswordInput, Stack, Text, TextInput, Title } from '@mantine/core';
import { useState, type FormEvent } from 'react';
import mark from '../andaru-icon.svg';
import { ApiClient, ApiError, apiErrorMessage } from '../api';
import type { Session } from '../types';

interface SetupBootstrapScreenProps {
  api: ApiClient;
  onBootstrapped(session: Session): void;
  onAlreadyConfigured(notice: string): void;
}

export function SetupBootstrapScreen({ api, onBootstrapped, onAlreadyConfigured }: SetupBootstrapScreenProps) {
  const [setupCode, setSetupCode] = useState('');
  const [login, setLogin] = useState('admin');
  const [displayName, setDisplayName] = useState('Administrator');
  const [password, setPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError('');
    if (password !== confirmPassword) {
      setError('Passwords do not match.');
      return;
    }
    if (password.length < 8) {
      setError('Password must be at least 8 characters.');
      return;
    }
    setBusy(true);
    try {
      const created = await api.bootstrapSetup({
        setupCode: setupCode.trim(),
        login: login.trim(),
        displayName: displayName.trim() || login.trim(),
        password,
      });
      setPassword('');
      setConfirmPassword('');
      onBootstrapped(created);
    } catch (requestError) {
      if (requestError instanceof ApiError && (requestError.status === 409 || requestError.code === 'AUTHENTICATION_UNAVAILABLE')) {
        onAlreadyConfigured(apiErrorMessage(requestError));
        return;
      }
      setError(apiErrorMessage(requestError));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="login-page">
      <Paper className="setup-card" component="main" withBorder p="xl">
        <form onSubmit={submit}>
          <Stack gap="md">
            <Group gap="sm" wrap="nowrap">
              <Image src={mark} alt="" w={40} h={40} />
              <div>
                <Title order={1} size="h2">Andaru setup assistant</Title>
                <Text c="dimmed" size="sm">Create the first administrator account, then configure management and WAN networking.</Text>
              </div>
            </Group>

            <Alert color="blue" role="status" title="One-time setup code required">
              <Stack gap="xs">
                <Text size="sm">On the local appliance console (screen or serial), run this command to display the one-time setup code:</Text>
                <Code block>sudo andaru ctl setup-code</Code>
              </Stack>
            </Alert>

            {error && <Alert color="red" role="alert" title="Setup failed">{error}</Alert>}

            <TextInput
              label="One-time setup code"
              description="16-character code shown on the appliance console (dashes optional)."
              placeholder="XXXX-XXXX-XXXX-XXXX"
              required
              autoFocus
              value={setupCode}
              onChange={(e) => setSetupCode(e.currentTarget.value)}
            />

            <div className="setup-grid">
              <TextInput
                label="Administrator login"
                autoComplete="username"
                required
                maxLength={64}
                value={login}
                onChange={(e) => setLogin(e.currentTarget.value)}
              />
              <TextInput
                label="Display name"
                maxLength={128}
                value={displayName}
                onChange={(e) => setDisplayName(e.currentTarget.value)}
              />
            </div>

            <div className="setup-grid">
              <PasswordInput
                label="Password"
                autoComplete="new-password"
                required
                minLength={8}
                maxLength={512}
                value={password}
                onChange={(e) => setPassword(e.currentTarget.value)}
              />
              <PasswordInput
                label="Confirm password"
                autoComplete="new-password"
                required
                minLength={8}
                maxLength={512}
                value={confirmPassword}
                onChange={(e) => setConfirmPassword(e.currentTarget.value)}
              />
            </div>

            <Group justify="flex-end">
              <Button type="submit" loading={busy}>Create administrator and continue</Button>
            </Group>
          </Stack>
        </form>
      </Paper>
    </div>
  );
}
