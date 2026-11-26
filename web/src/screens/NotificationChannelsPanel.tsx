import {
  Alert,
  Badge,
  Button,
  Checkbox,
  Drawer,
  Fieldset,
  Group,
  Modal,
  PasswordInput,
  ScrollArea,
  Select,
  Stack,
  Switch,
  Table,
  TagsInput,
  Text,
  TextInput,
  VisuallyHidden,
} from '@mantine/core';
import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react';
import { ApiClient, ApiError, apiErrorMessage, saveFailure } from '../api';
import {
  buildChannelWrite,
  channelDraftOf,
  channelFieldErrors,
  channelKindLabel,
  existingChannelSecretsOf,
  knownNotificationSources,
  validateChannelDraft,
  type NotificationChannelDraft,
} from '../notificationChannels';
import { severityColor } from '../notifications';
import type {
  NotificationChannel,
  NotificationChannelKind,
  NotificationChannelWrite,
  NotificationSeverity,
  SavedObject,
  Snapshot,
} from '../types';
import { useResource } from '../useResource';
import { Loaded } from './Loaded';

interface NotificationChannelsPanelProps {
  api: ApiClient;
  canManage: boolean;
  onSaved?(): void;
  onSessionLost(): void;
}

interface EditorState {
  saved?: SavedObject<NotificationChannel>;
  existingNames: string[];
  existingSecrets: string[];
}

const kindOptions: Array<{ value: NotificationChannelKind; label: string }> = [
  { value: 'webhook', label: 'Webhook (HTTP POST)' },
];

const severityOptions: Array<{ value: NotificationSeverity; label: string }> = [
  { value: 'info', label: 'Info (all severities)' },
  { value: 'warning', label: 'Warning and critical' },
  { value: 'critical', label: 'Critical only' },
];

function severitySummary(minSeverity: NotificationSeverity | undefined): { severity: NotificationSeverity; label: string } {
  const sev = minSeverity ?? 'info';
  if (sev === 'critical') return { severity: sev, label: 'critical only' };
  return { severity: sev, label: `${sev}+` };
}

