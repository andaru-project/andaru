import {
  Alert,
  Anchor,
  Badge,
  Button,
  Card,
  Divider,
  Group,
  Modal,
  Select,
  Stack,
  Switch,
  Text,
  Title,
} from '@mantine/core';
import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react';
import { Link, useParams } from 'react-router';
import { AddonMarkdown } from '../addonMarkdown';
import {
  addonText,
  buildSettingsPayload,
  initialPropertyDraft,
  normalizeHostname,
  safeExternalHttpUrl,
  type AddonPropertyDraft,
} from '../addons';
import { ApiClient, fieldErrors, reportError, saveFailure } from '../api';
import { formatSafeModeReasons } from './SystemWidget';
import { AddonPropertyForm } from './AddonPropertyForm';
import type {
  AddonBusGrantDecision,
  AddonDetailResponse,
  AddonSavedBusGrants,
  AddonSavedEntry,
  AddonSavedInterface,
  SavedObject,
  Session,
  Zone,
} from '../types';
import { useResource } from '../useResource';
import { Loaded } from './Loaded';

interface AddonDetailScreenProps {
  api: ApiClient;
  session: Session;
  onSaved(): void;
  onSessionLost(): void;
}

interface LoadedAddonDetail {
  detail: AddonDetailResponse;
  savedObject?: SavedObject<AddonSavedEntry>;
  zones: Zone[];
}

const healthBadgeColor: Record<string, string> = {
  healthy: 'green',
  starting: 'blue',
  stopped: 'gray',
  held: 'orange',
  unhealthy: 'red',
  none: 'gray',
  unknown: 'gray',
};

const busGrantOptions = [
  { value: 'unreviewed', label: 'Unreviewed (denied)' },
  { value: 'granted', label: 'Granted' },
  { value: 'denied', label: 'Denied' },
];

const logLineOptions = [
  { value: '50', label: '50 lines' },
  { value: '200', label: '200 lines' },
  { value: '500', label: '500 lines' },
  { value: '1000', label: '1000 lines' },
];

