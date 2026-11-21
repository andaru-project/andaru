import { Alert, Anchor, Button, Group, Text } from '@mantine/core';
import { useState } from 'react';
import { Link } from 'react-router';
import { ApiClient, ApiError, apiErrorMessage } from './api';
import { clock, pendingNotice, useCountdown, type ChangeState } from './changes';

interface PendingNoticeProps {
  api: ApiClient;
  capabilities: Set<string>;
  state: ChangeState | undefined;
  onChanged(): void;
  onSessionLost(): void;
}

export function PendingNotice({ api, capabilities, state, onChanged, onSessionLost }: PendingNoticeProps) {
  const notice = state ? pendingNotice(state) : null;
  const deadline = notice?.kind === 'awaiting' ? notice.apply.deadline : undefined;
  const remaining = useCountdown(deadline);
  const [error, setError] = useState('');

  async function finish(id: string, action: 'confirmation' | 'rollback') {
    setError('');
    try {
      await api.finishApply(id, action);
    } catch (requestError) {
      if (requestError instanceof ApiError && requestError.status === 401) onSessionLost();
      else setError(apiErrorMessage(requestError));
    }
    onChanged();
  }

  if (!notice) return null;
  const details = <Anchor component={Link} to="/changes">Review and apply</Anchor>;
  if (notice.kind === 'unapplied') return <Alert color="yellow" role="status" title="Saved changes are not applied yet">{details}</Alert>;
  if (notice.kind === 'failed' || notice.kind === 'rolled_back') {
    return <Alert color="red" role="status" title={notice.kind === 'failed' ? 'The last apply failed' : 'The last apply was rolled back'}><Anchor component={Link} to="/changes">See its details</Anchor></Alert>;
  }
  const { apply } = notice;
  return (
    <Alert color="orange" role="status" title={`Applied, awaiting confirmation: rolls back at ${apply.deadline ? clock(apply.deadline) : 'an unknown time'}`}>
      <Group>
        {remaining && <Text size="sm">{remaining} left</Text>}
        {capabilities.has('configuration.confirm') && <Button size="xs" onClick={() => void finish(apply.id, 'confirmation')}>Confirm</Button>}
        {capabilities.has('configuration.apply') && <Button size="xs" color="red" variant="default" onClick={() => void finish(apply.id, 'rollback')}>Roll back</Button>}
        {details}
      </Group>
      {error && <Text c="red" size="sm" role="alert" mt="xs">{error}</Text>}
    </Alert>
  );
}
