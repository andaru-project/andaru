import { Alert, Badge, Button, Group, Modal, Paper, Stack, Table, Text, Title } from '@mantine/core';
import { useState, type ReactNode } from 'react';
import { reportError, type ApiClient } from '../api';
import { formatTimestamp, useTimeZone } from '../timezone';
import type { Session, UpdateDeployment, UpdateStatus } from '../types';
import { useVisiblePoll, type Resource } from '../useResource';
import { Loaded } from './Loaded';

interface UpdatesScreenProps {
  api: ApiClient;
  session: Session;
  update: Resource<UpdateStatus | undefined>;
  onSessionLost(): void;
}

type UpdateAction = 'check' | 'stage' | 'rollback' | 'reboot';

function Row({ label, children }: { label: string; children: ReactNode }) {
  return <Table.Tr><Table.Th scope="row">{label}</Table.Th><Table.Td>{children}</Table.Td></Table.Tr>;
}

function Facts({ children }: { children: ReactNode }) {
  return <Table className="facts" withRowBorders={false} verticalSpacing={2} horizontalSpacing={0}><Table.Tbody>{children}</Table.Tbody></Table>;
}

function formatWhen(iso: string | undefined, timeZone: string): string {
  if (!iso) return '—';
  const formatted = formatTimestamp(iso, timeZone);
  return formatted === '—' ? iso : formatted;
}

function shortDigest(digest?: string): string {
  if (!digest) return '—';
  return digest.length > 24 ? `${digest.slice(0, 19)}…` : digest;
}

const stateMeta: Record<UpdateStatus['state'], { color: string; label: string }> = {
  unchecked: { color: 'gray', label: 'Not checked yet' },
  up_to_date: { color: 'green', label: 'Up to date' },
  available: { color: 'orange', label: 'Update available' },
  staged: { color: 'teal', label: 'Staged — reboot required' },
  checking: { color: 'blue', label: 'Checking…' },
  staging: { color: 'blue', label: 'Downloading and staging…' },
  rolling_back: { color: 'yellow', label: 'Queueing rollback…' },
  error: { color: 'red', label: 'Check failed' },
  unsupported: { color: 'gray', label: 'Not available on this host' },
};

function DeploymentRows({ deployment }: { deployment: UpdateDeployment }) {
  const timeZone = useTimeZone();
  return (
    <>
      {deployment.version && <Row label="Version"><Text span ff="monospace" fw={600}>{deployment.version}</Text></Row>}
      {deployment.image && <Row label="Image"><Text span ff="monospace" size="sm">{deployment.image}</Text></Row>}
      {deployment.digest && <Row label="Digest"><Text span ff="monospace" size="sm" title={deployment.digest}>{shortDigest(deployment.digest)}</Text></Row>}
      {deployment.timestamp && <Row label="Built">{formatWhen(deployment.timestamp, timeZone)}</Row>}
      {deployment.signature && (
        <Row label="Signature">
          <Badge size="xs" variant="light" color={deployment.signature === 'containerPolicy' ? 'green' : 'gray'} className="plain-badge">
            {deployment.signature === 'containerPolicy' ? 'Verified (cosign policy)' : deployment.signature}
          </Badge>
        </Row>
      )}
    </>
  );
}

function DeploymentSection({
  id,
  title,
  badgeColor,
  badgeVariant,
  badgeLabel,
  deployment,
  note,
  action,
}: {
  id: string;
  title: string;
  badgeColor: string;
  badgeVariant: 'filled' | 'light';
  badgeLabel: string;
  deployment?: UpdateDeployment;
  note?: string;
  action?: ReactNode;
}) {
  return (
    <Paper component="section" withBorder p="md" aria-labelledby={id}>
      <Group justify="space-between" mb="sm">
        <Title order={3} size="h5" id={id}>{title}</Title>
        <Badge color={badgeColor} variant={badgeVariant}>{badgeLabel}</Badge>
      </Group>
      <Stack gap="sm">
        {deployment && (
          <Facts>
            <DeploymentRows deployment={deployment} />
          </Facts>
        )}
        {note && <Text size="sm" c="dimmed">{note}</Text>}
        {action && <Group>{action}</Group>}
      </Stack>
    </Paper>
  );
}

