import {
  Alert,
  Badge,
  Button,
  Card,
  Group,
  Stack,
  Text,
  Title,
} from '@mantine/core';
import { useCallback, useState } from 'react';
import { Link, useNavigate } from 'react-router';
import { AddonMarkdown } from '../addonMarkdown';
import { healthBadgeColor, healthLabel } from '../addons';
import { ApiClient, reportError } from '../api';
import { plural } from '../format';
import { formatSafeModeReasons } from './SystemWidget';
import { UninstallAddonModal } from './UninstallAddonModal';
import type { AddonSummary, Session } from '../types';
import { useResource, useVisiblePoll } from '../useResource';
import { Loaded } from './Loaded';

interface AddonsScreenProps {
  api: ApiClient;
  session: Session;
  onSaved(): void;
  onSessionLost(): void;
}

interface PendingUninstall {
  item: AddonSummary;
  version: string;
}

export function AddonsScreen({ api, session, onSaved, onSessionLost }: AddonsScreenProps) {
  const navigate = useNavigate();
  const capabilities = new Set(session.capabilities);
  const canManage = capabilities.has('addons.manage');
  const canReadConfig = capabilities.has('configuration.read');
  const canWriteConfig = capabilities.has('configuration.write');

  const load = useCallback(() => api.addons(), [api]);
  const resource = useResource(load, onSessionLost);

  const anyStaging = resource.data?.items.some((item) => item.staged.staging) ?? false;
  useVisiblePoll(resource.refresh, 2_000, anyStaging);

  const [busySlugs, setBusySlugs] = useState<ReadonlySet<string>>(() => new Set());
  const [actionError, setActionError] = useState('');
  const [actionNotice, setActionNotice] = useState('');
  const [uninstallTarget, setUninstallTarget] = useState<PendingUninstall | null>(null);
  const [uninstalling, setUninstalling] = useState(false);

  function setBusy(slug: string, busy: boolean) {
    setBusySlugs((prev) => {
      const next = new Set(prev);
      if (busy) next.add(slug);
      else next.delete(slug);
      return next;
    });
  }

  async function stageAndContinue(item: AddonSummary) {
    setActionError('');
    setActionNotice('');
    if (!item.staged.present && !item.staged.staging && canManage) {
      setBusy(item.slug, true);
      try {
        await api.stageAddon(item.slug);
      } catch (err) {
        reportError(err, onSessionLost, setActionError);
        setBusy(item.slug, false);
        return;
      }
      setBusy(item.slug, false);
    }
    void navigate(`/system/addons/${encodeURIComponent(item.slug)}`);
  }

  async function unstageImage(item: AddonSummary) {
    setActionError('');
    setActionNotice('');
    setBusy(item.slug, true);
    try {
      await api.unstageAddon(item.slug);
      setActionNotice(`Removed staged image for ${item.name}.`);
      await resource.reload();
    } catch (err) {
      reportError(err, onSessionLost, setActionError);
    } finally {
      setBusy(item.slug, false);
    }
  }

  async function openUninstall(item: AddonSummary) {
    setActionError('');
    setActionNotice('');
    setBusy(item.slug, true);
    try {
      const saved = await api.addonConfig(item.id);
      setUninstallTarget({ item, version: saved.version });
    } catch (err) {
      reportError(err, onSessionLost, setActionError);
    } finally {
      setBusy(item.slug, false);
    }
  }

  async function confirmUninstall() {
    if (!uninstallTarget) return;
    setUninstalling(true);
    setActionError('');
    setActionNotice('');
    try {
      await api.deleteAddonConfig(uninstallTarget.item.id, uninstallTarget.version);
      const removedName = uninstallTarget.item.name;
      setUninstallTarget(null);
      setActionNotice(`Removed ${removedName} from saved configuration. Open Review and apply to stop and remove it from the running system.`);
      onSaved();
      await resource.reload();
    } catch (err) {
      setUninstallTarget(null);
      reportError(err, onSessionLost, setActionError);
    } finally {
      setUninstalling(false);
    }
  }

  const safeMode = resource.data?.safeMode;

  return (
    <Stack>
      <Group justify="space-between" align="flex-end" wrap="wrap">
        <div>
          <Title order={2}>Add-ons</Title>
          <Text c="dimmed">Curated, digest-pinned appliance add-ons shipped with this Andaru image.</Text>
        </div>
        <Button size="xs" variant="default" loading={resource.loading && resource.data !== undefined} onClick={() => void resource.reload()}>
          Refresh
        </Button>
      </Group>

      {safeMode?.active && (() => {
        const reasons = formatSafeModeReasons(safeMode);
        return (
          <Alert color="orange" role="status" title="Safe mode active">
            Add-on containers are held disabled{reasons ? ` (${reasons})` : ''}. Lifecycle controls are unavailable until safe mode clears.
          </Alert>
        );
      })()}

      {actionError && <Alert color="red" role="alert" title="Add-on action failed">{actionError}</Alert>}
      {actionNotice && <Alert role="status">{actionNotice}</Alert>}

      <Loaded resource={resource} what="add-ons">
        {(data) => data.items.length === 0 ? (
          <Alert role="status">No add-ons are shipped in this image.</Alert>
        ) : (
          <Stack gap="sm">
            {data.items.map((item) => {
              const busy = busySlugs.has(item.slug);
              const enabled = item.saved?.enabled;
              return (
                <Card key={item.id} withBorder padding="md" className="addon-card">
                  <Stack gap="xs">
                    <Group justify="space-between" align="flex-start" wrap="wrap" gap="sm">
                      <Stack gap={4} className="addon-card-head">
                        <Group gap="xs" wrap="wrap">
                          <Title order={3} size="h4">{item.name}</Title>
                          <Text size="xs" c="dimmed" className="tabular">
                            {item.application.name} {item.application.version} (manifest {item.version})
                          </Text>
                        </Group>
                        {item.description && <AddonMarkdown source={item.description} />}
                      </Stack>

                      <Group gap="xs" wrap="wrap">
                        {item.installed ? (
                          <Badge color={enabled === false ? 'gray' : 'blue'} variant="light">
                            {enabled === undefined ? 'Installed' : enabled ? 'Enabled' : 'Disabled'}
                          </Badge>
                        ) : (
                          <Badge color="gray" variant="outline">Not installed</Badge>
                        )}
                        {item.runtime.applied && (
                          <Badge color={healthBadgeColor[item.runtime.health] ?? 'gray'} variant="light">
                            {healthLabel(item.runtime.health)}
                          </Badge>
                        )}
                        {item.runtime.updateAvailable && (
                          <Badge color="yellow" variant="filled">Update available</Badge>
                        )}
                        {item.unreviewedBusCount > 0 && (
                          <Badge color="orange" variant="light">
                            {plural(item.unreviewedBusCount, 'bus permission')} to review
                          </Badge>
                        )}
                        <Badge color={item.staged.staging ? 'blue' : item.staged.present ? 'teal' : 'gray'} variant="dot">
                          {item.staged.staging ? 'Staging image…' : item.staged.present ? 'Image staged' : 'Not staged'}
                        </Badge>
                      </Group>
                    </Group>

                    <Group justify="space-between" align="center" wrap="wrap" gap="xs">
                      <Text size="xs" c="dimmed" className="addon-digest-line">
                        {item.catalogImage}
                      </Text>

                      <Group gap="xs" wrap="wrap">
                        {!item.installed && canWriteConfig && (
                          <Button
                            size="xs"
                            loading={busy}
                            disabled={!item.staged.present && !item.staged.staging && !canManage}
                            onClick={() => void stageAndContinue(item)}
                          >
                            {item.staged.present || item.staged.staging ? 'Install' : 'Stage image and continue'}
                          </Button>
                        )}
                        {!item.installed && !canWriteConfig && canManage && !item.staged.present && !item.staged.staging && (
                          <Button size="xs" variant="default" loading={busy} onClick={() => void stageAndContinue(item)}>
                            Stage image
                          </Button>
                        )}
                        {!item.installed && !item.runtime.applied && item.staged.present && !item.staged.staging && canManage && (
                          <Button size="xs" variant="subtle" color="gray" loading={busy} onClick={() => void unstageImage(item)}>
                            Unstage image
                          </Button>
                        )}
                        {item.installed && item.runtime.updateAvailable && canManage && (
                          <Button size="xs" color="yellow" variant="light" loading={busy} onClick={() => void stageAndContinue(item)}>
                            {item.staged.present || item.staged.staging ? 'Review update' : 'Update'}
                          </Button>
                        )}
                        <Button
                          size="xs"
                          variant="default"
                          component={Link}
                          to={`/system/addons/${encodeURIComponent(item.slug)}`}
                        >
                          {item.installed && canReadConfig ? 'Settings' : 'Details'}
                        </Button>
                        {item.installed && canWriteConfig && canReadConfig && (
                          <Button
                            size="xs"
                            variant="subtle"
                            color="red"
                            loading={busy}
                            onClick={() => void openUninstall(item)}
                          >
                            Uninstall
                          </Button>
                        )}
                      </Group>
                    </Group>
                  </Stack>
                </Card>
              );
            })}
          </Stack>
        )}
      </Loaded>

      <UninstallAddonModal
        opened={uninstallTarget !== null}
        name={uninstallTarget?.item.name ?? ''}
        loading={uninstalling}
        onClose={() => setUninstallTarget(null)}
        onConfirm={() => void confirmUninstall()}
      />
    </Stack>
  );
}
