import { Alert, Anchor, Button, Group, Text } from '@mantine/core';
import { useState } from 'react';
import { Link } from 'react-router';
import { ApiClient, reportError } from './api';
import { clock, pendingNotice, useCountdown, type ChangeState } from './changes';
import { useTimeZone } from './timezone';
import type { Session } from './types';

interface PendingNoticeProps {
  api: ApiClient;
  session: Session;
  state: ChangeState | undefined;
  onChanged(): void;
  onSessionLost(): void;
}

export function PendingNotice({ api, session, state, onChanged, onSessionLost }: PendingNoticeProps) {
  const timeZone = useTimeZone();
  const notice = state ? pendingNotice(state) : null;
  const remaining = useCountdown(notice?.kind === 'awaiting' ? notice.apply.deadline : undefined);
  const [busy, setBusy] = useState(false);
  // An error belongs to the apply it was about; another apply's notice does not show it.
  const [error, setError] = useState({ apply: '', message: '' });

  async function finish(id: string, action: 'confirmation' | 'rollback') {
    setBusy(true);
    setError({ apply: '', message: '' });
    try {
      await api.finishApply(id, action);
    } catch (requestError) {
      reportError(requestError, onSessionLost, (message) => setError({ apply: id, message }));
    } finally {
      setBusy(false);
      onChanged();
    }
  }

  if (!notice) return null;
  if (notice.kind === 'unapplied') return <Alert color="yellow" role="status" title="Saved changes are not applied yet"><Anchor component={Link} to="/changes">Review and apply</Anchor></Alert>;
  if (notice.kind === 'activating') return <Alert color="blue" role="status" title={`Applying saved version ${notice.apply.savedVersion}…`} />;
  if (notice.kind === 'failed' || notice.kind === 'rolled_back') {
    return <Alert color="red" role="status" title={notice.kind === 'failed' ? 'The last apply failed' : 'The last apply was rolled back'}><Anchor component={Link} to="/changes">See its details</Anchor></Alert>;
  }
  const { apply } = notice;
  return (
    <Alert color="orange" role="status" title={`Applied, awaiting confirmation: rolls back at ${apply.deadline ? clock(apply.deadline, timeZone) : 'an unknown time'}`}>
      <Group>
        {remaining && <Text size="sm" className="tabular">{remaining} left</Text>}
        {session.capabilities.includes('configuration.confirm') && <Button size="xs" disabled={busy} onClick={() => void finish(apply.id, 'confirmation')}>Confirm</Button>}
        {session.capabilities.includes('configuration.apply') && <Button size="xs" color="red" variant="default" disabled={busy} onClick={() => void finish(apply.id, 'rollback')}>Roll back</Button>}
        <Anchor component={Link} to="/changes">Review and apply</Anchor>
      </Group>
      {error.apply === apply.id && <Text className="error-text" size="sm" role="alert" mt="xs">{error.message}</Text>}
    </Alert>
  );
}