function bootedIdentity(status?: UpdateStatus): string {
  const b = status?.booted;
  return b ? `${b.digest ?? ''}|${b.version ?? ''}` : '';
}

export function UpdatesScreen({ api, session, update, onSessionLost }: UpdatesScreenProps) {
  const timeZone = useTimeZone();
  const canManage = session.capabilities.includes('configuration.apply');
  const [busy, setBusy] = useState<UpdateAction | null>(null);
  const [actionError, setActionError] = useState('');
  const [rebootedFrom, setRebootedFrom] = useState<string | null>(null);
  const [confirmKind, setConfirmKind] = useState<'reboot' | 'rollback' | null>(null);

  const currentBooted = bootedIdentity(update.data);
  const rebooting = rebootedFrom !== null && (currentBooted === '' || currentBooted === rebootedFrom);
  const activeOp = update.data?.state === 'staging' || update.data?.state === 'checking' || update.data?.state === 'rolling_back';
  // Poll every 3 s while an async stage or engine-side check is running.
  useVisiblePoll(update.refresh, 3_000, activeOp && !rebooting);

  async function runAction(kind: UpdateAction) {
    setBusy(kind);
    setActionError('');
    try {
      if (kind === 'check') await api.checkUpdate();
      else if (kind === 'stage') await api.stageUpdate();
      else if (kind === 'rollback') await api.rollbackUpdate();
      else {
        await api.rebootSystem();
        setRebootedFrom(currentBooted);
        return;
      }
      await update.reload();
    } catch (error) {
      reportError(error, onSessionLost, setActionError);
      void update.reload();
    } finally {
      setBusy(null);
    }
  }

  return (
    <Stack className="details">
      <Group justify="space-between" align="flex-end" wrap="wrap" gap="sm">
        <div>
          <Title order={2}>Software updates</Title>
          <Text c="dimmed">Signed Andaru appliance image updates and rollbacks.</Text>
        </div>
        {canManage && (
          <Button
            variant="default"
            loading={busy === 'check'}
            disabled={busy !== null || activeOp || rebooting}
            onClick={() => void runAction('check')}
          >
            Check for updates
          </Button>
        )}
      </Group>

      {actionError && <Alert color="red" role="alert" title="Update action failed">{actionError}</Alert>}
      {rebooting && (
        <Alert color="teal" role="status" title="Rebooting appliance">
          The box is restarting into the staged image. Connectivity will pause briefly and this page will reconnect once Andaru is back.
        </Alert>
      )}
      {!canManage && (
        <Alert color="blue" variant="light" title="Read-only access">
          Checking for, staging, rolling back, and rebooting for software updates requires the configuration.apply capability.
        </Alert>
      )}

      <Loaded resource={update} what="software update status">
        {(status) => {
          if (!status) return null;
          const badge = stateMeta[status.state] ?? stateMeta.unsupported;
          const inProgress = busy !== null || activeOp;

          if (!status.supported) {
            return (
              <Paper component="section" withBorder p="md">
                <Stack gap="xs">
                  <Group justify="space-between">
                    <Title order={3} size="h5">Appliance image updates</Title>
                    <Badge color="gray" variant="light">{badge.label}</Badge>
                  </Group>
                  <Text size="sm" c="dimmed">
                    This host is not booted from a bootc-managed Andaru appliance image, so image updates are unavailable here.
                  </Text>
                  {status.checkedAt && <Text size="xs" c="dimmed" className="tabular">Last checked: {formatWhen(status.checkedAt, timeZone)}</Text>}
                </Stack>
              </Paper>
            );
          }

          const pendingDeployment = status.staged ?? (status.rollbackQueued ? status.rollback : undefined);

          return (
            <Stack>
              {status.state === 'error' && status.error && (
                <Alert color="orange" role="alert" title="Last update check reported an error">
                  {status.error}
                </Alert>
              )}

              <Paper component="section" withBorder p="md" aria-labelledby="updates-current">
                <Group justify="space-between" mb="sm">
                  <Title order={3} size="h5" id="updates-current">Running image</Title>
                  <Badge color={badge.color} variant={status.state === 'available' || status.rebootNeeded ? 'filled' : 'light'}>
                    {badge.label}
                  </Badge>
                </Group>
                <Facts>
                  {status.booted && <DeploymentRows deployment={status.booted} />}
                  <Row label="Last checked">{formatWhen(status.checkedAt, timeZone)}</Row>
                </Facts>
              </Paper>

              {(status.staged || status.rebootNeeded) && (
                <DeploymentSection
                  id="updates-staged"
                  title={status.rollbackQueued && !status.staged ? 'Rollback queued for next boot' : 'Staged for next boot'}
                  badgeColor="teal"
                  badgeVariant="filled"
                  badgeLabel="Reboot required"
                  deployment={pendingDeployment}
                  note={
                    status.rollbackQueued && !status.staged
                      ? 'The previous deployment is queued for the next boot. Reboot when you are ready to switch back.'
                      : 'The signed image is verified and staged on disk. Reboot when you are ready to switch to it.'
                  }
                  action={
                    canManage ? (
                      <Button
                        color="teal"
                        loading={busy === 'reboot'}
                        disabled={inProgress || rebooting}
                        onClick={() => setConfirmKind('reboot')}
                      >
                        Reboot to apply
                      </Button>
                    ) : undefined
                  }
                />
              )}

              {status.available && !status.staged && !status.rollbackQueued && (
                <DeploymentSection
                  id="updates-available"
                  title="Available update"
                  badgeColor="orange"
                  badgeVariant="filled"
                  badgeLabel={status.state === 'staging' ? 'Downloading…' : 'Ready to download'}
                  deployment={status.available}
                  note="Downloading stages the signed image without interrupting traffic. You choose when to reboot."
                  action={
                    canManage ? (
                      <Button
                        loading={busy === 'stage' || status.state === 'staging'}
                        disabled={inProgress || rebooting}
                        onClick={() => void runAction('stage')}
                      >
                        Download and stage update
                      </Button>
                    ) : undefined
                  }
                />
              )}

              {status.rollback && (
                <DeploymentSection
                  id="updates-rollback"
                  title="Previous image (rollback)"
                  badgeColor="gray"
                  badgeVariant="light"
                  badgeLabel={status.rollbackQueued ? 'Queued' : 'Retained'}
                  deployment={status.rollback}
                  action={
                    canManage && !status.rollbackQueued ? (
                      <Button
                        variant="default"
                        color="orange"
                        loading={busy === 'rollback'}
                        disabled={inProgress || rebooting}
                        onClick={() => setConfirmKind('rollback')}
                      >
                        Roll back to previous version
                      </Button>
                    ) : undefined
                  }
                />
              )}
            </Stack>
          );
        }}
      </Loaded>

      <Modal
        opened={confirmKind !== null}
        onClose={() => setConfirmKind(null)}
        title={confirmKind === 'reboot' ? 'Reboot appliance now?' : 'Roll back to previous version?'}
      >
        <Stack>
          <Text size="sm">
            {confirmKind === 'reboot'
              ? 'Rebooting switches the box into the staged image. Network traffic through this firewall will pause until startup completes.'
              : 'Rolling back queues the previous deployment for the next boot without rebooting immediately.'}
          </Text>
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setConfirmKind(null)}>Cancel</Button>
            <Button
              color={confirmKind === 'reboot' ? 'teal' : 'orange'}
              onClick={() => {
                const kind = confirmKind;
                setConfirmKind(null);
                if (kind) void runAction(kind);
              }}
            >
              {confirmKind === 'reboot' ? 'Reboot now' : 'Queue rollback'}
            </Button>
          </Group>
        </Stack>
      </Modal>
    </Stack>
  );
}
