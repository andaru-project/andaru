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
  channelKindDisplay,
  channelSecretsOf,
  existingChannelSecretsOf,
  isKnownChannelKind,
  knownNotificationSources,
  sharedSecretHolders,
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
  SecretView,
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
  sharedHoldersBySecret: Record<string, string[]>;
}

const kindOptions: Array<{ value: NotificationChannelKind; label: string }> = [
  { value: 'webhook', label: 'Webhook (HTTP POST)' },
  { value: 'ntfy', label: 'ntfy' },
  { value: 'pushover', label: 'Pushover' },
  { value: 'telegram', label: 'Telegram' },
];

const kindSpecificErrorFields = [
  'kind',
  'webhookUrl',
  'webhookAuth',
  'ntfyServer',
  'ntfyTopic',
  'ntfyToken',
  'pushoverToken',
  'pushoverUser',
  'telegramToken',
  'telegramChatId',
] as const;

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

function channelTargetSummary(ch: NotificationChannel): string | undefined {
  if (ch.kind === 'ntfy' && ch.ntfy) return `${ch.ntfy.server}/${ch.ntfy.topic}`;
  if (ch.kind === 'telegram' && ch.telegram) return ch.telegram.chatId;
  return undefined;
}

interface SecretFieldProps {
  label: string;
  unresolvedTitle: string;
  unresolvedBody: string;
  existing?: SecretView;
  sharedHolders: string[];
  freshDescription: string;
  optionalPrefix?: string;
  unresolvedHint: string;
  freshPlaceholder: string;
  keepPlaceholder: string;
  toggleLabel: string;
  required?: boolean;
  value: string;
  error?: string;
  onChange(value: string): void;
  removeLabel?: string;
  removed?: boolean;
  onRemoveChange?(checked: boolean): void;
}

function SecretField({
  label,
  unresolvedTitle,
  unresolvedBody,
  existing,
  sharedHolders,
  freshDescription,
  optionalPrefix,
  unresolvedHint,
  freshPlaceholder,
  keepPlaceholder,
  toggleLabel,
  required,
  value,
  error,
  onChange,
  removeLabel,
  removed,
  onRemoveChange,
}: SecretFieldProps) {
  const sharedText = sharedHolders.length > 0 ? `, shared with ${sharedHolders.join(', ')}` : '';
  let description = freshDescription;
  if (existing) {
    if (existing.unresolved) {
      description = optionalPrefix
        ? `${optionalPrefix} (${existing.secret}, unresolved${sharedText}). ${sharedHolders.length > 0 ? 'Entering a value saves a separate secret for this channel so other channels are not changed.' : unresolvedHint}`
        : `Unresolved secret (${existing.secret}${sharedText}). ${sharedHolders.length > 0 ? 'Entering a new value saves a separate secret for this channel so other channels are not changed.' : unresolvedHint}`;
    } else if (sharedHolders.length > 0) {
      description = optionalPrefix
        ? `${optionalPrefix} (${existing.secret}${sharedText}). Entering a new value saves a separate secret for this channel so other channels are not changed.`
        : `Stored securely (${existing.secret}${sharedText}). Entering a new value saves a separate secret for this channel so other channels are not changed.`;
    } else {
      description = optionalPrefix
        ? `${optionalPrefix} (${existing.secret}). Leave blank to keep the current value.`
        : `Stored securely (${existing.secret}). Leave blank to keep the current value.`;
    }
  }

  return (
    <>
      {existing?.unresolved && !removed && (
        <Alert color="orange" role="status" title={unresolvedTitle}>
          {unresolvedBody}
        </Alert>
      )}
      <PasswordInput
        label={label}
        description={description}
        placeholder={existing && !existing.unresolved ? keepPlaceholder : freshPlaceholder}
        autoComplete="new-password"
        visibilityToggleFocusable
        visibilityToggleButtonProps={{ 'aria-label': toggleLabel }}
        aria-invalid={Boolean(error) || undefined}
        required={required}
        disabled={removed}
        value={value}
        error={error}
        onChange={(event) => onChange(event.currentTarget.value)}
      />
      {existing && removeLabel && onRemoveChange && (
        <Checkbox
          label={removeLabel}
          checked={Boolean(removed)}
          onChange={(event) => onRemoveChange(event.currentTarget.checked)}
        />
      )}
    </>
  );
}