function NotificationChannelEditor({
  saved,
  existingNames,
  existingSecrets,
  onSave,
  onClose,
  onRefresh,
  onSessionLost,
}: {
  saved?: SavedObject<NotificationChannel>;
  existingNames: string[];
  existingSecrets: string[];
  onSave(value: NotificationChannelWrite, saved?: SavedObject<NotificationChannel>): Promise<void>;
  onClose(): void;
  onRefresh(): void;
  onSessionLost(): void;
}) {
  const [draft, setDraft] = useState<NotificationChannelDraft>(() => channelDraftOf(saved?.value));
  const [errors, setErrors] = useState<Map<string, string>>(new Map());
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);
  const [errorFocusTick, setErrorFocusTick] = useState(0);
  const submittingRef = useRef(false);
  const openRef = useRef(true);
  const formRef = useRef<HTMLFormElement>(null);
  const failureRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    openRef.current = true;
    return () => { openRef.current = false; };
  }, []);

  useEffect(() => {
    if (!failure) return;
    failureRef.current?.scrollIntoView?.({ block: 'nearest' });
    if (errors.size === 0 || errors.has('')) failureRef.current?.focus?.();
  }, [failure, errors]);

  useEffect(() => {
    if (errorFocusTick === 0) return;
    const invalid = formRef.current?.querySelector<HTMLElement>('[aria-invalid="true"]:not(:disabled)');
    if (invalid) {
      invalid.scrollIntoView?.({ block: 'nearest' });
      invalid.focus?.();
    } else {
      failureRef.current?.scrollIntoView?.({ block: 'nearest' });
      failureRef.current?.focus?.();
    }
  }, [errorFocusTick]);

  const existingUrl = saved?.value.webhook?.url;
  const existingAuth = saved?.value.webhook?.authorization;

  function patch(update: Partial<NotificationChannelDraft>) {
    setDraft((cur) => ({ ...cur, ...update }));
  }

  function clearFieldError(field: string) {
    setErrors((cur) => {
      if (!cur.has(field)) return cur;
      const next = new Map(cur);
      next.delete(field);
      return next;
    });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (submittingRef.current) return;
    setFailure('');
    const localErrors = validateChannelDraft(draft, saved?.value, existingNames);
    if (localErrors.size > 0) {
      setErrors(localErrors);
      setErrorFocusTick((t) => t + 1);
      return;
    }

    submittingRef.current = true;
    setBusy(true);
    setErrors(new Map());
    const payload = buildChannelWrite(draft, saved?.value, existingSecrets);
    try {
      await onSave(payload, saved);
      if (openRef.current) onClose();
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        onSessionLost();
        return;
      }
      onRefresh();
      if (!openRef.current) return;
      const fields = channelFieldErrors(error);
      setErrors(fields);
      setFailure(saveFailure(error, fields, 'notification channel'));
      if (fields.size > 0 && !fields.has('')) setErrorFocusTick((t) => t + 1);
    } finally {
      submittingRef.current = false;
      if (openRef.current) setBusy(false);
    }
  }

  return (
    <Drawer
      opened
      onClose={() => { if (!submittingRef.current) onClose(); }}
      closeOnClickOutside={!busy}
      closeOnEscape={!busy}
      position="right"
      size="md"
      title={saved ? `Edit channel: ${saved.value.name}` : 'Add notification channel'}
      lockScroll={false}
      closeButtonProps={{ 'aria-label': 'Close', disabled: busy }}
    >
      <form ref={formRef} onSubmit={submit} noValidate>
        <Stack>
          {failure && (
            <Alert ref={failureRef} tabIndex={-1} color="red" role="alert" title="Notification channel was not saved">
              {failure}
            </Alert>
          )}
          <Fieldset variant="unstyled" disabled={busy}>
            <Stack>
              <TextInput
                data-autofocus
                label="Name"
                placeholder="ops-webhook"
                maxLength={128}
                required
                value={draft.name}
                error={errors.get('name')}
                onChange={(event) => {
                  patch({ name: event.currentTarget.value });
                  clearFieldError('name');
                }}
              />
              <TextInput
                label="Description"
                placeholder="Optional note about where this channel delivers"
                maxLength={4096}
                value={draft.description}
                error={errors.get('description')}
                onChange={(event) => {
                  patch({ description: event.currentTarget.value });
                  clearFieldError('description');
                }}
              />
              <Switch
                label="Enabled"
                description="Disabled channels keep their settings and secrets without delivering notifications."
                checked={draft.enabled}
                onChange={(event) => patch({ enabled: event.currentTarget.checked })}
              />
              <Select
                label="Channel kind"
                allowDeselect={false}
                data={kindOptions}
                value={draft.kind}
                error={errors.get('kind')}
                onChange={(value) => {
                  if (value === 'webhook') patch({ kind: value });
                  clearFieldError('kind');
                }}
              />
              <Select
                label="Minimum severity"
                description="Send notifications at or above this severity."
                allowDeselect={false}
                data={severityOptions}
                value={draft.minSeverity}
                error={errors.get('minSeverity')}
                onChange={(value) => {
                  if (value === 'info' || value === 'warning' || value === 'critical') patch({ minSeverity: value });
                  clearFieldError('minSeverity');
                }}
              />
              <TagsInput
                label="Sources"
                description="Leave empty to receive notifications from all sources."
                placeholder="All sources"
                data={knownNotificationSources}
                value={draft.sources}
                error={errors.get('sources')}
                clearable
                onChange={(value) => {
                  patch({ sources: value });
                  clearFieldError('sources');
                }}
              />
              {existingUrl?.unresolved && (
                <Alert color="orange" role="status" title="Webhook URL needs to be restored">
                  This channel&apos;s URL secret ({existingUrl.secret}) was left unresolved by a restore. Enter a new URL below to resolve it, or leave it blank to keep editing other fields.
                </Alert>
              )}
              <PasswordInput
                label="Webhook URL"
                description={
                  existingUrl
                    ? existingUrl.unresolved
                      ? `Unresolved secret (${existingUrl.secret}). Enter an http:// or https:// URL to resolve.`
                      : `Stored securely (${existingUrl.secret}). Leave blank to keep the current URL.`
                    : 'Must be an http:// or https:// URL with a host. Stored in the secret store and never shown again.'
                }
                placeholder={existingUrl && !existingUrl.unresolved ? 'Leave blank to keep existing URL' : 'https://hooks.example/services/...'}
                autoComplete="new-password"
                visibilityToggleFocusable
                visibilityToggleButtonProps={{ 'aria-label': 'Toggle webhook URL visibility' }}
                aria-invalid={errors.has('webhookUrl') || undefined}
                required={!existingUrl}
                value={draft.webhookUrl}
                error={errors.get('webhookUrl')}
                onChange={(event) => {
                  patch({ webhookUrl: event.currentTarget.value });
                  clearFieldError('webhookUrl');
                }}
              />
              {existingAuth?.unresolved && !draft.removeAuth && (
                <Alert color="orange" role="status" title="Authorization header needs to be restored">
                  The authorization secret ({existingAuth.secret}) was left unresolved by a restore. Enter a new value to resolve it, or remove it below.
                </Alert>
              )}
              <PasswordInput
                label="Authorization header"
                description={
                  existingAuth
                    ? existingAuth.unresolved
                      ? `Optional HTTP Authorization header (${existingAuth.secret}, unresolved). Enter a value to resolve.`
                      : `Optional HTTP Authorization header (${existingAuth.secret}). Leave blank to keep the current value.`
                    : 'Optional HTTP Authorization header value (for example, Bearer <token>). Stored in the secret store.'
                }
                placeholder={existingAuth && !existingAuth.unresolved ? 'Leave blank to keep existing header' : 'Bearer ...'}
                autoComplete="new-password"
                visibilityToggleFocusable
                visibilityToggleButtonProps={{ 'aria-label': 'Toggle authorization header visibility' }}
                aria-invalid={errors.has('webhookAuth') || undefined}
                disabled={draft.removeAuth}
                value={draft.webhookAuth}
                error={errors.get('webhookAuth')}
                onChange={(event) => {
                  patch({ webhookAuth: event.currentTarget.value });
                  clearFieldError('webhookAuth');
                }}
              />
              {existingAuth && (
                <Checkbox
                  label="Remove authorization header"
                  checked={draft.removeAuth}
                  onChange={(event) => {
                    const checked = event.currentTarget.checked;
                    patch({ removeAuth: checked, ...(checked ? { webhookAuth: '' } : {}) });
                    clearFieldError('webhookAuth');
                  }}
                />
              )}
              <Text size="xs" c="dimmed">
                Channel changes take effect when saved, without applying network configuration.
              </Text>
            </Stack>
          </Fieldset>
          <Group justify="flex-end" gap="xs" className="drawer-actions">
            <Button variant="default" disabled={busy} onClick={() => { if (!submittingRef.current) onClose(); }}>Cancel</Button>
            <Button type="submit" loading={busy}>{saved ? 'Save channel' : 'Add channel'}</Button>
          </Group>
        </Stack>
      </form>
    </Drawer>
  );
}

