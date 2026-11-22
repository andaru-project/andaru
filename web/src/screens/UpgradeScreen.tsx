import { Alert, Badge, Button, Group, Paper, Radio, Stack, Text, Title } from '@mantine/core';
import { useCallback, useState } from 'react';
import { ApiClient, reportError } from '../api';
import type { Session, UpgradeView } from '../types';
import { useResource } from '../useResource';
import { Loaded } from './Loaded';

interface UpgradeScreenProps {
  api: ApiClient;
  session: Session;
  onFinished(): void;
  onSessionLost(): void;
}

// Finish the upgrade (settings safe mode): one card per question the saved settings need, each listing the objects
// it is about and its choices, the one they read with now marked. A choice can raise a further question, so the page
// asks the engine again after each one. Saving needs configuration.write; others see the page read-only.
export function UpgradeScreen({ api, session, onFinished, onSessionLost }: UpgradeScreenProps) {
  const canWrite = session.capabilities.includes('configuration.write');
  const [choices, setChoices] = useState<Record<string, string>>({});
  const load = useCallback(() => api.upgradePreview(choices), [api, choices]);
  const view = useResource<UpgradeView>(load, onSessionLost);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [saved, setSaved] = useState('');

  async function finish(current: UpgradeView) {
    setBusy(true);
    setError('');
    try {
      const result = await api.finishUpgrade(choices, current.savedVersion);
      setSaved(result.savedVersion);
      onFinished();
    } catch (requestError) {
      reportError(requestError, onSessionLost, setError);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Stack>
      <div>
        <Title order={2}>Finish the upgrade</Title>
        <Text c="dimmed">This Andaru reads the saved settings with the choices marked as current until someone answers. Until then, nothing can be saved or applied; the network keeps running as it was.</Text>
      </div>
      {saved && <Alert color="green" role="status" title={`Saved as version ${saved}`}>Review and apply the saved settings to put them on the network.</Alert>}
      <Loaded resource={view} what="the questions">
        {(current) => {
          const open = current.questions.filter((question) => !question.answered);
          if (current.questions.length === 0) return <Text>The saved settings wait for no decision.</Text>;
          return (
            <Stack>
              {current.questions.map((question) => (
                <Paper key={question.id} withBorder p="md">
                  <Stack gap="xs">
                    <Text fw={600}>{question.question}</Text>
                    <Text size="sm" c="dimmed">About: {question.objects.join(', ')}</Text>
                    <Radio.Group value={choices[question.id] ?? ''} onChange={(choice) => setChoices({ ...choices, [question.id]: choice })} aria-label={question.question}>
                      <Stack gap="xs">
                        {question.choices.map((choice) => (
                          <Radio key={choice.id} value={choice.id} disabled={!canWrite || busy} label={
                            <Group gap="xs">
                              <span>{choice.label}</span>
                              {choice.id === question.provisional && <Badge variant="light">Current reading</Badge>}
                            </Group>
                          } description={choice.description} />
                        ))}
                      </Stack>
                    </Radio.Group>
                  </Stack>
                </Paper>
              ))}
              {error && <Alert color="red" role="alert">{error}</Alert>}
              {canWrite
                ? <Group><Button disabled={open.length > 0 || busy} loading={busy} onClick={() => void finish(current)}>Save the answers</Button>{open.length > 0 && <Text size="sm" c="dimmed">{open.length === 1 ? 'One question is' : `${open.length} questions are`} still open.</Text>}</Group>
                : <Text c="dimmed">Answering needs permission to change the configuration.</Text>}
            </Stack>
          );
        }}
      </Loaded>
    </Stack>
  );
}
