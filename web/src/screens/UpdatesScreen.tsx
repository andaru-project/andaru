import { Alert, Badge, Button, Group, Modal, Paper, Stack, Table, Text, Title } from '@mantine/core';
import { useState, type ReactNode } from 'react';
import { reportError, type ApiClient } from '../api';
import type { Session, UpdateDeployment, UpdateStatus } from '../types';
import type { Resource } from '../useResource';
import { Loaded } from './Loaded';

interface UpdatesScreenProps {
  api: ApiClient;
  session: Session;
  update: Resource<UpdateStatus | undefined>;
  onSessionLost(): void;
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return <Table.Tr><Table.Th scope="row">{label}</Table.Th><Table.Td>{children}</Table.Td></Table.Tr>;
}

function Facts({ children }: { children: ReactNode }) {
  return <Table className="facts" withRowBorders={false} verticalSpacing={2} horizontalSpacing={0}><Table.Tbody>{children}</Table.Tbody></Table>;
}

function formatUtc(iso?: string): string {
  if (!iso) return '—';
  const ms = Date.parse(iso);
  if (!Number.isFinite(ms)) return iso;
  return `${new Date(ms).toISOString().slice(0, 16).replace('T', ' ')} UTC`;
}

function shortDigest(digest?: string): string {
  if (!digest) return '—';
  return digest.length > 24 ? `${digest.slice(0, 19)}…` : digest;
}

const stateMeta: Record<UpdateStatus['state'], { color: string; label: string }> = {
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
  return (
    <>
      {deployment.version && <Row label="Version"><Text span ff="monospace" fw={600}>{deployment.version}</Text></Row>}
      {deployment.image && <Row label="Image"><Text span ff="monospace" size="sm">{deployment.image}</Text></Row>}
      {deployment.digest && <Row label="Digest"><Text span ff="monospace" size="sm" title={deployment.digest}>{shortDigest(deployment.digest)}</Text></Row>}
      {deployment.timestamp && <Row label="Built">{formatUtc(deployment.timestamp)}</Row>}
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

export function UpdatesScreen({ api, session, update, onSessionLost }: UpdatesScreenProps) {
  const canManage = session.capabilities.includes('configuration.apply');
  const [busy, setBusy] = useState<'check' | 'stage' | 'rollback' | 'reboot' | null>(null);
  const [actionError, setActionError] = useState('');
  const [rebooting, setRebooting] = useState(false);
  const [confirmKind, setConfirmKind] = useState<'reboot' | 'rollback' | null>(null);

  async function runAction(kind: 'check' | 'stage' | 'rollback' | 'reboot') {
    setBusy(kind);
    setActionError('');
    try {
      if (kind === 'check') await api.checkUpdate();
      else if (kind === 'stage') await api.stageUpdate();
      else if (kind === 'rollback') await api.rollbackUpdate();
      else {
        await api.rebootSystem();
        setRebooting(true);
        return;
      }
      await update.reload();
    } catch (error) {
      reportError(error, onSessionLost, setActionError);
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
            disabled={busy !== null || rebooting}
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
          const inProgress = busy !== null || status.state === 'checking' || status.state === 'staging' || status.state === 'rolling_back';

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
                  {status.checkedAt && <Text size="xs" c="dimmed">Last checked: {formatUtc(status.checkedAt)}</Text>}
                </Stack>
              </Paper>
            );
          }

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
                  <Row label="Last checked">{formatUtc(status.checkedAt)}</Row>
                </Facts>
              </Paper>

              {status.staged && (
                <Paper component="section" withBorder p="md" aria-labelledby="updates-staged">
                  <Group justify="space-between" mb="sm">
                    <Title order={3} size="h5" id="updates-staged">Staged for next boot</Title>
                    <Badge color="teal" variant="filled">Reboot required</Badge>
                  </Group>
                  <Stack gap="sm">
                    <Facts>
                      <DeploymentRows deployment={status.staged} />
                    </Facts>
                    <Text size="sm" c="dimmed">
                      The signed image is verified and staged on disk. Reboot when you are ready to switch to it.
                    </Text>
                    {canManage && (
                      <Group>
                        <Button
                          color="teal"
                          loading={busy === 'reboot'}
                          disabled={inProgress || rebooting}
                          onClick={() => setConfirmKind('reboot')}
                        >
                          Reboot to apply
                        </Button>
                      </Group>
                    )}
                  </Stack>
                </Paper>
              )}

              {status.available && !status.staged && (
                <Paper component="section" withBorder p="md" aria-labelledby="updates-available">
                  <Group justify="space-between" mb="sm">
                    <Title order={3} size="h5" id="updates-available">Available update</Title>
                    <Badge color="orange" variant="filled">Ready to download</Badge>
                  </Group>
                  <Stack gap="sm">
                    <Facts>
                      <DeploymentRows deployment={status.available} />
                    </Facts>
                    <Text size="sm" c="dimmed">
                      Downloading stages the signed image without interrupting traffic. You choose when to reboot.
                    </Text>
                    {canManage && (
                      <Group>
                        <Button
                          loading={busy === 'stage'}
                          disabled={inProgress || rebooting}
                          onClick={() => void runAction('stage')}
                        >
                          Download and stage update
                        </Button>
                      </Group>
                    )}
                  </Stack>
                </Paper>
              )}

              {status.rollback && (
                <Paper component="section" withBorder p="md" aria-labelledby="updates-rollback">
                  <Group justify="space-between" mb="sm">
                    <Title order={3} size="h5" id="updates-rollback">Previous image (rollback)</Title>
                    <Badge color="gray" variant="light">Retained</Badge>
                  </Group>
                  <Stack gap="sm">
                    <Facts>
                      <DeploymentRows deployment={status.rollback} />
                    </Facts>
                    {canManage && (
                      <Group>
                        <Button
                          variant="default"
                          color="orange"
                          loading={busy === 'rollback'}
                          disabled={inProgress || rebooting}
                          onClick={() => setConfirmKind('rollback')}
                        >
                          Roll back to previous version
                        </Button>
                      </Group>
                    )}
                  </Stack>
                </Paper>
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