export function AddonDetailScreen({ api, session, onSaved, onSessionLost }: AddonDetailScreenProps) {
  const { slug = '' } = useParams();
  const capabilities = useMemo(() => new Set(session.capabilities), [session.capabilities]);
  const canManage = capabilities.has('addons.manage');
  const canReadConfig = capabilities.has('configuration.read');
  const canWriteConfig = capabilities.has('configuration.write');

  const load = useCallback(async (): Promise<LoadedAddonDetail> => {
    const detail = await api.addon(slug);
    if (!canReadConfig) {
      return { detail, zones: [] };
    }
    const [zonesList, savedObject] = await Promise.all([
      api.zones(),
      detail.installed ? api.addonConfig(detail.id) : Promise.resolve(undefined),
    ]);
    return {
      detail,
      savedObject,
      zones: zonesList.items.map((item) => item.value),
    };
  }, [api, slug, canReadConfig]);

  const resource = useResource(load, onSessionLost);
  const loaded = resource.data;
  const detail = loaded?.detail;
  const manifest = detail?.manifest;

  const [enabled, setEnabled] = useState(true);
  const [propDraft, setPropDraft] = useState<AddonPropertyDraft>(() => initialPropertyDraft([], undefined));
  const [ifaceZones, setIfaceZones] = useState<Record<string, string>>({});
  const [pubGrants, setPubGrants] = useState<Record<string, string>>({});
  const [subGrants, setSubGrants] = useState<Record<string, string>>({});

  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState('');
  const [saveNotice, setSaveNotice] = useState('');
  const [propErrors, setPropErrors] = useState<Map<string, string>>(() => new Map());

  const [lifecycleBusy, setLifecycleBusy] = useState<string | null>(null);
  const [lifecycleError, setLifecycleError] = useState('');
  const [lifecycleNotice, setLifecycleNotice] = useState('');

  const [uninstallOpen, setUninstallOpen] = useState(false);
  const [uninstalling, setUninstalling] = useState(false);

  const [selectedLog, setSelectedLog] = useState('');
  const [logLinesCount, setLogLinesCount] = useState('200');

  const initializedKey = useRef('');
  useEffect(() => {
    if (!loaded || !manifest) return;
    const saved = loaded.savedObject?.value ?? loaded.detail.saved;
    const key = `${manifest.id}:${loaded.savedObject?.version ?? 'new'}:${loaded.detail.installed ? '1' : '0'}`;
    if (initializedKey.current === key) return;
    initializedKey.current = key;

    setEnabled(loaded.detail.installed ? Boolean(saved?.enabled) : true);
    setPropDraft(initialPropertyDraft(manifest.properties, saved?.settings));

    const nextIfaces: Record<string, string> = {};
    for (const spec of manifest.interfaces ?? []) {
      const existing = saved?.interfaces?.find((item) => item.name === spec.name);
      nextIfaces[spec.name] = existing?.zone ?? '';
    }
    setIfaceZones(nextIfaces);

    const nextPub: Record<string, string> = {};
    for (const spec of manifest.bus?.publish ?? []) {
      nextPub[spec.topic] = saved?.busGrants?.publish?.[spec.topic] ?? 'unreviewed';
    }
    setPubGrants(nextPub);

    const nextSub: Record<string, string> = {};
    for (const spec of manifest.bus?.subscribe ?? []) {
      nextSub[spec.topic] = saved?.busGrants?.subscribe?.[spec.topic] ?? 'unreviewed';
    }
    setSubGrants(nextSub);
    setPropErrors(new Map());
    setSaveError('');
  }, [loaded, manifest]);

  const activeLog = useMemo(() => {
    if (selectedLog && manifest?.logs?.some((l) => l.name === selectedLog)) return selectedLog;
    return manifest?.logs?.[0]?.name ?? '';
  }, [selectedLog, manifest]);

  const loadLogs = useCallback(
    () => (activeLog && slug ? api.addonLogs(slug, activeLog, { lines: Number(logLinesCount) || 200 }) : Promise.resolve(undefined)),
    [api, slug, activeLog, logLinesCount],
  );
  const logsResource = useResource(loadLogs, onSessionLost);

  async function runStage() {
    setLifecycleError('');
    setLifecycleNotice('');
    setLifecycleBusy('stage');
    try {
      await api.stageAddon(slug);
      setLifecycleNotice('Container image pulled and digest verified.');
      await resource.reload();
    } catch (err) {
      reportError(err, onSessionLost, setLifecycleError);
    } finally {
      setLifecycleBusy(null);
    }
  }

  async function runUnstage() {
    setLifecycleError('');
    setLifecycleNotice('');
    setLifecycleBusy('unstage');
    try {
      await api.unstageAddon(slug);
      setLifecycleNotice('Staged container image removed.');
      await resource.reload();
    } catch (err) {
      reportError(err, onSessionLost, setLifecycleError);
    } finally {
      setLifecycleBusy(null);
    }
  }

  async function runControl(action: 'start' | 'stop' | 'restart') {
    setLifecycleError('');
    setLifecycleNotice('');
    setLifecycleBusy(action);
    try {
      await api.controlAddon(slug, action);
      setLifecycleNotice(`Add-on ${action} completed.`);
      await resource.reload();
    } catch (err) {
      reportError(err, onSessionLost, setLifecycleError);
    } finally {
      setLifecycleBusy(null);
    }
  }

  async function handleSave(event: FormEvent) {
    event.preventDefault();
    if (!detail || !manifest || !canWriteConfig) return;
    setSaving(true);
    setSaveError('');
    setSaveNotice('');
    setPropErrors(new Map());

    const settings = buildSettingsPayload(manifest.properties, propDraft);

    const interfaces: AddonSavedInterface[] = [];
    for (const spec of manifest.interfaces ?? []) {
      const zone = (ifaceZones[spec.name] ?? '').trim();
      if (zone) interfaces.push({ name: spec.name, zone });
    }

    const publish: Record<string, AddonBusGrantDecision> = {};
    for (const spec of manifest.bus?.publish ?? []) {
      const decision = pubGrants[spec.topic];
      if (decision === 'granted' || decision === 'denied') publish[spec.topic] = decision;
    }
    const subscribe: Record<string, AddonBusGrantDecision> = {};
    for (const spec of manifest.bus?.subscribe ?? []) {
      const decision = subGrants[spec.topic];
      if (decision === 'granted' || decision === 'denied') subscribe[spec.topic] = decision;
    }
    const busGrants: AddonSavedBusGrants | undefined =
      Object.keys(publish).length > 0 || Object.keys(subscribe).length > 0
        ? {
            ...(Object.keys(publish).length > 0 ? { publish } : {}),
            ...(Object.keys(subscribe).length > 0 ? { subscribe } : {}),
          }
        : undefined;

    const entry: AddonSavedEntry = {
      id: manifest.id,
      enabled,
      ...(settings ? { settings } : {}),
      ...(interfaces.length > 0 ? { interfaces } : {}),
      ...(busGrants ? { busGrants } : {}),
    };

    try {
      if (loaded?.savedObject) {
        await api.replaceAddonConfig(manifest.id, entry, loaded.savedObject.version);
        setSaveNotice('Saved add-on configuration.');
      } else {
        await api.createAddonConfig(entry);
        setSaveNotice('Add-on added to saved configuration.');
      }
      initializedKey.current = '';
      onSaved();
      await resource.reload();
    } catch (err) {
      const mapped = fieldErrors(err, (path) => {
        const match = /^\/(?:addons\/\d+\/)?settings\/([^/]+)/.exec(path);
        return match ? match[1] : '';
      });
      setPropErrors(mapped);
      setSaveError(saveFailure(err, mapped, 'add-on'));
    } finally {
      setSaving(false);
    }
  }

  async function confirmUninstall() {
    if (!loaded?.savedObject || !manifest) return;
    setUninstalling(true);
    setSaveError('');
    setSaveNotice('');
    try {
      await api.deleteAddonConfig(manifest.id, loaded.savedObject.version);
      setUninstallOpen(false);
      initializedKey.current = '';
      setSaveNotice('Removed add-on from saved configuration. Open Review and apply to stop and remove it.');
      onSaved();
      await resource.reload();
    } catch (err) {
      reportError(err, onSessionLost, setSaveError);
    } finally {
      setUninstalling(false);
    }
  }

  const webuiState = useMemo(() => {
    const webui = detail?.webui;
    if (!webui) return null;
    if (webui.originConflict) {
      return {
        suppressed: true,
        reason: webui.conflictReason || 'The configured add-on web interface hostname matches an Andaru management origin. Configure a separate hostname to open it.',
        title: addonText(webui.title) || 'Web interface',
        url: null,
      };
    }
    const safeUrl = webui.url ? safeExternalHttpUrl(webui.url) : null;
    if (!safeUrl) {
      return {
        suppressed: false,
        reason: '',
        title: addonText(webui.title) || 'Web interface',
        url: null,
      };
    }
    try {
      const targetHost = normalizeHostname(new URL(safeUrl).hostname);
      const currentHost = normalizeHostname(window.location.hostname);
      if (targetHost && currentHost && targetHost === currentHost) {
        return {
          suppressed: true,
          reason: 'This add-on web interface shares the current Andaru management hostname and has been suppressed for browser origin isolation.',
          title: addonText(webui.title) || 'Web interface',
          url: null,
        };
      }
    } catch {
      return null;
    }
    return {
      suppressed: false,
      reason: '',
      title: addonText(webui.title) || 'Web interface',
      url: safeUrl,
    };
  }, [detail?.webui]);

  const zoneOptions = useMemo(
    () => (loaded?.zones ?? []).map((z) => ({ value: z.id, label: z.name })),
    [loaded?.zones],
  );

  return (
    <Stack>
      <Group justify="space-between" align="center" wrap="wrap">
        <Anchor component={Link} to="/system/addons" size="sm">
          ← Back to Add-ons
        </Anchor>
        <Button size="xs" variant="default" loading={resource.loading && detail !== undefined} onClick={() => void resource.reload()}>
          Refresh
        </Button>
      </Group>

      <Loaded resource={resource} what="add-on details">
        {() => detail && manifest && (
          <Stack gap="md">
            <div>
              <Group gap="xs" wrap="wrap" align="baseline">
                <Title order={2}>{detail.name}</Title>
                <Text size="sm" c="dimmed" className="tabular">
                  {detail.application.name} {detail.application.version} · manifest {detail.version}
                </Text>
              </Group>
              {detail.description && <AddonMarkdown source={detail.description} />}
            </div>

            {detail.safeMode.active && (() => {
              const reasons = formatSafeModeReasons(detail.safeMode);
              return (
                <Alert color="orange" role="status" title="Safe mode active">
                  Add-on containers are held disabled{reasons ? ` (${reasons})` : ''}. Runtime controls are unavailable until safe mode clears.
                </Alert>
              );
            })()}

            <Card withBorder padding="md">
              <Stack gap="sm">
                <Group justify="space-between" align="center" wrap="wrap">
                  <Title order={3} size="h4">Runtime &amp; image</Title>
                  <Group gap="xs" wrap="wrap">
                    <Badge color={healthBadgeColor[detail.runtime.health] ?? 'gray'} variant="light">
                      {detail.runtime.health}
                    </Badge>
                    <Badge color="gray" variant="outline">
                      unit: {detail.runtime.activeState}{detail.runtime.subState ? ` (${detail.runtime.subState})` : ''}
                    </Badge>
                    <Badge color={detail.staged.present ? 'teal' : 'gray'} variant="dot">
                      {detail.staged.present ? 'Image staged' : 'Image not staged'}
                    </Badge>
                    {detail.runtime.updateAvailable && (
                      <Badge color="yellow" variant="filled">Update available</Badge>
                    )}
                  </Group>
                </Group>

                <Text size="xs" c="dimmed" className="addon-digest-line">
                  Catalog image: {detail.catalogImage}
                </Text>
                {detail.runtime.appliedImage && (
                  <Text size="xs" c="dimmed" className="addon-digest-line">
                    Applied image: {detail.runtime.appliedImage}
                  </Text>
                )}

                {detail.runtime.updateAvailable && (
                  <Alert color="yellow" role="status" title="Newer image available in catalog">
                    Stage the new catalog digest and apply configuration changes in <Anchor component={Link} to="/changes">Review and apply</Anchor> to update this add-on.
                  </Alert>
                )}

                {lifecycleError && <Alert color="red" role="alert" title="Lifecycle action failed">{lifecycleError}</Alert>}
                {lifecycleNotice && <Alert role="status">{lifecycleNotice}</Alert>}

                {canManage && (
                  <Group gap="xs" wrap="wrap">
                    {!detail.staged.present && (
                      <Button size="xs" loading={lifecycleBusy === 'stage'} onClick={() => void runStage()}>
                        Stage image
                      </Button>
                    )}
                    {!detail.installed && !detail.runtime.applied && detail.staged.present && (
                      <Button size="xs" variant="default" loading={lifecycleBusy === 'unstage'} onClick={() => void runUnstage()}>
                        Unstage image
                      </Button>
                    )}
                    <Button
                      size="xs"
                      variant="default"
                      loading={lifecycleBusy === 'start'}
                      disabled={!detail.runtime.applied || detail.safeMode.active || lifecycleBusy !== null}
                      onClick={() => void runControl('start')}
                    >
                      Start
                    </Button>
                    <Button
                      size="xs"
                      variant="default"
                      loading={lifecycleBusy === 'stop'}
                      disabled={!detail.runtime.applied || detail.safeMode.active || lifecycleBusy !== null}
                      onClick={() => void runControl('stop')}
                    >
                      Stop
                    </Button>
                    <Button
                      size="xs"
                      variant="default"
                      loading={lifecycleBusy === 'restart'}
                      disabled={!detail.runtime.applied || detail.safeMode.active || lifecycleBusy !== null}
                      onClick={() => void runControl('restart')}
                    >
                      Restart
                    </Button>
                  </Group>
                )}
              </Stack>
            </Card>

            {webuiState && (
              <Card withBorder padding="md">
                <Stack gap="xs">
                  <Title order={3} size="h4">{webuiState.title}</Title>
                  {webuiState.suppressed ? (
                    <Alert color="orange" role="alert" title="Separate origin required">
                      {webuiState.reason}
                    </Alert>
                  ) : webuiState.url ? (
                    <Stack gap={4}>
                      <Group gap="xs" wrap="wrap">
                        <Anchor href={webuiState.url} target="_blank" rel="noopener noreferrer">
                          Open {webuiState.title} ({webuiState.url})
                        </Anchor>
                      </Group>
                      <Text size="xs" c="dimmed">
                        Opens in a new tab on a separate origin with this add-on&apos;s own authentication.
                      </Text>
                    </Stack>
                  ) : (
                    <Text size="sm" c="dimmed">
                      Configure this add-on&apos;s web interface hostname and save to enable the external link.
                    </Text>
                  )}
                </Stack>
              </Card>
            )}

            {canReadConfig && (
              <Card withBorder padding="md">
                <form onSubmit={(event) => void handleSave(event)}>
                  <Stack gap="md">
                    <Group justify="space-between" align="center" wrap="wrap">
                      <Title order={3} size="h4">Configuration</Title>
                      {detail.installed && canWriteConfig && (
                        <Button size="xs" variant="subtle" color="red" type="button" onClick={() => setUninstallOpen(true)}>
                          Uninstall
                        </Button>
                      )}
                    </Group>

                    {!detail.staged.present && !detail.installed && (
                      <Alert color="blue" role="status" title="Image not yet staged">
                        Stage the pinned container image before applying so image download completes outside the apply rollback window.
                      </Alert>
                    )}

                    {saveError && <Alert color="red" role="alert" title="Save failed">{saveError}</Alert>}
                    {saveNotice && (
                      <Alert role="status">
                        {saveNotice} <Anchor component={Link} to="/changes">Review and apply</Anchor>
                      </Alert>
                    )}

                    <Switch
                      label="Enable add-on"
                      description="Enabled add-ons are realized as systemd containers when you apply changes."
                      checked={enabled}
                      disabled={!canWriteConfig}
                      onChange={(event) => setEnabled(event.currentTarget.checked)}
                    />

                    {(manifest.properties?.length ?? 0) > 0 && (
                      <>
                        <Divider label="Settings" labelPosition="left" />
                        <AddonPropertyForm
                          properties={manifest.properties ?? []}
                          draft={propDraft}
                          zones={loaded?.zones ?? []}
                          readOnly={!canWriteConfig}
                          errors={propErrors}
                          onChange={setPropDraft}
                        />
                      </>
                    )}

                    {(manifest.interfaces?.length ?? 0) > 0 && (
                      <>
                        <Divider label="Interfaces" labelPosition="left" />
                        <Stack gap="sm">
                          {(manifest.interfaces ?? []).map((spec) => (
                            <Select
                              key={spec.name}
                              label={`${addonText(spec.title) || spec.name} (${spec.name})`}
                              description={spec.zoneRequired ? 'A firewall zone is required before enabling this add-on.' : 'Optional firewall zone assignment.'}
                              required={Boolean(spec.zoneRequired && enabled)}
                              disabled={!canWriteConfig}
                              data={zoneOptions}
                              value={ifaceZones[spec.name] || null}
                              clearable={!spec.zoneRequired}
                              allowDeselect={!spec.zoneRequired}
                              placeholder={zoneOptions.length > 0 ? 'Select a zone' : 'No zones configured'}
                              onChange={(val) => setIfaceZones((prev) => ({ ...prev, [spec.name]: val ?? '' }))}
                            />
                          ))}
                        </Stack>
                      </>
                    )}

                    {manifest.bus && ((manifest.bus.publish?.length ?? 0) > 0 || (manifest.bus.subscribe?.length ?? 0) > 0) && (
                      <>
                        <Divider label="Event bus permissions" labelPosition="left" />
                        <Text size="xs" c="dimmed">
                          Unreviewed topics are denied by default. Grant or deny each requested topic explicitly.
                        </Text>
                        <Stack gap="sm">
                          {(manifest.bus.publish ?? []).map((spec) => {
                            const decision = pubGrants[spec.topic] ?? 'unreviewed';
                            return (
                              <Card key={`pub:${spec.topic}`} withBorder padding="sm">
                                <Stack gap={6}>
                                  <Group justify="space-between" align="center" wrap="wrap" gap="xs">
                                    <Group gap="xs" wrap="wrap">
                                      <Badge size="xs" color="blue" variant="light">Publish</Badge>
                                      <Text size="sm" fw={600}>{addonText(spec.title) || spec.topic}</Text>
                                      <Text size="xs" c="dimmed" className="tabular">{spec.topic}</Text>
                                    </Group>
                                    {decision === 'unreviewed' && (
                                      <Badge size="xs" color="orange" variant="light">Needs review</Badge>
                                    )}
                                  </Group>
                                  {spec.description && <AddonMarkdown source={spec.description} />}
                                  <Select
                                    aria-label={`Publish permission for ${spec.topic}`}
                                    disabled={!canWriteConfig}
                                    data={busGrantOptions}
                                    value={decision}
                                    allowDeselect={false}
                                    onChange={(val) => setPubGrants((prev) => ({ ...prev, [spec.topic]: val ?? 'unreviewed' }))}
                                  />
                                </Stack>
                              </Card>
                            );
                          })}

                          {(manifest.bus.subscribe ?? []).map((spec) => {
                            const decision = subGrants[spec.topic] ?? 'unreviewed';
                            return (
                              <Card key={`sub:${spec.topic}`} withBorder padding="sm">
                                <Stack gap={6}>
                                  <Group justify="space-between" align="center" wrap="wrap" gap="xs">
                                    <Group gap="xs" wrap="wrap">
                                      <Badge size="xs" color="grape" variant="light">Subscribe</Badge>
                                      <Text size="sm" fw={600}>{addonText(spec.title) || spec.topic}</Text>
                                      <Text size="xs" c="dimmed" className="tabular">{spec.topic}</Text>
                                    </Group>
                                    {decision === 'unreviewed' && (
                                      <Badge size="xs" color="orange" variant="light">Needs review</Badge>
                                    )}
                                  </Group>
                                  {spec.description && <AddonMarkdown source={spec.description} />}
                                  <Select
                                    aria-label={`Subscribe permission for ${spec.topic}`}
                                    disabled={!canWriteConfig}
                                    data={busGrantOptions}
                                    value={decision}
                                    allowDeselect={false}
                                    onChange={(val) => setSubGrants((prev) => ({ ...prev, [spec.topic]: val ?? 'unreviewed' }))}
                                  />
                                </Stack>
                              </Card>
                            );
                          })}
                        </Stack>
                      </>
                    )}

                    {canWriteConfig && (
                      <Group justify="flex-end" gap="xs">
                        <Button type="submit" loading={saving}>
                          {detail.installed ? 'Save settings' : 'Install and save'}
                        </Button>
                      </Group>
                    )}
                  </Stack>
                </form>
              </Card>
            )}

            {(manifest.logs?.length ?? 0) > 0 && (
              <Card withBorder padding="md">
                <Stack gap="sm">
                  <Group justify="space-between" align="flex-end" wrap="wrap" gap="xs">
                    <Title order={3} size="h4">Logs</Title>
                    <Group gap="xs" wrap="wrap">
                      <Select
                        aria-label="Log stream"
                        size="xs"
                        data={(manifest.logs ?? []).map((l) => ({ value: l.name, label: addonText(l.title) || l.name }))}
                        value={activeLog || null}
                        allowDeselect={false}
                        onChange={(val) => { if (val) setSelectedLog(val); }}
                      />
                      <Select
                        aria-label="Log lines"
                        size="xs"
                        data={logLineOptions}
                        value={logLinesCount}
                        allowDeselect={false}
                        onChange={(val) => { if (val) setLogLinesCount(val); }}
                      />
                      <Button size="xs" variant="default" loading={logsResource.loading} onClick={() => void logsResource.reload()}>
                        Refresh logs
                      </Button>
                    </Group>
                  </Group>

                  {logsResource.error && <Alert color="red" role="alert" title="Could not load logs">{logsResource.error}</Alert>}
                  {logsResource.data?.truncated && (
                    <Text size="xs" c="dimmed">Showing the most recent bounded log output.</Text>
                  )}
                  <pre className="addon-log-view">
                    {logsResource.data && logsResource.data.lines.length > 0
                      ? logsResource.data.lines.join('\n')
                      : logsResource.loading
                        ? 'Loading logs…'
                        : 'No log entries.'}
                  </pre>
                </Stack>
              </Card>
            )}
          </Stack>
        )}
      </Loaded>

      <Modal
        opened={uninstallOpen}
        onClose={() => { if (!uninstalling) setUninstallOpen(false); }}
        title={detail ? `Uninstall ${detail.name}` : 'Uninstall add-on'}
        centered
        lockScroll={false}
        closeButtonProps={{ 'aria-label': 'Close' }}
      >
        <Stack>
          <Text size="sm">
            Uninstalling removes <strong>{detail?.name}</strong> and its saved configuration from the appliance declaration.
          </Text>
          <Text size="sm" c="dimmed">
            The running add-on unit is only stopped and removed when you apply changes in <strong>Review and apply</strong>, protected by Andaru&apos;s automatic rollback timer until confirmed.
          </Text>
          <Group justify="flex-end" gap="xs">
            <Button variant="default" disabled={uninstalling} onClick={() => setUninstallOpen(false)}>Cancel</Button>
            <Button color="red" loading={uninstalling} onClick={() => void confirmUninstall()}>Uninstall</Button>
          </Group>
        </Stack>
      </Modal>
    </Stack>
  );
}
