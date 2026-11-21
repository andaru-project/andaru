import { Alert, Badge, Button, Code, Group, List, Loader, Paper, Radio, ScrollArea, Stack, Table, Text, Title } from '@mantine/core';
import { useCallback, useState } from 'react';
import { ApiClient, ApiError, apiErrorMessage } from '../api';
import { clock, differences, liveApply, newer, useCountdown, type ChangeState } from '../changes';
import type { Apply, Session, VersionInfo } from '../types';
import { useResource } from '../useResource';

interface ChangesScreenProps {
  api: ApiClient;
  session: Session;
  state: ChangeState | undefined;
  stateError: string;
  connected: boolean;
  onChanged(): void;
  onSessionLost(): void;
}

const stateLabels: Record<Apply['state'], string> = {
  activating: 'Activating',
  awaiting_confirmation: 'Awaiting confirmation',
  confirmed: 'Confirmed',
  rolled_back: 'Rolled back',
  failed: 'Failed',
};

function show(value: unknown) {
  return value === undefined ? '' : JSON.stringify(value, null, 2);
}

export function ChangesScreen({ api, session, state, stateError, connected, onChanged, onSessionLost }: ChangesScreenProps) {
  const canApply = session.capabilities.includes('configuration.apply');
  const canConfirm = session.capabilities.includes('configuration.confirm');
  const ready = Boolean(state);
  const head = state?.head ?? '0';
  const live = state ? liveApply(state.applies) : null;
  const liveSaved = live?.savedVersion ?? '0';
  const latest = state?.applies.items[0];
  const [confirmWindow, setConfirmWindow] = useState('120');
  const [busy, setBusy] = useState('');
  const [message, setMessage] = useState('');
  const remaining = useCountdown(latest?.state === 'awaiting_confirmation' ? latest.deadline : undefined);

  const load = useCallback(async () => {
    if (!ready || !newer(head, liveSaved)) return null;
    const saved: VersionInfo[] = [];
    let before = '';
    collect: for (;;) {
      const page = await api.versions(100, before);
      for (const info of page.items) {
        if (!newer(info.id, liveSaved)) break collect;
        saved.push(info);
      }
      if (!page.nextBefore) break;
      before = page.nextBefore;
    }
    if (saved.length === 0) return null;
    // shortcut: one request per saved version since the live one; a server-side difference (spec call 10) replaces it.
    const [baseline, preview, ...versions] = await Promise.all([api.version(liveSaved), api.previewNetwork(saved[0].id), ...saved.map((info) => api.version(info.id))]);
    return { reviewed: saved[0].id, baseline, preview, versions };
  }, [api, head, liveSaved, ready]);
  const review = useResource(load, onSessionLost);

  function failed(error: unknown) {
    if (error instanceof ApiError && error.status === 401) onSessionLost();
    else setMessage(apiErrorMessage(error));
  }

  async function apply(reviewed: string) {
    setBusy('apply');
    setMessage('');
    try {
      const result = await api.apply(reviewed, Number(confirmWindow));
      setMessage(`Apply ${result.id}: ${stateLabels[result.state]}.${result.reason ? ` ${result.reason}` : ''}`);
    } catch (error) {
      if (error instanceof ApiError && (error.status === 409 || error.status === 412)) setMessage('The saved configuration changed since this review. The review was reloaded: check it again before applying.');
      else if (error instanceof ApiError && error.status === 0) setMessage('Connection lost during the apply. Its state is unknown until the box answers again.');
      else failed(error);
    } finally {
      setBusy('');
      onChanged();
      void review.reload();
    }
  }

  async function finish(id: string, action: 'confirmation' | 'rollback') {
    setBusy(action);
    setMessage('');
    try {
      const result = await api.finishApply(id, action);
      setMessage(`Apply ${result.id}: ${stateLabels[result.state]}.${result.reason ? ` ${result.reason}` : ''}`);
    } catch (error) {
      failed(error);
    } finally {
      setBusy('');
      onChanged();
    }
  }

  if (!state) return stateError ? <Alert color="red" role="alert" title="Changes request failed">{stateError}</Alert> : <Loader aria-label="Loading changes" />;

  const detail = review.data;
  const changed = detail ? differences(detail.baseline.value, detail.versions[0].value, detail.versions.flatMap((version) => version.changes)) : [];
  const errors = detail?.preview.findings.filter((finding) => finding.severity === 'error') ?? [];
  const warnings = detail?.preview.findings.filter((finding) => finding.severity === 'warning') ?? [];
  const inFlight = latest?.state === 'awaiting_confirmation' || latest?.state === 'activating';

  return (
    <Stack>
      <div><Title order={2}>Review and apply</Title><Text c="dimmed">Saved changes take effect only when applied, and stay only when confirmed.</Text></div>
      {stateError && <Alert color="red" role="alert" title="Changes request failed">{stateError}</Alert>}
      {review.error && <Alert color="red" role="alert" title="Review request failed">{review.error}</Alert>}
      {message && <Alert color="blue" role="status">{message}</Alert>}
      {!canApply && <Alert color="gray" role="status">Your account can review changes but not apply them.</Alert>}

      {busy === 'apply' && <Alert color="blue" role="status" title="Applying"><Group><Loader size="sm" /><Text size="sm">Applying saved version {detail?.reviewed}. The box may drop this connection while its network changes.</Text></Group></Alert>}
      {latest?.state === 'awaiting_confirmation' && (
        !connected ? (
          <Alert color="red" role="alert" title="Connection lost">Connection lost: the box will roll back at {latest.deadline ? clock(latest.deadline) : 'its deadline'} unless this apply is confirmed. Its state is unknown until the box answers again.</Alert>
        ) : (
          <Alert color="orange" title="Awaiting confirmation">
            <Stack gap="xs">
              <Text size="sm">Apply {latest.id} of saved version {latest.savedVersion} is live. The box rolls back at {latest.deadline ? clock(latest.deadline) : 'its deadline'}{remaining && ` (${remaining} left)`} unless it is confirmed.</Text>
              <Group>
                {canConfirm && <Button size="xs" loading={busy === 'confirmation'} onClick={() => void finish(latest.id, 'confirmation')}>Confirm</Button>}
                {canApply && <Button size="xs" color="red" variant="default" loading={busy === 'rollback'} onClick={() => void finish(latest.id, 'rollback')}>Roll back</Button>}
              </Group>
            </Stack>
          </Alert>
        )
      )}
      {latest?.state === 'activating' && <Alert color="blue" role="status" title="Activating">Apply {latest.id} of saved version {latest.savedVersion} is activating.{latest.reason && ` ${latest.reason}`}</Alert>}
      {(latest?.state === 'failed' || latest?.state === 'rolled_back') && <Alert color="red" role="status" title={latest.state === 'failed' ? 'The last apply failed' : 'The last apply was rolled back'}>Apply {latest.id} of saved version {latest.savedVersion}.{latest.reason && ` ${latest.reason}`}</Alert>}
      {latest?.state === 'confirmed' && latest.id === live?.id && <Alert color="green" role="status" title="Confirmed">Apply {latest.id} of saved version {latest.savedVersion} is confirmed.</Alert>}

      <Paper withBorder p="md">
        <Title order={3} size="h4">What is live</Title>
        {live ? <Text>Apply {live.id}, saved version {live.savedVersion}: {stateLabels[live.state]}.</Text> : <Text>Nothing has been applied; the box runs its factory network.</Text>}
      </Paper>

      <Paper withBorder p="md">
        <Stack>
          <Title order={3} size="h4">What would change</Title>
          {review.loading && !detail ? <Loader aria-label="Loading the review" /> : !detail ? <Text role="status">Nothing to apply: every saved change is live.</Text> : (
            <>
              <Stack gap="xs">{detail.versions.map((version) => <div key={version.id}><Text fw={600}>Saved version {version.id} by {version.actor} at {new Date(version.createdAt).toLocaleString()}</Text><Text size="sm" c="dimmed">{version.changes.map((change) => `${change.collection} ${change.id}${change.created ? ' (created)' : change.deleted ? ' (deleted)' : ''}`).join(', ') || 'No changed objects'}</Text></div>)}</Stack>
              {changed.length === 0 ? <Text role="status">The saved versions end where the live one started.</Text> : changed.map((item) => (
                <div key={`${item.collection} ${item.id}`} aria-label={`${item.label} ${item.kind}`}>
                  <Group gap="xs"><Badge color={item.kind === 'created' ? 'green' : item.kind === 'deleted' ? 'red' : 'yellow'}>{item.kind}</Badge><Text fw={600}>{item.label}</Text><Text size="sm" c="dimmed">{item.collection}</Text></Group>
                  <ScrollArea><Table withTableBorder><Table.Thead><Table.Tr><Table.Th>Live</Table.Th><Table.Th>Saved</Table.Th></Table.Tr></Table.Thead><Table.Tbody><Table.Tr>
                    <Table.Td valign="top">{item.kind === 'created' ? <Text c="dimmed" size="sm">Not present</Text> : <Code block>{show(item.before)}</Code>}</Table.Td>
                    <Table.Td valign="top">{item.kind === 'deleted' ? <Text c="dimmed" size="sm">Deleted</Text> : <Code block>{show(item.after)}</Code>}</Table.Td>
                  </Table.Tr></Table.Tbody></Table></ScrollArea>
                </div>
              ))}
            </>
          )}
        </Stack>
      </Paper>

      {detail && (
        <Paper withBorder p="md">
          <Stack>
            <Title order={3} size="h4">Network preview</Title>
            {detail.preview.findings.length === 0 && <Text>The network preview of saved version {detail.reviewed} found nothing to report.</Text>}
            {errors.length > 0 && <Alert color="red" role="alert" title="These problems block the apply"><List>{errors.map((finding) => <List.Item key={`${finding.code} ${finding.path}`}>{finding.message}{finding.path && <> <Code>{finding.path}</Code></>}</List.Item>)}</List></Alert>}
            {warnings.length > 0 && <Alert color="yellow" title="Warnings"><List>{warnings.map((finding) => <List.Item key={`${finding.code} ${finding.path}`}>{finding.message}{finding.path && <> <Code>{finding.path}</Code></>}</List.Item>)}</List></Alert>}
            {canApply && (
              <Group align="flex-end">
                <Radio.Group label="Confirmation window" value={confirmWindow} onChange={setConfirmWindow}>
                  <Group mt="xs">{['60', '120', '300'].map((seconds) => <Radio key={seconds} value={seconds} label={`${seconds} seconds`} />)}</Group>
                </Radio.Group>
                <Button color="orange" disabled={!detail.preview.realizable || inFlight || Boolean(busy) || review.loading} onClick={() => void apply(detail.reviewed)}>Apply saved version {detail.reviewed}</Button>
              </Group>
            )}
          </Stack>
        </Paper>
      )}

      <Paper withBorder p="md">
        <Title order={3} size="h4" mb="sm">History</Title>
        {state.applies.items.length === 0 ? <Text>No apply yet.</Text> : (
          <ScrollArea><Table striped><Table.Thead><Table.Tr><Table.Th>Apply</Table.Th><Table.Th>State</Table.Th><Table.Th>Saved version</Table.Th><Table.Th>Time</Table.Th><Table.Th>Reason</Table.Th></Table.Tr></Table.Thead><Table.Tbody>
            {state.applies.items.slice(0, 20).map((item) => <Table.Tr key={item.id}><Table.Td>{item.id}</Table.Td><Table.Td>{stateLabels[item.state]}</Table.Td><Table.Td>{item.savedVersion}</Table.Td><Table.Td>{new Date(item.createdAt).toLocaleString()}</Table.Td><Table.Td>{item.reason || '—'}</Table.Td></Table.Tr>)}
          </Table.Tbody></Table></ScrollArea>
        )}
      </Paper>
    </Stack>
  );
}
