import { fieldErrors } from './api';
import type {
  EmailChannelSecurity,
  KnownNotificationChannelKind,
  NotificationChannel,
  NotificationChannelKind,
  NotificationChannelWrite,
  NotificationSeverity,
  SecretView,
  SecretWrite,
  Snapshot,
} from './types';

export interface NotificationChannelDraft {
  name: string;
  description: string;
  enabled: boolean;
  kind: NotificationChannelKind;
  minSeverity: NotificationSeverity;
  sources: string[];
  webhookUrl: string;
  webhookAuth: string;
  removeWebhookAuth: boolean;
  ntfyServer: string;
  ntfyTopic: string;
  ntfyToken: string;
  removeNtfyToken: boolean;
  pushoverToken: string;
  pushoverUser: string;
  telegramToken: string;
  telegramChatId: string;
  emailHost: string;
  emailPort: string;
  emailSecurity: EmailChannelSecurity;
  emailUsername: string;
  emailPassword: string;
  removeEmailPassword: boolean;
  emailFrom: string;
  emailTo: string[];
}

export const knownNotificationSources = ['engine', 'apply', 'upgrade', 'disk', 'delivery', 'notifications'];

export const channelKindLabel: Record<KnownNotificationChannelKind, string> = {
  webhook: 'Webhook',
  ntfy: 'ntfy',
  pushover: 'Pushover',
  telegram: 'Telegram',
  email: 'E-mail',
};

export function isKnownChannelKind(kind: string): kind is KnownNotificationChannelKind {
  return Object.hasOwn(channelKindLabel, kind);
}

export function channelKindDisplay(kind: string): string {
  return isKnownChannelKind(kind) ? channelKindLabel[kind] : kind;
}

const controlChars = /\p{Cc}/u;
const controlOrFormatChars = /[\p{Cc}\p{Cf}]/u;
const ntfyTopicPattern = /^[A-Za-z0-9_-]{1,64}$/;
const telegramChatIdPattern = /^(-?[0-9]{1,63}|@[A-Za-z0-9_]{1,63})$/;
const utf8Encoder = new TextEncoder();
const secretByteLimit = 4096;
const ntfyServerCharLimit = 2048;
const descriptionCharLimit = 4096;
const utf8ByteLength = (value: string) => utf8Encoder.encode(value).length;

const topLevelChannelKeys = new Set(['id', 'name', 'description', 'enabled', 'kind', 'filter']);
const forbiddenObjectKeys = new Set(['__proto__', 'constructor', 'prototype']);
const secretViewKeys = new Set(['secret', 'isSet', 'updatedAt', 'version', 'unresolved']);

export interface ChannelSecretEntry {
  label: string;
  view: SecretView;
  optional?: boolean;
}

function isSecretView(value: unknown): value is SecretView {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false;
  const obj = value as Record<string, unknown>;
  if (!Object.hasOwn(obj, 'secret') || typeof obj.secret !== 'string') return false;
  const hasMetadata = (Object.hasOwn(obj, 'isSet') && typeof obj.isSet === 'boolean')
    || (Object.hasOwn(obj, 'version') && typeof obj.version === 'string')
    || (Object.hasOwn(obj, 'unresolved') && typeof obj.unresolved === 'boolean');
  if (!hasMetadata) return false;
  return Object.keys(obj).every((k) => secretViewKeys.has(k));
}

function collectUnknownSecrets(value: unknown, prefix: string, out: ChannelSecretEntry[]) {
  if (isSecretView(value)) {
    out.push({ label: prefix || 'secret', view: value });
    return;
  }
  if (Array.isArray(value)) {
    value.forEach((item, index) => collectUnknownSecrets(item, prefix ? `${prefix}[${index}]` : `[${index}]`, out));
    return;
  }
  if (value && typeof value === 'object') {
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      collectUnknownSecrets(v, prefix ? `${prefix}.${k}` : k, out);
    }
  }
}

