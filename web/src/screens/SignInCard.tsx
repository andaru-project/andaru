import { Alert, Button, Group, Image, Paper, Stack, Text, Title } from '@mantine/core';
import type { FormEvent, ReactNode } from 'react';
import mark from '../andaru-icon.svg';

interface SignInCardProps {
  title: string;
  description: string;
  notice?: string;
  error: string;
  busy: boolean;
  onSubmit(event: FormEvent): void;
  children: ReactNode;
}

// The sign-in page Andaru and standalone Netplane share; each brings its own fields.
export function SignInCard({ title, description, notice, error, busy, onSubmit, children }: SignInCardProps) {
  return (
    <div className="login-page">
      <Paper className="login-card" component="main" withBorder p="xl">
        <form onSubmit={onSubmit}>
          <Stack>
            <Group gap="sm" wrap="nowrap">
              <Image src={mark} alt="" w={40} h={40} />
              <div>
                <Title order={1} size="h2">{title}</Title>
                <Text c="dimmed">{description}</Text>
              </div>
            </Group>
            {notice && <Alert color="yellow" role="alert">{notice}</Alert>}
            {error && <Alert color="red" role="alert" title="Sign-in failed">{error}</Alert>}
            {children}
            <Button loading={busy} type="submit">Sign in</Button>
          </Stack>
        </form>
      </Paper>
    </div>
  );
}