export function NotificationChannelsPanel({ api, canManage, onSaved, onSessionLost }: NotificationChannelsPanelProps) {
  const load = useCallback(() => api.notificationChannels(), [api]);
  const resource = useResource(load, onSessionLost);

  const [openingId, setOpeningId] = useState<string | null>(null);
  const [editor, setEditor] = useState<EditorState | null>(null);
  const [deleting, setDeleting] = useState<SavedObject<NotificationChannel> | null>(null);
  const [deletingBusy, setDeletingBusy] = useState(false);
  const [actionError, setActionError] = useState('');
  const openingRef = useRef(false);
  const deletingRef = useRef(false);
  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);

  function readOptionalSnapshot(): Promise<Snapshot | undefined> {
    return api.config().catch((error: unknown) => {
      if (error instanceof ApiError && error.status === 401) throw error;
      // Non-fatal: still avoid colliding with loaded channel secret names.
      return undefined;
    });
  }

  async function openCreate() {
    if (openingRef.current) return;
    openingRef.current = true;
    setOpeningId('new');
    setActionError('');
    try {
      const [list, snapshot] = await Promise.all([api.notificationChannels(), readOptionalSnapshot()]);
      const channels = list.items.map((item) => item.value);
      if (!mountedRef.current) return;
      setEditor({
        existingNames: channels.map((c) => c.name),
        existingSecrets: existingChannelSecretsOf(channels, snapshot),
      });
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        onSessionLost();
        return;
      }
      if (!mountedRef.current) return;
      setActionError(apiErrorMessage(error));
    } finally {
      openingRef.current = false;
      if (mountedRef.current) setOpeningId(null);
    }
  }

  async function openEdit(id: string) {
    if (openingRef.current) return;
    openingRef.current = true;
    setOpeningId(id);
    setActionError('');
    try {
      const [current, list, snapshot] = await Promise.all([
        api.notificationChannel(id),
        api.notificationChannels(),
        readOptionalSnapshot(),
      ]);
      const channels = list.items.map((item) => item.value);
      if (!mountedRef.current) return;
      setEditor({
        saved: current,
        existingNames: channels.map((c) => c.name),
        existingSecrets: existingChannelSecretsOf(channels, snapshot),
      });
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        onSessionLost();
        return;
      }
      if (!mountedRef.current) return;
      if (error instanceof ApiError && error.status === 404) {
        void resource.reload();
        setActionError('This notification channel was deleted elsewhere. The list has been refreshed.');
      } else {
        setActionError(apiErrorMessage(error));
      }
    } finally {
      openingRef.current = false;
      if (mountedRef.current) setOpeningId(null);
    }
  }

  async function saveChannel(value: NotificationChannelWrite, saved?: SavedObject<NotificationChannel>) {
    await api.saveNotificationChannel(value, saved);
    onSaved?.();
    if (mountedRef.current) await resource.reload();
  }

  async function confirmDelete() {
    if (!deleting || deletingRef.current) return;
    deletingRef.current = true;
    setDeletingBusy(true);
    setActionError('');
    try {
      await api.deleteNotificationChannel(deleting.value.id, deleting.version);
      onSaved?.();
      if (mountedRef.current) await resource.reload();
      if (mountedRef.current) setDeleting(null);
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        onSessionLost();
        return;
      }
      if (error instanceof ApiError && (error.status === 404 || error.status === 412)) {
        if (mountedRef.current) await resource.reload();
        if (!mountedRef.current) return;
        setDeleting(null);
        setActionError(
          error.status === 412
            ? 'This notification channel was changed elsewhere while the confirmation was open. Review the updated list and try again.'
            : 'This notification channel was already deleted elsewhere. The list has been refreshed.',
        );
        return;
      }
      if (!mountedRef.current) return;
      setDeleting(null);
      setActionError(apiErrorMessage(error));
    } finally {
      deletingRef.current = false;
      if (mountedRef.current) setDeletingBusy(false);
    }
  }

  return (
    <Stack>
      <Group justify="space-between" align="flex-end" wrap="wrap">
        <Text size="sm" c="dimmed">
          External delivery targets for appliance notifications. Changes take effect when saved, without applying network configuration.
        </Text>
        <Group gap="xs">
          {canManage && (
            <Button
              size="xs"
              loading={openingId === 'new'}
              disabled={openingId !== null && openingId !== 'new'}
              onClick={() => void openCreate()}
            >
              Add channel
            </Button>
          )}
          <Button size="xs" variant="default" loading={resource.loading} onClick={() => { setActionError(''); void resource.reload(); }}>
            Refresh
          </Button>
        </Group>
      </Group>

      {actionError && (
        <Alert color="red" role="alert" title="Notification channels were not changed" withCloseButton onClose={() => setActionError('')}>
          {actionError}
        </Alert>
      )}

      <Loaded resource={resource} what="notification channels">
        {(list) => {
          const sorted = [...list.items].sort((a, b) => a.value.name.localeCompare(b.value.name, undefined, { sensitivity: 'base' }));
          if (sorted.length === 0) {
            return (
              <Alert role="status" title="No notification channels">
                {canManage
                  ? 'No delivery channels are configured yet. Add a webhook channel to forward alerts outside the appliance.'
                  : 'No delivery channels are configured yet.'}
              </Alert>
            );
          }

          return (
            <ScrollArea type="auto">
              <Table className="data-table" verticalSpacing="xs" withTableBorder>
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>Channel</Table.Th>
                    <Table.Th>Kind</Table.Th>
                    <Table.Th>Status</Table.Th>
                    <Table.Th>Filter</Table.Th>
                    <Table.Th>Secrets</Table.Th>
                    {canManage && <Table.Th><VisuallyHidden>Actions</VisuallyHidden></Table.Th>}
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {sorted.map((item) => {
                    const ch = item.value;
                    const enabled = ch.enabled ?? true;
                    const sev = severitySummary(ch.filter?.minSeverity);
                    const sources = ch.filter?.sources ?? [];
                    const urlSecret = ch.webhook?.url;
                    const authSecret = ch.webhook?.authorization;
                    const hasUnresolved = Boolean(urlSecret?.unresolved || authSecret?.unresolved);
                    return (
                      <Table.Tr key={ch.id}>
                        <Table.Td data-label="Channel">
                          <Group gap="xs" wrap="wrap">
                            <Text fw={600}>{ch.name}</Text>
                            {hasUnresolved && <Badge size="xs" color="orange" variant="light">Unresolved secret</Badge>}
                          </Group>
                          {ch.description && <Text size="sm" c="dimmed">{ch.description}</Text>}
                        </Table.Td>
                        <Table.Td data-label="Kind">
                          <Badge size="sm" variant="default" className="plain-badge">{channelKindLabel[ch.kind] ?? ch.kind}</Badge>
                        </Table.Td>
                        <Table.Td data-label="Status">
                          <Badge variant="dot" color={enabled ? 'green' : 'gray'}>{enabled ? 'Enabled' : 'Disabled'}</Badge>
                        </Table.Td>
                        <Table.Td data-label="Filter">
                          <Group gap="xs" wrap="wrap">
                            <Badge size="xs" className="plain-badge" color={severityColor[sev.severity]} variant={sev.severity === 'critical' ? 'filled' : 'light'}>
                              {sev.label}
                            </Badge>
                            {sources.length === 0 ? (
                              <Text size="sm" c="dimmed">All sources</Text>
                            ) : (
                              sources.map((src) => <Badge key={src} size="xs" variant="outline" className="plain-badge">{src}</Badge>)
                            )}
                          </Group>
                        </Table.Td>
                        <Table.Td data-label="Secrets">
                          <Stack gap={2}>
                            <Text size="sm">
                              URL: {urlSecret?.unresolved ? <Text span inherit className="error-text">unresolved</Text> : urlSecret?.isSet ? 'set' : 'missing'}
                            </Text>
                            {authSecret && (
                              <Text size="xs" c="dimmed">
                                Authorization: {authSecret.unresolved ? <Text span inherit className="error-text">unresolved</Text> : authSecret.isSet ? 'set' : 'missing'}
                              </Text>
                            )}
                          </Stack>
                        </Table.Td>
                        {canManage && (
                          <Table.Td>
                            <Group gap="xs" wrap="nowrap">
                              <Button
                                size="xs"
                                variant="default"
                                loading={openingId === ch.id}
                                disabled={openingId !== null && openingId !== ch.id}
                                aria-label={`Edit ${ch.name}`}
                                onClick={() => void openEdit(ch.id)}
                              >
                                Edit
                              </Button>
                              <Button
                                size="xs"
                                variant="light"
                                color="red"
                                disabled={openingId !== null}
                                aria-label={`Delete ${ch.name}`}
                                onClick={() => { setActionError(''); setDeleting(item); }}
                              >
                                Delete
                              </Button>
                            </Group>
                          </Table.Td>
                        )}
                      </Table.Tr>
                    );
                  })}
                </Table.Tbody>
              </Table>
            </ScrollArea>
          );
        }}
      </Loaded>

      {editor && (
        <NotificationChannelEditor
          key={editor.saved ? `${editor.saved.value.id}:${editor.saved.version}` : 'new'}
          saved={editor.saved}
          existingNames={editor.existingNames}
          existingSecrets={editor.existingSecrets}
          onSave={saveChannel}
          onClose={() => setEditor(null)}
          onRefresh={() => { if (mountedRef.current) void resource.reload(); }}
          onSessionLost={onSessionLost}
        />
      )}

      <Modal
        opened={deleting !== null}
        onClose={() => { if (!deletingRef.current) setDeleting(null); }}
        closeOnClickOutside={!deletingBusy}
        closeOnEscape={!deletingBusy}
        closeButtonProps={{ 'aria-label': 'Close', disabled: deletingBusy }}
        lockScroll={false}
        title="Delete notification channel"
      >
        <Stack>
          <Text size="sm">
            Delete <Text span fw={600}>{deleting?.value.name}</Text>? Notifications will no longer be delivered to this channel.
          </Text>
          <Group justify="flex-end" gap="xs">
            <Button variant="default" disabled={deletingBusy} onClick={() => { if (!deletingRef.current) setDeleting(null); }}>Cancel</Button>
            <Button color="red" loading={deletingBusy} onClick={() => void confirmDelete()}>Delete channel</Button>
          </Group>
        </Stack>
      </Modal>
    </Stack>
  );
}