// Extract all secret references held by a channel across known and unknown future kind sub-objects.
export function channelSecretsOf(channel: NotificationChannel): ChannelSecretEntry[] {
  if (channel.kind === 'webhook') {
    const out: ChannelSecretEntry[] = [];
    if (channel.webhook?.url) out.push({ label: 'URL', view: channel.webhook.url });
    if (channel.webhook?.authorization) out.push({ label: 'Authorization', view: channel.webhook.authorization, optional: true });
    return out;
  }
  if (channel.kind === 'ntfy') {
    return channel.ntfy?.token ? [{ label: 'Token', view: channel.ntfy.token, optional: true }] : [];
  }
  if (channel.kind === 'pushover') {
    const out: ChannelSecretEntry[] = [];
    if (channel.pushover?.token) out.push({ label: 'App token', view: channel.pushover.token });
    if (channel.pushover?.user) out.push({ label: 'User key', view: channel.pushover.user });
    return out;
  }
  if (channel.kind === 'telegram') {
    return channel.telegram?.token ? [{ label: 'Bot token', view: channel.telegram.token }] : [];
  }
  if (channel.kind === 'email') {
    return channel.email?.password ? [{ label: 'Password', view: channel.email.password, optional: true }] : [];
  }
  // Forward-compatible recursive scan of unknown kind sub-objects.
  const out: ChannelSecretEntry[] = [];
  if (!topLevelChannelKeys.has(channel.kind) && !forbiddenObjectKeys.has(channel.kind) && Object.hasOwn(channel, channel.kind)) {
    collectUnknownSecrets(channel[channel.kind], '', out);
  }
  return out;
}

// Echo an unknown kind's sub-object back in write form, converting any SecretView fields to { secret, unresolved? }.
function echoUnknownKindSubObject(sub: unknown): unknown {
  if (isSecretView(sub)) {
    return { secret: sub.secret, ...(sub.unresolved ? { unresolved: true } : {}) };
  }
  if (Array.isArray(sub)) return sub.map(echoUnknownKindSubObject);
  if (sub && typeof sub === 'object') {
    return Object.fromEntries(
      Object.entries(sub as Record<string, unknown>)
        .filter(([, v]) => v !== undefined)
        .map(([k, v]) => [k, echoUnknownKindSubObject(v)]),
    );
  }
  return sub;
}

export function channelDraftOf(channel?: NotificationChannel): NotificationChannelDraft {
  return {
    name: channel?.name ?? '',
    description: channel?.description ?? '',
    enabled: channel?.enabled ?? true,
    kind: channel?.kind ?? 'webhook',
    minSeverity: channel?.filter?.minSeverity ?? 'info',
    sources: [...(channel?.filter?.sources ?? [])],
    webhookUrl: '',
    webhookAuth: '',
    removeWebhookAuth: false,
    ntfyServer: channel?.ntfy?.server ?? 'https://ntfy.sh',
    ntfyTopic: channel?.ntfy?.topic ?? '',
    ntfyToken: '',
    removeNtfyToken: false,
    pushoverToken: '',
    pushoverUser: '',
    telegramToken: '',
    telegramChatId: channel?.telegram?.chatId ?? '',
    emailHost: channel?.email?.host ?? '',
    emailPort: channel?.email ? String(channel.email.port) : '465',
    emailSecurity: channel?.email?.security ?? 'tls',
    emailUsername: channel?.email?.username ?? '',
    emailPassword: '',
    removeEmailPassword: false,
    emailFrom: channel?.email?.from ?? '',
    emailTo: [...(channel?.email?.to ?? [])],
  };
}

export function existingChannelSecretsOf(channels: NotificationChannel[], snapshot?: Snapshot): string[] {
  const names = new Set<string>();
  for (const ch of channels) {
    for (const { view } of channelSecretsOf(ch)) {
      if (view.secret) names.add(view.secret);
    }
  }
  if (snapshot) {
    for (const link of snapshot.value.links) {
      if (link.password?.secret) names.add(link.password.secret);
    }
    for (const unused of snapshot.unusedSecrets ?? []) names.add(unused);
  }
  return [...names];
}

