import { fieldErrors } from './api';
import type {
  NotificationChannel,
  NotificationChannelKind,
  NotificationChannelWrite,
  NotificationSeverity,
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
  removeAuth: boolean;
}

export const knownNotificationSources = ['engine', 'apply', 'upgrade', 'disk', 'delivery', 'notifications'];

export const channelKindLabel: Record<NotificationChannelKind, string> = {
  webhook: 'Webhook',
};

const controlChars = /\p{Cc}/u;

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
    removeAuth: false,
  };
}

export function existingChannelSecretsOf(channels: NotificationChannel[], snapshot?: Snapshot): string[] {
  const names = new Set<string>();
  for (const ch of channels) {
    if (ch.webhook?.url?.secret) names.add(ch.webhook.url.secret);
    if (ch.webhook?.authorization?.secret) names.add(ch.webhook.authorization.secret);
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
export function nextChannelSecretName(channelName: string, suffix: 'URL' | 'AUTH', existingSecrets: Iterable<string> = []): string {
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
  if (!/^https?:\/\/[^/\s]/i.test(trimmed)) {
    return 'Use an http:// or https:// URL with a host.';
  }
  try {
    const parsed = new URL(trimmed);
    if ((parsed.protocol !== 'http:' && parsed.protocol !== 'https:') || !parsed.host) {
      return 'Use an http:// or https:// URL with a host.';
    }
  } catch {
    return 'Use an http:// or https:// URL with a host.';
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
  } else if (controlChars.test(name)) {
    errors.set('name', 'Control characters are not allowed.');
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
  }

  const sources = normalizeSources(draft.sources);
  if (sources.length > 64) {
    errors.set('sources', 'At most 64 sources can be selected.');
  } else if (sources.some((s) => s.length > 64 || controlChars.test(s))) {
    errors.set('sources', 'Each source must be 1 to 64 characters without control characters.');
  }

  const urlErr = validateWebhookUrl(draft.webhookUrl, !saved);
  if (urlErr) errors.set('webhookUrl', urlErr);

  if (!draft.removeAuth && draft.webhookAuth.includes('\0')) {
    errors.set('webhookAuth', 'Authorization header cannot contain null bytes.');
  }

  return errors;
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

  const existingUrl = saved?.webhook?.url;
  const trimmedUrl = draft.webhookUrl.trim();
  let urlWrite: SecretWrite;
  if (trimmedUrl !== '') {
    const secret = existingUrl?.secret || nextChannelSecretName(name, 'URL', used);
    used.add(secret);
    urlWrite = { secret, set: trimmedUrl, expectedVersion: existingUrl?.version || '0' };
  } else if (existingUrl) {
    used.add(existingUrl.secret);
    urlWrite = { secret: existingUrl.secret, ...(existingUrl.unresolved ? { unresolved: true } : {}) };
  } else {
    const secret = nextChannelSecretName(name, 'URL', used);
    used.add(secret);
    urlWrite = { secret, set: '', expectedVersion: '0' };
  }

  const existingAuth = saved?.webhook?.authorization;
  const trimmedAuth = draft.webhookAuth.trim();
  let authWrite: SecretWrite | undefined;
  if (!draft.removeAuth) {
    if (trimmedAuth !== '') {
      const secret = existingAuth?.secret || nextChannelSecretName(name, 'AUTH', used);
      used.add(secret);
      authWrite = { secret, set: trimmedAuth, expectedVersion: existingAuth?.version || '0' };
    } else if (existingAuth) {
      authWrite = { secret: existingAuth.secret, ...(existingAuth.unresolved ? { unresolved: true } : {}) };
    }
  }

  return {
    name,
    ...(description ? { description } : {}),
    enabled: draft.enabled,
    kind: draft.kind,
    filter: {
      minSeverity: draft.minSeverity,
      ...(sources.length > 0 ? { sources } : {}),
    },
    webhook: {
      url: urlWrite,
      ...(authWrite ? { authorization: authWrite } : {}),
    },
  };
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
    return '';
  });
}
