import { Alert, Badge, Button, Code, Group, List, Loader, Paper, Radio, ScrollArea, Stack, Table, Text, Title } from '@mantine/core';
import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiClient, ApiError, reportError } from '../api';
import { clock, describe, differences, liveApply, newer, stateLabels, useCountdown, type ChangeState } from '../changes';
import type { Finding, Session, VersionInfo } from '../types';
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

function pretty(value: unknown) {
  return value === undefined ? '' : JSON.stringify(value, null, 2);
}

function FindingsList({ findings }: { findings: Finding[] }) {
  return <List>{findings.map((finding, index) => <List.Item key={index}>{finding.message}{finding.path && <> <Code>{finding.path}</Code></>}</List.Item>)}</List>;
}

export function ChangesScreen({ api, session, state, stateError, connected, onChanged, onSessionLost }: ChangesScreenProps) {
  const canApply = session.capabilities.includes('configuration.apply');
  const canConfirm = session.capabilities.includes('configuration.confirm');
  const live = state ? liveApply(state.applies) : null;
  const latest = state?.applies.items[0];
  const [confirmWindow, setConfirmWindow] = useState('120');
  const [busy, setBusy] = useState('');
  const [message, setMessage] = useState('');
  const [refused, setRefused] = useState<Finding[]>([]);
  const remaining = useCountdown(latest?.state === 'awaiting_confirmation' ? latest.deadline : undefined);

  // The review reads its own live and head versions and keeps them until reloaded, so a save made
  // meanwhile never swaps what Apply sends.
  const load = useCallback(async () => {
    const [applies, first] = await Promise.all([api.listApplies(), api.versions(100)]);
    const liveSaved = liveApply(applies)?.savedVersion ?? '0';
    // The newest apply the review saw; a newer one in the shell's poll means live may have moved.
    const lastApply = applies.items[0]?.id ?? '0';
    const reviewed = first.items[0]?.id ?? '0';
    const saved: VersionInfo[] = [];
    let page = first;
    collect: for (;;) {
      for (const info of page.items) {
        if (!newer(info.id, liveSaved)) break collect;
        saved.push(info);
      }
      if (!page.nextBefore) break;
      page = await api.versions(100, page.nextBefore);
    }
    if (saved.length === 0) return { lastApply, reviewed, changes: null };
    // shortcut: one request per saved version since the live one; a server-side difference (spec call 10) replaces it.
    const [baseline, preview, ...versions] = await Promise.all([api.version(liveSaved), api.previewNetwork(reviewed), ...saved.map((info) => api.version(info.id))]);
    return { lastApply, reviewed, changes: { baseline, preview, versions } };
  }, [api]);
  const review = useResource(load, onSessionLost);
  const reloadReview = review.reload;

  // A reload the user asks for, or a reconnect, starts over: old answers no longer describe the review.
  const reload = useCallback(() => {
    setMessage('');
    setRefused([]);
    onChanged();
    void reloadReview();
  }, [onChanged, reloadReview]);

  const wasConnected = useRef(connected);
  useEffect(() => {
    if (connected && !wasConnected.current) reload();
    wasConnected.current = connected;
  }, [connected, reload]);

  async function apply(reviewed: string) {
    setBusy('apply');
    setMessage('');
    setRefused([]);
    try {
      setMessage(describe(await api.apply(reviewed, Number(confirmWindow))));
      void reloadReview();
    } catch (error) {
      if (error instanceof ApiError && error.status === 412 && error.code === 'PRECONDITION_FAILED') {
        setMessage('The saved configuration changed since this review. The review was reloaded: check it again before applying.');
        void reloadReview();
      } else if (error instanceof ApiError && error.status === 0) {
        setMessage('Connection lost during the apply. Its state is unknown until the box answers again.');
      } else {
        reportError(error, onSessionLost, setMessage);
        if (error instanceof ApiError) setRefused(error.findings);
      }
    } finally {
      setBusy('');
      onChanged();
    }
  }

  async function finish(id: string, action: 'confirmation' | 'rollback') {
    setBusy(action);
    setMessage('');
    setRefused([]);
    try {
      setMessage(describe(await api.finishApply(id, action)));
    } catch (error) {
      reportError(error, onSessionLost, setMessage);
    } finally {
      setBusy('');
      onChanged();
      void reloadReview();
    }
  }

  if (!state) return stateError ? <Alert color="red" role="alert" title="Changes request failed">{stateError}</Alert> : <Loader aria-label="Loading changes" />;

  const detail = review.data;
  const changes = detail?.changes;
  // Stale only when the shell has seen something newer than the review; a review newer than the
  // shell's last poll (another session applied meanwhile) is not stale.
  const stale = !detail ? '' : newer(state.head, detail.reviewed) ? `Saved version ${state.head} arrived since this review.` : newer(latest?.id ?? '0', detail.lastApply) ? 'The live configuration changed since this review.' : '';
  const changed = changes ? differences(changes.baseline.value, changes.versions[0].value, changes.versions.flatMap((version) => version.changes)) : [];
  const errors = changes?.preview.findings.filter((finding) => finding.severity === 'error') ?? [];
  const warnings = changes?.preview.findings.filter((finding) => finding.severity === 'warning') ?? [];
  const inFlight = latest?.state === 'awaiting_confirmation' || latest?.state === 'activating';
  const deadline = latest?.deadline ? clock(latest.deadline) : 'its deadline';

  return (
    <Stack>
      <div><Title order={2}>Review and apply</Title><Text c="dimmed">Saved changes take effect only when applied, and stay only when confirmed.</Text></div>
      {stateError && <Alert color="red" role="alert" title="Changes request failed">{stateError}</Alert>}
      {review.error && <Alert color="red" role="alert" title="Review request failed"><Stack gap="xs"><Text size="sm">{review.error}</Text><Group><Button size="xs" variant="default" loading={review.loading} onClick={reload}>Reload</Button></Group></Stack></Alert>}
      {stale && !review.error && <Alert color="yellow" role="status" title={stale}><Button size="xs" variant="default" loading={review.loading} onClick={reload}>Reload</Button></Alert>}
      {message && <Alert color="blue" role="status">{message}</Alert>}
      {refused.length > 0 && <Alert color="red" title="The engine refused the apply"><FindingsList findings={refused} /></Alert>}
      {!canApply && <Alert color="gray" role="status">Your account can review changes but not apply them.</Alert>}

      {busy === 'apply' && <Alert color="blue" role="status" title="Applying"><Group><Loader size="sm" /><Text size="sm">Applying saved version {detail?.reviewed}. The box may drop this connection while its network changes.</Text></Group></Alert>}
      {latest?.state === 'awaiting_confirmation' && (
        <Alert color={connected ? 'orange' : 'red'} title={connected ? 'Awaiting confirmation' : 'Connection lost'}>
          <Stack gap="xs">
            <Text size="sm">{connected
              ? `Apply ${latest.id} of saved version ${latest.savedVersion} is live. The box rolls back at ${deadline}${remaining && ` (${remaining} left)`} unless it is confirmed.`
              : `Connection lost: the box will roll back at ${deadline} unless this apply is confirmed. Its state is unknown until the box answers again.`}</Text>
            <Group>
              {canConfirm && <Button size="xs" loading={busy === 'confirmation'} disabled={Boolean(busy)} onClick={() => void finish(latest.id, 'confirmation')}>Confirm</Button>}
              {canApply && <Button size="xs" color="red" variant="default" loading={busy === 'rollback'} disabled={Boolean(busy)} onClick={() => void finish(latest.id, 'rollback')}>Roll back</Button>}
            </Group>
          </Stack>
        </Alert>
      )}
      {latest?.state === 'activating' && <Alert color="blue" role="status" title="Activating">Applying saved version {latest.savedVersion}…{latest.reason && ` ${latest.reason}`}</Alert>}
      {(latest?.state === 'failed' || latest?.state === 'rolled_back') && <Alert color="red" role="status" title={latest.state === 'failed' ? 'The last apply failed' : 'The last apply was rolled back'}>Apply {latest.id} of saved version {latest.savedVersion}.{latest.reason && ` ${latest.reason}`}</Alert>}
      {latest?.state === 'confirmed' && latest.id === live?.id && <Alert color="green" role="status" title="Confirmed">Apply {latest.id} of saved version {latest.savedVersion} is confirmed.</Alert>}

      <Paper withBorder p="md">
        <Title order={3} size="h4">What is live</Title>
        {live ? <Text>Apply {live.id}, saved version {live.savedVersion}: {stateLabels[live.state]}.</Text> : <Text>Nothing has been applied; the box runs its factory network.</Text>}
      </Paper>

      {(detail || review.loading) && (
        <Paper withBorder p="md">
          <Stack>
            <Title order={3} size="h4">What would change</Title>
            {!detail ? <Loader aria-label="Loading the review" /> : !changes ? (!review.error && <Text role="status">Nothing to apply: every saved change is live.</Text>) : (
              <>
                <Stack gap="xs">{changes.versions.map((version) => <div key={version.id}><Text fw={600}>Saved version {version.id} by {version.actor} at {new Date(version.createdAt).toLocaleString()}</Text><Text size="sm" c="dimmed">{version.changes.map((change) => `${change.collection} ${change.id}${change.created ? ' (created)' : change.deleted ? ' (deleted)' : ''}`).join(', ') || 'No changed objects'}</Text></div>)}</Stack>
                {changed.length === 0 ? <Text role="status">The saved versions end where the live one started.</Text> : changed.map((item) => (
                  <div key={`${item.collection} ${item.id}`} aria-label={`${item.label} ${item.kind}`}>
                    <Group gap="xs"><Badge color={item.kind === 'created' ? 'green' : item.kind === 'deleted' ? 'red' : 'yellow'}>{item.kind}</Badge><Text fw={600}>{item.label}</Text><Text size="sm" c="dimmed">{item.collection}</Text></Group>
                    <ScrollArea><Table withTableBorder><Table.Thead><Table.Tr><Table.Th>Live</Table.Th><Table.Th>Saved</Table.Th></Table.Tr></Table.Thead><Table.Tbody><Table.Tr>
                      <Table.Td valign="top">{item.kind === 'created' ? <Text c="dimmed" size="sm">Not present</Text> : <Code block>{pretty(item.before)}</Code>}</Table.Td>
                      <Table.Td valign="top">{item.kind === 'deleted' ? <Text c="dimmed" size="sm">Deleted</Text> : <Code block>{pretty(item.after)}</Code>}</Table.Td>
                    </Table.Tr></Table.Tbody></Table></ScrollArea>
                  </div>
                ))}
              </>
            )}
          </Stack>
        </Paper>
      )}

      {detail && changes && (
        <Paper withBorder p="md">
          <Stack>
            <Title order={3} size="h4">Network preview</Title>
            {changes.preview.findings.length === 0 && <Text>The network preview of saved version {detail.reviewed} found nothing to report.</Text>}
            {errors.length > 0 && <Alert color="red" role="alert" title="These problems block the apply"><FindingsList findings={errors} /></Alert>}
            {warnings.length > 0 && <Alert color="yellow" title="Warnings"><FindingsList findings={warnings} /></Alert>}
            {canApply && (
              <Group align="flex-end">
                <Radio.Group label="Confirmation window" value={confirmWindow} onChange={setConfirmWindow}>
                  <Group mt="xs">{['60', '120', '300'].map((seconds) => <Radio key={seconds} value={seconds} label={`${seconds} seconds`} />)}</Group>
                </Radio.Group>
                <Button color="orange" disabled={!changes.preview.realizable || inFlight || Boolean(busy) || review.loading || Boolean(stale) || Boolean(review.error)} onClick={() => void apply(detail.reviewed)}>Apply saved version {detail.reviewed}</Button>
              </Group>
            )}
          </Stack>
        </Paper>
      )}

      <Paper withBorder p="md">
        <Title order={3} size="h4" mb="sm">History</Title>
        {state.applies.items.length === 0 ? <Text>No apply yet.</Text> : (
          <ScrollArea><Table striped aria-label="Apply history"><Table.Thead><Table.Tr><Table.Th>Apply</Table.Th><Table.Th>State</Table.Th><Table.Th>Saved version</Table.Th><Table.Th>Time</Table.Th><Table.Th>Reason</Table.Th></Table.Tr></Table.Thead><Table.Tbody>
            {state.applies.items.slice(0, 20).map((item) => <Table.Tr key={item.id}><Table.Td>{item.id}</Table.Td><Table.Td>{stateLabels[item.state]}</Table.Td><Table.Td>{item.savedVersion}</Table.Td><Table.Td>{new Date(item.createdAt).toLocaleString()}</Table.Td><Table.Td>{item.reason || '—'}</Table.Td></Table.Tr>)}
          </Table.Tbody></Table></ScrollArea>
        )}
      </Paper>
    </Stack>
  );
}