// Allocate a collision-free core secret name matching ^[A-Za-z_][A-Za-z0-9_]{0,63}$.
export function nextChannelSecretName(
  channelName: string,
  suffix: 'URL' | 'AUTH' | 'TOKEN' | 'USER' | 'PASSWORD',
  existingSecrets: Iterable<string> = [],
): string {
  const used = new Set(existingSecrets);
  const slug = channelName.toUpperCase().replace(/[^A-Z0-9]+/g, '_').replace(/^_+|_+$/g, '').slice(0, 40).replace(/_+$/, '');
  const base = slug ? `NOTIFY_${slug}_${suffix}` : `NOTIFY_${suffix}`;
  if (!used.has(base)) return base;
  for (let i = 2; ; i += 1) {
    const candidate = `${base}_${i}`;
    if (!used.has(candidate)) return candidate;
  }
}

export function validateWebhookUrl(raw: string, required: boolean): string | undefined {
  const trimmed = raw.trim();
  if (!trimmed) return required ? 'Enter a webhook URL.' : undefined;
  if (controlChars.test(trimmed) || /\s/.test(trimmed)) {
    return 'Webhook URL cannot contain whitespace or control characters.';
  }
  if (utf8ByteLength(trimmed) > secretByteLimit) {
    return `Webhook URL must be ${secretByteLimit} bytes or fewer.`;
  }
  if (!/^https?:\/\/[^/?#\\]/i.test(trimmed) || /^https?:\/\/[^/?#]*\\/i.test(trimmed)) {
    return 'Use an http:// or https:// URL with a host.';
  }
  try {
    const parsed = new URL(trimmed);
    if ((parsed.protocol !== 'http:' && parsed.protocol !== 'https:') || !parsed.hostname) {
      return 'Use an http:// or https:// URL with a host.';
    }
  } catch {
    return 'Use an http:// or https:// URL with a host.';
  }
  return undefined;
}

const invalidNtfyServerMsg = 'Use an http:// or https:// server URL (optional proxy path, with no credentials, query, or fragment).';

// ntfy server must be an http(s) URL (may include a reverse-proxy path) with no credentials, query, or fragment.
export function validateNtfyServer(raw: string): string | undefined {
  const trimmed = raw.trim();
  if (!trimmed) return 'Enter an ntfy server URL.';
  if (controlChars.test(trimmed) || /\s/.test(trimmed)) {
    return 'Server URL cannot contain whitespace or control characters.';
  }
  if (trimmed.length > ntfyServerCharLimit) {
    return `Server URL must be ${ntfyServerCharLimit} characters or fewer.`;
  }
  // Authority must be non-empty without userinfo (@), backslash, query (?), or fragment (#), and no empty trailing port (:).
  const match = /^https?:\/\/([^/?#\\@]+)(?:\/[^?#\\]*)?$/i.exec(trimmed);
  if (!match || match[1].endsWith(':') || /%(?![0-9a-fA-F]{2})/.test(trimmed)) {
    return invalidNtfyServerMsg;
  }
  try {
    const parsed = new URL(trimmed);
    if (
      (parsed.protocol !== 'http:' && parsed.protocol !== 'https:')
      || !parsed.hostname
      || /^\.+$/.test(parsed.hostname)
      || parsed.hostname.includes('..')
      || parsed.port === '0'
    ) {
      return invalidNtfyServerMsg;
    }
  } catch {
    return invalidNtfyServerMsg;
  }
  return undefined;
}

export function validateNtfyTopic(raw: string): string | undefined {
  const trimmed = raw.trim();
  if (!trimmed) return 'Enter an ntfy topic.';
  if (!ntfyTopicPattern.test(trimmed)) {
    return 'Use 1 to 64 letters, digits, underscores, or hyphens.';
  }
  return undefined;
}

export function validateTelegramChatId(raw: string): string | undefined {
  const trimmed = raw.trim();
  if (!trimmed) return 'Enter a Telegram chat ID.';
  if (!telegramChatIdPattern.test(trimmed)) {
    return 'Use a numeric chat ID (optional leading -) or @channelname (up to 64 characters).';
  }
  return undefined;
}

const emailHostCharLimit = 253;
const emailUsernameCharLimit = 256;
const emailAddressCharLimit = 512;
const emailMaxRecipients = 20;
const dnsHostnamePattern = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*$/i;
const ipv4Pattern = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/;
const forbiddenEmailAddrChars = /[ <>"\\()[\],;:]/;
const quotedDisplayNamePattern = /^"(?:[^"\\]|\\.)*"$/;
const unquotedDisplaySpecials = /[()<>@,;:\\"[\]]/;

function isValidIPv4(value: string): boolean {
  const match = ipv4Pattern.exec(value);
  if (!match) return false;
  return match.slice(1).every((part) => (part === '0' || !part.startsWith('0')) && Number(part) <= 255);
}

function isValidIPv6(value: string): boolean {
  if (!value.includes(':') || /[\s[\]%/?#@\\]/.test(value)) return false;
  try {
    new URL(`http://[${value}]`);
    return true;
  } catch {
    return false;
  }
}

export function validateEmailHost(raw: string): string | undefined {
  const trimmed = raw.trim();
  if (!trimmed) return 'Enter an SMTP host.';
  if (controlOrFormatChars.test(trimmed) || /\s/.test(trimmed)) {
    return 'SMTP host cannot contain whitespace, control, or format characters.';
  }
  if (trimmed.length > emailHostCharLimit) {
    return `SMTP host must be ${emailHostCharLimit} characters or fewer.`;
  }
  if (isValidIPv4(trimmed) || isValidIPv6(trimmed)) return undefined;
  const labels = trimmed.split('.');
  const lastLabel = labels[labels.length - 1];
  if (dnsHostnamePattern.test(trimmed) && !/^\d+$/.test(lastLabel)) {
    return undefined;
  }
  return 'Enter a host name or IP address, with no scheme, port, or path.';
}

export function validateEmailPort(raw: string): string | undefined {
  const trimmed = raw.trim();
  if (!trimmed) return 'Enter an SMTP port.';
  if (!/^\d+$/.test(trimmed)) return 'Port must be a whole number from 1 to 65535.';
  const port = Number(trimmed);
  if (!Number.isSafeInteger(port) || port < 1 || port > 65535) {
    return 'Port must be a whole number from 1 to 65535.';
  }
  return undefined;
}

// Strip balanced RFC 5322 comments (...) outside quoted strings so valid addresses such as
// "Ops (On call) <ops@example.com>" or "ops@example.com (Ops)" parse consistently with net/mail.ParseAddress.
// Returns undefined if quotes or parentheses are unbalanced.
function stripRfc5322Comments(input: string): string | undefined {
  let out = '';
  let inQuotes = false;
  let commentDepth = 0;
  for (let i = 0; i < input.length; i += 1) {
    const ch = input[i];
    if (inQuotes) {
      out += ch;
      if (ch === '\\') {
        if (i + 1 >= input.length) return undefined;
        out += input[++i];
      } else if (ch === '"') {
        inQuotes = false;
      }
      continue;
    }
    if (commentDepth > 0) {
      if (ch === '\\') {
        if (i + 1 >= input.length) return undefined;
        i += 1;
      } else if (ch === '(') {
        commentDepth += 1;
      } else if (ch === ')') {
        commentDepth -= 1;
        if (commentDepth === 0) out += ' ';
      }
      continue;
    }
    if (ch === '"') {
      inQuotes = true;
      out += ch;
    } else if (ch === '(') {
      commentDepth = 1;
    } else if (ch === ')') {
      return undefined;
    } else {
      out += ch;
    }
  }
  return inQuotes || commentDepth !== 0 ? undefined : out.trim();
}

// Parse one RFC 5322 address ("ops@example.com", "Display Name <ops@example.com>", or with RFC 5322 comments)
// suitable for SMTP MAIL FROM / RCPT TO (no quoted local part, no control/format chars, exactly one '@').
export function parseEmailAddress(raw: string): { address: string } | { error: string } {
  const trimmed = raw.trim();
  if (!trimmed) return { error: 'Enter an e-mail address.' };
  if (controlOrFormatChars.test(trimmed)) {
    return { error: 'Control or format characters are not allowed.' };
  }
  if (trimmed.length < 3 || trimmed.length > emailAddressCharLimit) {
    return { error: `E-mail address must be 3 to ${emailAddressCharLimit} characters.` };
  }

  const uncommented = stripRfc5322Comments(trimmed);
  if (uncommented === undefined) {
    return { error: 'Enter one e-mail address, such as Andaru <alerts@example.com>.' };
  }

  let addr = uncommented;
  if (uncommented.endsWith('>')) {
    const lt = uncommented.lastIndexOf('<');
    if (lt < 0) {
      return { error: 'Enter one e-mail address, such as Andaru <alerts@example.com>.' };
    }
    const displayName = uncommented.slice(0, lt).trim();
    if (displayName && !quotedDisplayNamePattern.test(displayName) && unquotedDisplaySpecials.test(displayName)) {
      return { error: 'Quote display names containing special characters, or use a plain address.' };
    }
    addr = uncommented.slice(lt + 1, -1);
  } else if (uncommented.includes('<') || uncommented.includes('>')) {
    return { error: 'Enter one e-mail address, such as Andaru <alerts@example.com>.' };
  }

  const at = addr.indexOf('@');
  const local = at > 0 ? addr.slice(0, at) : '';
  const domain = at > 0 ? addr.slice(at + 1) : '';
  if (
    !local
    || !domain
    || addr.indexOf('@', at + 1) !== -1
    || forbiddenEmailAddrChars.test(addr)
    || /\s/.test(addr)
    || local.startsWith('.')
    || local.endsWith('.')
    || local.includes('..')
    || domain.startsWith('.')
    || domain.endsWith('.')
    || domain.includes('..')
  ) {
    return { error: 'Enter one e-mail address without a quoted local part, such as ops@example.com.' };
  }
  return { address: addr.toLowerCase() };
}

const normalizeEmailRecipients = (raw: string[]) => raw.map((item) => item.trim()).filter(Boolean);

function validateSecretValue(
  raw: string,
  articleLabel: string,
  fieldLabel: string,
  required: boolean,
  preserveWhitespace = false,
): string | undefined {
  const value = preserveWhitespace ? raw : raw.trim();
  if (!value) return required ? `Enter ${articleLabel}.` : undefined;
  if (controlChars.test(value)) {
    return `${fieldLabel} cannot contain control characters or line breaks.`;
  }
  if (utf8ByteLength(value) > secretByteLimit) {
    return `${fieldLabel} must be ${secretByteLimit} bytes or fewer.`;
  }
  return undefined;
}

export function normalizeSources(raw: string[]): string[] {
  const unique = new Set<string>();
  for (const item of raw) {
    const trimmed = item.trim();
    if (trimmed) unique.add(trimmed);
  }
  return [...unique].sort((a, b) => a.localeCompare(b));
}

export function validateChannelDraft(
  draft: NotificationChannelDraft,
  saved: NotificationChannel | undefined,
  existingNames: Iterable<string> = [],
): Map<string, string> {
  const errors = new Map<string, string>();
  const name = draft.name.trim();
  if (!name) {
    errors.set('name', 'Enter a channel name.');
  } else if (controlOrFormatChars.test(name)) {
    errors.set('name', 'Control or format characters are not allowed.');
  } else if (name.length > 128) {
    errors.set('name', 'Channel name must be 128 characters or fewer.');
  } else {
    for (const existing of existingNames) {
      if (existing === name && existing !== saved?.name) {
        errors.set('name', 'Another channel already has this name.');
        break;
      }
    }
  }

  if (controlChars.test(draft.description)) {
    errors.set('description', 'Control characters are not allowed.');
  } else if (draft.description.trim().length > descriptionCharLimit) {
    errors.set('description', `Description must be ${descriptionCharLimit} characters or fewer.`);
  }

  const sources = normalizeSources(draft.sources);
  if (sources.length > 64) {
    errors.set('sources', 'At most 64 sources can be selected.');
  } else if (sources.some((s) => s.length > 64 || controlOrFormatChars.test(s))) {
    errors.set('sources', 'Each source must be 1 to 64 characters without control or format characters.');
  }

  const sameKind = saved?.kind === draft.kind;
  if (draft.kind === 'webhook') {
    const urlErr = validateWebhookUrl(draft.webhookUrl, !(sameKind && saved?.webhook?.url));
    if (urlErr) errors.set('webhookUrl', urlErr);
    if (!draft.removeWebhookAuth) {
      const authErr = validateSecretValue(draft.webhookAuth, 'an authorization header', 'Authorization header', false);
      if (authErr) errors.set('webhookAuth', authErr);
    }
  } else if (draft.kind === 'ntfy') {
    const serverErr = validateNtfyServer(draft.ntfyServer);
    if (serverErr) errors.set('ntfyServer', serverErr);
    const topicErr = validateNtfyTopic(draft.ntfyTopic);
    if (topicErr) errors.set('ntfyTopic', topicErr);
    if (!draft.removeNtfyToken) {
      const tokErr = validateSecretValue(draft.ntfyToken, 'an access token', 'Access token', false);
      if (tokErr) errors.set('ntfyToken', tokErr);
    }
  } else if (draft.kind === 'pushover') {
    const tokErr = validateSecretValue(draft.pushoverToken, 'an application token', 'Application token', !(sameKind && saved?.pushover?.token));
    if (tokErr) errors.set('pushoverToken', tokErr);
    const userErr = validateSecretValue(draft.pushoverUser, 'a user key', 'User key', !(sameKind && saved?.pushover?.user));
    if (userErr) errors.set('pushoverUser', userErr);
  } else if (draft.kind === 'telegram') {
    const tokErr = validateSecretValue(draft.telegramToken, 'a bot token', 'Bot token', !(sameKind && saved?.telegram?.token));
    if (tokErr) errors.set('telegramToken', tokErr);
    const chatErr = validateTelegramChatId(draft.telegramChatId);
    if (chatErr) errors.set('telegramChatId', chatErr);
  } else if (draft.kind === 'email') {
    const hostErr = validateEmailHost(draft.emailHost);
    if (hostErr) errors.set('emailHost', hostErr);
    const portErr = validateEmailPort(draft.emailPort);
    if (portErr) errors.set('emailPort', portErr);

    const username = draft.emailUsername.trim();
    if (username && controlOrFormatChars.test(username)) {
      errors.set('emailUsername', 'Control or format characters are not allowed.');
    } else if (username.length > emailUsernameCharLimit) {
      errors.set('emailUsername', `Username must be ${emailUsernameCharLimit} characters or fewer.`);
    }

    const existingPassword = sameKind ? saved?.email?.password : undefined;
    const hasNewPassword = !draft.removeEmailPassword && draft.emailPassword !== '';
    const keepsExistingPassword = Boolean(existingPassword) && !draft.removeEmailPassword;
    const willSendPassword = hasNewPassword || keepsExistingPassword;

    if (!draft.removeEmailPassword) {
      const pwErr = validateSecretValue(draft.emailPassword, 'a password', 'Password', false, true);
      if (pwErr) errors.set('emailPassword', pwErr);
    }

    if (username && !willSendPassword) {
      if (draft.removeEmailPassword) {
        if (!errors.has('emailUsername')) {
          errors.set('emailUsername', 'Clear the username, or uncheck "Remove password" to keep SMTP authentication.');
        }
      } else if (!errors.has('emailPassword')) {
        errors.set('emailPassword', 'A username requires a password.');
      }
    } else if (!username && willSendPassword && !errors.has('emailUsername')) {
      errors.set(
        'emailUsername',
        keepsExistingPassword && !hasNewPassword
          ? 'Enter a username, or check "Remove password" to disable SMTP authentication.'
          : 'A password requires a username.',
      );
    }

    const parsedFrom = parseEmailAddress(draft.emailFrom);
    if ('error' in parsedFrom) {
      errors.set('emailFrom', parsedFrom.error);
    }

    const recipients = normalizeEmailRecipients(draft.emailTo);
    if (recipients.length === 0) {
      errors.set('emailTo', 'Enter at least one recipient e-mail address.');
    } else if (recipients.length > emailMaxRecipients) {
      errors.set('emailTo', `At most ${emailMaxRecipients} recipients can be configured.`);
    } else {
      const seen = new Set<string>();
      for (const recipient of recipients) {
        const parsed = parseEmailAddress(recipient);
        if ('error' in parsed) {
          errors.set('emailTo', parsed.error);
          break;
        }
        if (seen.has(parsed.address)) {
          errors.set('emailTo', 'Each recipient address must be unique.');
          break;
        }
        seen.add(parsed.address);
      }
    }
  }

  return errors;
}

export function sharedSecretHolders(
  secretName: string | undefined,
  selfId: string | undefined,
  channels: NotificationChannel[],
): string[] {
  if (!secretName) return [];
  const holders: string[] = [];
  for (const ch of channels) {
    if (ch.id === selfId) continue;
    if (channelSecretsOf(ch).some(({ view }) => view.secret === secretName)) {
      holders.push(ch.name);
    }
  }
  return holders.sort((a, b) => a.localeCompare(b));
}

function writeOptionalSecret(
  rawValue: string,
  remove: boolean,
  channelName: string,
  suffix: 'URL' | 'AUTH' | 'TOKEN' | 'USER' | 'PASSWORD',
  existing: SecretView | undefined,
  used: Set<string>,
  preserveWhitespace = false,
): SecretWrite | undefined {
  if (remove) return undefined;
  const value = preserveWhitespace ? rawValue : rawValue.trim();
  if (value !== '') {
    const secret = nextChannelSecretName(channelName, suffix, used);
    used.add(secret);
    return { secret, set: value, expectedVersion: '0' };
  }
  if (existing) {
    return { secret: existing.secret, ...(existing.unresolved ? { unresolved: true } : {}) };
  }
  return undefined;
}

function writeRequiredSecret(
  rawValue: string,
  channelName: string,
  suffix: 'URL' | 'TOKEN' | 'USER',
  existing: SecretView | undefined,
  used: Set<string>,
): SecretWrite {
  const existingOrNew = writeOptionalSecret(rawValue, false, channelName, suffix, existing, used);
  if (existingOrNew) return existingOrNew;
  const secret = nextChannelSecretName(channelName, suffix, used);
  used.add(secret);
  return { secret, set: '', expectedVersion: '0' };
}

export function buildChannelWrite(
  draft: NotificationChannelDraft,
  saved: NotificationChannel | undefined,
  existingSecrets: Iterable<string> = [],
): NotificationChannelWrite {
  const name = draft.name.trim();
  const description = draft.description.trim();
  const sources = normalizeSources(draft.sources);
  const used = new Set(existingSecrets);

  if (saved) {
    for (const { view } of channelSecretsOf(saved)) {
      if (view.secret) used.add(view.secret);
    }
  }

  const base: NotificationChannelWrite = {
    name,
    ...(description ? { description } : {}),
    enabled: draft.enabled,
    kind: draft.kind,
    filter: {
      minSeverity: draft.minSeverity,
      ...(sources.length > 0 ? { sources } : {}),
    },
  };

  // Always allocate a fresh secret (expectedVersion: '0') when writing a new secret value so
  // editing one channel can never mutate a secret another channel references concurrently.
  const sameKind = saved?.kind === draft.kind;
  if (draft.kind === 'webhook') {
    const url = writeRequiredSecret(draft.webhookUrl, name, 'URL', sameKind ? saved?.webhook?.url : undefined, used);
    const authorization = writeOptionalSecret(
      draft.webhookAuth,
      draft.removeWebhookAuth,
      name,
      'AUTH',
      sameKind ? saved?.webhook?.authorization : undefined,
      used,
    );
    return { ...base, webhook: { url, ...(authorization ? { authorization } : {}) } };
  }
  if (draft.kind === 'ntfy') {
    const token = writeOptionalSecret(
      draft.ntfyToken,
      draft.removeNtfyToken,
      name,
      'TOKEN',
      sameKind ? saved?.ntfy?.token : undefined,
      used,
    );
    return {
      ...base,
      ntfy: {
        server: draft.ntfyServer.trim(),
        topic: draft.ntfyTopic.trim(),
        ...(token ? { token } : {}),
      },
    };
  }
  if (draft.kind === 'pushover') {
    const token = writeRequiredSecret(draft.pushoverToken, name, 'TOKEN', sameKind ? saved?.pushover?.token : undefined, used);
    const user = writeRequiredSecret(draft.pushoverUser, name, 'USER', sameKind ? saved?.pushover?.user : undefined, used);
    return { ...base, pushover: { token, user } };
  }
  if (draft.kind === 'telegram') {
    const token = writeRequiredSecret(draft.telegramToken, name, 'TOKEN', sameKind ? saved?.telegram?.token : undefined, used);
    return {
      ...base,
      telegram: {
        token,
        chatId: draft.telegramChatId.trim(),
      },
    };
  }
  if (draft.kind === 'email') {
    const username = draft.emailUsername.trim();
    const password = writeOptionalSecret(
      draft.emailPassword,
      draft.removeEmailPassword,
      name,
      'PASSWORD',
      sameKind ? saved?.email?.password : undefined,
      used,
      true,
    );
    return {
      ...base,
      email: {
        host: draft.emailHost.trim(),
        port: Number(draft.emailPort.trim()),
        security: draft.emailSecurity,
        ...(username ? { username } : {}),
        ...(password ? { password } : {}),
        from: draft.emailFrom.trim(),
        to: normalizeEmailRecipients(draft.emailTo),
      },
    };
  }

  // Unknown future kind: preserve its own sub-object unchanged (stripping SecretView metadata).
  if (
    sameKind
    && saved
    && !topLevelChannelKeys.has(draft.kind)
    && !forbiddenObjectKeys.has(draft.kind)
    && Object.hasOwn(saved, draft.kind)
    && saved[draft.kind] !== undefined
  ) {
    return { ...base, [draft.kind]: echoUnknownKindSubObject(saved[draft.kind]) } as NotificationChannelWrite;
  }
  return base;
}

export function channelFieldErrors(error: unknown): Map<string, string> {
  return fieldErrors(error, (path) => {
    const clean = path.replace(/^\/(?:notifications\/channels\/\d+\/)?/, '/');
    if (clean === '/name') return 'name';
    if (clean === '/description') return 'description';
    if (clean === '/kind') return 'kind';
    if (clean === '/filter/minSeverity') return 'minSeverity';
    if (clean === '/filter/sources' || clean.startsWith('/filter/sources/')) return 'sources';
    if (clean === '/webhook/url' || clean.startsWith('/webhook/url/')) return 'webhookUrl';
    if (clean === '/webhook/authorization' || clean.startsWith('/webhook/authorization/')) return 'webhookAuth';
    if (clean === '/ntfy/server') return 'ntfyServer';
    if (clean === '/ntfy/topic') return 'ntfyTopic';
    if (clean === '/ntfy/token' || clean.startsWith('/ntfy/token/')) return 'ntfyToken';
    if (clean === '/pushover/token' || clean.startsWith('/pushover/token/')) return 'pushoverToken';
    if (clean === '/pushover/user' || clean.startsWith('/pushover/user/')) return 'pushoverUser';
    if (clean === '/telegram/token' || clean.startsWith('/telegram/token/')) return 'telegramToken';
    if (clean === '/telegram/chatId') return 'telegramChatId';
    if (clean === '/email/host') return 'emailHost';
    if (clean === '/email/port') return 'emailPort';
    if (clean === '/email/security') return 'emailSecurity';
    if (clean === '/email/username') return 'emailUsername';
    if (clean === '/email/password' || clean.startsWith('/email/password/')) return 'emailPassword';
    if (clean === '/email/from') return 'emailFrom';
    if (clean === '/email/to' || clean.startsWith('/email/to/')) return 'emailTo';
    return '';
  });
}