function NotificationChannelEditor({
  saved,
  existingNames,
  existingSecrets,
  sharedHoldersBySecret,
  onSave,
  onClose,
  onRefresh,
  onSessionLost,
}: {
  saved?: SavedObject<NotificationChannel>;
  existingNames: string[];
  existingSecrets: string[];
  sharedHoldersBySecret: Record<string, string[]>;
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

  const sameKind = saved?.value.kind === draft.kind;
  const unknownKind = !isKnownChannelKind(draft.kind);
  const existingWebhookUrl = sameKind ? saved?.value.webhook?.url : undefined;
  const existingWebhookAuth = sameKind ? saved?.value.webhook?.authorization : undefined;
  const existingNtfyToken = sameKind ? saved?.value.ntfy?.token : undefined;
  const existingPushoverToken = sameKind ? saved?.value.pushover?.token : undefined;
  const existingPushoverUser = sameKind ? saved?.value.pushover?.user : undefined;
  const existingTelegramToken = sameKind ? saved?.value.telegram?.token : undefined;

  const sharedFor = (view?: SecretView) => (view?.secret ? (sharedHoldersBySecret[view.secret] ?? []) : []);
  const selectKindOptions = unknownKind
    ? [...kindOptions, { value: draft.kind, label: draft.kind }]
    : kindOptions;

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
                placeholder="ops-alerts"
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
                description={unknownKind ? 'This channel kind is not editable in this version of the web client. Its delivery settings will be kept unchanged.' : undefined}
                allowDeselect={false}
                disabled={unknownKind}
                data={selectKindOptions}
                value={draft.kind}
                error={errors.get('kind')}
                onChange={(value) => {
                  if (value && isKnownChannelKind(value)) {
                    patch({ kind: value });
                    setErrors((cur) => {
                      const next = new Map(cur);
                      for (const field of kindSpecificErrorFields) next.delete(field);
                      return next;
                    });
                  }
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
              {draft.kind === 'webhook' && (
                <>
                  <SecretField
                    label="Webhook URL"
                    unresolvedTitle="Webhook URL needs to be restored"
                    unresolvedBody={`This channel's URL secret (${existingWebhookUrl?.secret ?? ''}) was left unresolved by a restore. Enter a new URL below to resolve it, or leave it blank to keep editing other fields.`}
                    existing={existingWebhookUrl}
                    sharedHolders={sharedFor(existingWebhookUrl)}
                    freshDescription="Must be an http:// or https:// URL with a host. Stored in the secret store and never shown again."
                    unresolvedHint="Enter an http:// or https:// URL to resolve."
                    freshPlaceholder="https://hooks.example/services/..."
                    keepPlaceholder="Leave blank to keep existing URL"
                    toggleLabel="Toggle webhook URL visibility"
                    required={!existingWebhookUrl}
                    value={draft.webhookUrl}
                    error={errors.get('webhookUrl')}
                    onChange={(value) => {
                      patch({ webhookUrl: value });
                      clearFieldError('webhookUrl');
                    }}
                  />
                  <SecretField
                    label="Authorization header"
                    unresolvedTitle="Authorization header needs to be restored"
                    unresolvedBody={`The authorization secret (${existingWebhookAuth?.secret ?? ''}) was left unresolved by a restore. Enter a new value to resolve it, or remove it below.`}
                    existing={existingWebhookAuth}
                    sharedHolders={sharedFor(existingWebhookAuth)}
                    freshDescription="Optional HTTP Authorization header value (for example, Bearer <token>). Stored in the secret store."
                    optionalPrefix="Optional HTTP Authorization header"
                    unresolvedHint="Enter a value to resolve."
                    freshPlaceholder="Bearer ..."
                    keepPlaceholder="Leave blank to keep existing header"
                    toggleLabel="Toggle authorization header visibility"
                    value={draft.webhookAuth}
                    error={errors.get('webhookAuth')}
                    onChange={(value) => {
                      patch({ webhookAuth: value });
                      clearFieldError('webhookAuth');
                    }}
                    removeLabel="Remove authorization header"
                    removed={draft.removeWebhookAuth}
                    onRemoveChange={(checked) => {
                      patch({ removeWebhookAuth: checked, ...(checked ? { webhookAuth: '' } : {}) });
                      clearFieldError('webhookAuth');
                    }}
                  />
                </>
              )}
              {draft.kind === 'ntfy' && (
                <>
                  <TextInput
                    label="Server URL"
                    description="http:// or https:// ntfy server with host only (no path, query, or credentials)."
                    placeholder="https://ntfy.sh"
                    required
                    value={draft.ntfyServer}
                    error={errors.get('ntfyServer')}
                    onChange={(event) => {
                      patch({ ntfyServer: event.currentTarget.value });
                      clearFieldError('ntfyServer');
                    }}
                  />
                  <TextInput
                    label="Topic"
                    description="1 to 64 letters, digits, underscores, or hyphens."
                    placeholder="andaru-alerts"
                    maxLength={64}
                    required
                    value={draft.ntfyTopic}
                    error={errors.get('ntfyTopic')}
                    onChange={(event) => {
                      patch({ ntfyTopic: event.currentTarget.value });
                      clearFieldError('ntfyTopic');
                    }}
                  />
                  <SecretField
                    label="Access token"
                    unresolvedTitle="Access token needs to be restored"
                    unresolvedBody={`The access token secret (${existingNtfyToken?.secret ?? ''}) was left unresolved by a restore. Enter a new token to resolve it, or remove it below.`}
                    existing={existingNtfyToken}
                    sharedHolders={sharedFor(existingNtfyToken)}
                    freshDescription="Optional ntfy access token (tk_...). Stored in the secret store and never shown again."
                    optionalPrefix="Optional ntfy access token"
                    unresolvedHint="Enter a token to resolve."
                    freshPlaceholder="tk_..."
                    keepPlaceholder="Leave blank to keep existing token"
                    toggleLabel="Toggle ntfy access token visibility"
                    value={draft.ntfyToken}
                    error={errors.get('ntfyToken')}
                    onChange={(value) => {
                      patch({ ntfyToken: value });
                      clearFieldError('ntfyToken');
                    }}
                    removeLabel="Remove access token"
                    removed={draft.removeNtfyToken}
                    onRemoveChange={(checked) => {
                      patch({ removeNtfyToken: checked, ...(checked ? { ntfyToken: '' } : {}) });
                      clearFieldError('ntfyToken');
                    }}
                  />
                </>
              )}
              {draft.kind === 'pushover' && (
                <>
                  <SecretField
                    label="Application token"
                    unresolvedTitle="Application token needs to be restored"
                    unresolvedBody={`The application token secret (${existingPushoverToken?.secret ?? ''}) was left unresolved by a restore. Enter a new token below to resolve it, or leave it blank to keep editing other fields.`}
                    existing={existingPushoverToken}
                    sharedHolders={sharedFor(existingPushoverToken)}
                    freshDescription="Pushover application API token. Stored in the secret store and never shown again."
                    unresolvedHint="Enter a Pushover application token to resolve."
                    freshPlaceholder="Application API token"
                    keepPlaceholder="Leave blank to keep existing application token"
                    toggleLabel="Toggle Pushover application token visibility"
                    required={!existingPushoverToken}
                    value={draft.pushoverToken}
                    error={errors.get('pushoverToken')}
                    onChange={(value) => {
                      patch({ pushoverToken: value });
                      clearFieldError('pushoverToken');
                    }}
                  />
                  <SecretField
                    label="User key"
                    unresolvedTitle="User key needs to be restored"
                    unresolvedBody={`The user key secret (${existingPushoverUser?.secret ?? ''}) was left unresolved by a restore. Enter a new user key below to resolve it, or leave it blank to keep editing other fields.`}
                    existing={existingPushoverUser}
                    sharedHolders={sharedFor(existingPushoverUser)}
                    freshDescription="Pushover user or group key. Stored in the secret store and never shown again."
                    unresolvedHint="Enter a Pushover user key to resolve."
                    freshPlaceholder="User or group key"
                    keepPlaceholder="Leave blank to keep existing user key"
                    toggleLabel="Toggle Pushover user key visibility"
                    required={!existingPushoverUser}
                    value={draft.pushoverUser}
                    error={errors.get('pushoverUser')}
                    onChange={(value) => {
                      patch({ pushoverUser: value });
                      clearFieldError('pushoverUser');
                    }}
                  />
                </>
              )}
              {draft.kind === 'telegram' && (
                <>
                  <SecretField
                    label="Bot token"
                    unresolvedTitle="Bot token needs to be restored"
                    unresolvedBody={`The bot token secret (${existingTelegramToken?.secret ?? ''}) was left unresolved by a restore. Enter a new bot token below to resolve it, or leave it blank to keep editing other fields.`}
                    existing={existingTelegramToken}
                    sharedHolders={sharedFor(existingTelegramToken)}
                    freshDescription="Telegram bot token from @BotFather. Stored in the secret store and never shown again."
                    unresolvedHint="Enter a Telegram bot token to resolve."
                    freshPlaceholder="123456:ABC-DEF..."
                    keepPlaceholder="Leave blank to keep existing bot token"
                    toggleLabel="Toggle Telegram bot token visibility"
                    required={!existingTelegramToken}
                    value={draft.telegramToken}
                    error={errors.get('telegramToken')}
                    onChange={(value) => {
                      patch({ telegramToken: value });
                      clearFieldError('telegramToken');
                    }}
                  />
                  <TextInput
                    label="Chat ID"
                    description="Numeric chat ID (optional leading -) or @channelname."
                    placeholder="-1001234567890"
                    maxLength={64}
                    required
                    value={draft.telegramChatId}
                    error={errors.get('telegramChatId')}
                    onChange={(event) => {
                      patch({ telegramChatId: event.currentTarget.value });
                      clearFieldError('telegramChatId');
                    }}
                  />
                </>
              )}
              {unknownKind && (
                <Alert color="gray" role="status" title="Delivery settings are read-only">
                  This channel uses a kind ({draft.kind}) that this web client cannot edit yet. You can still update its name, description, enabled state, or filter; its delivery settings and secrets will be kept unchanged.
                </Alert>
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

  const [busy, setBusy] = useState<{ kind: 'open' | 'test'; id: string } | null>(null);
  const [testNotice, setTestNotice] = useState('');
  const [editor, setEditor] = useState<EditorState | null>(null);
  const [deleting, setDeleting] = useState<SavedObject<NotificationChannel> | null>(null);
  const [deletingBusy, setDeletingBusy] = useState(false);
  const [actionError, setActionError] = useState('');
  const busyRef = useRef(false);
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
    if (!canManage || busyRef.current || deletingRef.current) return;
    busyRef.current = true;
    setBusy({ kind: 'open', id: 'new' });
    setActionError('');
    setTestNotice('');
    try {
      const [list, snapshot] = await Promise.all([api.notificationChannels(), readOptionalSnapshot()]);
      const channels = list.items.map((item) => item.value);
      if (!mountedRef.current) return;
      setEditor({
        existingNames: channels.map((c) => c.name),
        existingSecrets: existingChannelSecretsOf(channels, snapshot),
        sharedHoldersBySecret: {},
      });
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        onSessionLost();
        return;
      }
      if (!mountedRef.current) return;
      setActionError(apiErrorMessage(error));
    } finally {
      busyRef.current = false;
      if (mountedRef.current) setBusy(null);
    }
  }

  async function openEdit(id: string) {
    if (!canManage || busyRef.current || deletingRef.current) return;
    busyRef.current = true;
    setBusy({ kind: 'open', id });
    setActionError('');
    setTestNotice('');
    try {
      const [current, list, snapshot] = await Promise.all([
        api.notificationChannel(id),
        api.notificationChannels(),
        readOptionalSnapshot(),
      ]);
      const channels = list.items.map((item) => item.value);
      if (!mountedRef.current) return;
      const sharedHoldersBySecret: Record<string, string[]> = {};
      for (const { view } of channelSecretsOf(current.value)) {
        if (view.secret) {
          sharedHoldersBySecret[view.secret] = sharedSecretHolders(view.secret, current.value.id, channels);
        }
      }
      setEditor({
        saved: current,
        existingNames: channels.map((c) => c.name),
        existingSecrets: existingChannelSecretsOf(channels, snapshot),
        sharedHoldersBySecret,
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
      busyRef.current = false;
      if (mountedRef.current) setBusy(null);
    }
  }

  async function sendTest(channel: NotificationChannel) {
    if (!canManage || busyRef.current || deletingRef.current) return;
    busyRef.current = true;
    setBusy({ kind: 'test', id: channel.id });
    setActionError('');
    setTestNotice('');
    try {
      const result = await api.testNotificationChannel(channel.id);
      if (!mountedRef.current) return;
      setTestNotice(`Queued test notification for ${channel.name} (${result.job}). Delivery runs in the background; repeated delivery failures raise a notification.`);
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
        if (error instanceof ApiError && error.status === 409) {
          void resource.reload();
        }
        setActionError(apiErrorMessage(error));
      }
    } finally {
      busyRef.current = false;
      if (mountedRef.current) setBusy(null);
    }
  }

  async function saveChannel(value: NotificationChannelWrite, saved?: SavedObject<NotificationChannel>) {
    await api.saveNotificationChannel(value, saved);
    onSaved?.();
    if (mountedRef.current) await resource.reload();
  }

  async function confirmDelete() {
    if (!deleting || deletingRef.current || busyRef.current) return;
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

  const isBusyFor = (kind: 'open' | 'test', id: string) => busy?.kind === kind && busy.id === id;

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
              loading={isBusyFor('open', 'new')}
              disabled={busy !== null && !isBusyFor('open', 'new')}
              onClick={() => void openCreate()}
            >
              Add channel
            </Button>
          )}
          <Button size="xs" variant="default" loading={resource.loading} onClick={() => { setActionError(''); setTestNotice(''); void resource.reload(); }}>
            Refresh
          </Button>
        </Group>
      </Group>

      {testNotice && (
        <Alert color="blue" role="status" title="Test notification queued" withCloseButton closeButtonLabel="Dismiss test notification notice" onClose={() => setTestNotice('')}>
          {testNotice}
        </Alert>
      )}

      {actionError && (
        <Alert color="red" role="alert" title="Notification channel action failed" withCloseButton closeButtonLabel="Dismiss error" onClose={() => setActionError('')}>
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
                  ? 'No delivery channels are configured yet. Add a channel to forward alerts outside the appliance.'
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
                    const secrets = channelSecretsOf(ch);
                    const hasUnresolved = secrets.some(({ view }) => Boolean(view.unresolved));
                    const targetSummary = channelTargetSummary(ch);
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
                          <Stack gap={2} align="flex-start">
                            <Badge size="sm" variant="default" className="plain-badge">{channelKindDisplay(ch.kind)}</Badge>
                            {targetSummary && <Text size="xs" c="dimmed">{targetSummary}</Text>}
                          </Stack>
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
                          {secrets.length === 0 ? (
                            <Text size="sm" c="dimmed">None</Text>
                          ) : (
                            <Stack gap={2}>
                              {secrets.map(({ label, view }, index) => (
                                <Text key={`${label}:${view.secret}`} size={index === 0 ? 'sm' : 'xs'} c={index === 0 ? undefined : 'dimmed'}>
                                  {label}: {view.unresolved ? <Text span inherit className="error-text">unresolved</Text> : view.isSet ? 'set' : 'missing'}
                                </Text>
                              ))}
                            </Stack>
                          )}
                        </Table.Td>
                        {canManage && (
                          <Table.Td>
                            <Group gap="xs" wrap="nowrap">
                              <Button
                                size="xs"
                                variant="default"
                                loading={isBusyFor('test', ch.id)}
                                disabled={busy !== null && !isBusyFor('test', ch.id)}
                                aria-label={`Send test to ${ch.name}`}
                                onClick={() => void sendTest(ch)}
                              >
                                Send test
                              </Button>
                              <Button
                                size="xs"
                                variant="default"
                                loading={isBusyFor('open', ch.id)}
                                disabled={busy !== null && !isBusyFor('open', ch.id)}
                                aria-label={`Edit ${ch.name}`}
                                onClick={() => void openEdit(ch.id)}
                              >
                                Edit
                              </Button>
                              <Button
                                size="xs"
                                variant="light"
                                color="red"
                                disabled={busy !== null}
                                aria-label={`Delete ${ch.name}`}
                                onClick={() => {
                                  if (busyRef.current || deletingRef.current) return;
                                  setActionError('');
                                  setTestNotice('');
                                  setDeleting(item);
                                }}
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
          sharedHoldersBySecret={editor.sharedHoldersBySecret}
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
