import type { AddonProperty, AddonText, SecretView, SecretWrite } from './types';

export function addonText(value?: AddonText): string {
  if (!value) return '';
  return typeof value === 'string' ? value : value.i18n;
}

export function safeExternalHttpUrl(raw: string): string | null {
  try {
    const parsed = new URL(raw.trim());
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return null;
    return parsed.toString();
  } catch {
    return null;
  }
}

export function normalizeHostname(host: string): string {
  return host.trim().replace(/^\[/, '').replace(/\]$/, '').replace(/\.$/, '').toLowerCase();
}

export const healthBadgeColor: Record<string, string> = {
  healthy: 'green',
  running: 'green',
  starting: 'blue',
  stopped: 'gray',
  held: 'orange',
  unhealthy: 'red',
  none: 'gray',
  unknown: 'gray',
};

export function healthLabel(health: string): string {
  switch (health) {
    case 'healthy':
      return 'Healthy';
    case 'running':
      return 'Running';
    case 'starting':
      return 'Starting';
    case 'stopped':
      return 'Stopped';
    case 'held':
      return 'Held (safe mode)';
    case 'unhealthy':
      return 'Unhealthy';
    case 'none':
      return 'Not running';
    default:
      return health || 'Unknown';
  }
}

export interface AddonPropertyDraft {
  values: Record<string, unknown>;
  secretInputs: Record<string, string>;
  secretRemovals: Record<string, boolean>;
  existingSecrets: Record<string, SecretView>;
}

function isSecretView(value: unknown): value is SecretView {
  return (
    typeof value === 'object' &&
    value !== null &&
    typeof (value as { secret?: unknown }).secret === 'string'
  );
}

function stringArray(raw: unknown, fallback: unknown): string[] {
  const source = Array.isArray(raw) ? raw : Array.isArray(fallback) ? fallback : [];
  return source.filter((item): item is string => typeof item === 'string');
}

export function initialPropertyDraft(
  properties: AddonProperty[] | undefined,
  savedSettings: Record<string, unknown> | undefined,
): AddonPropertyDraft {
  const values: Record<string, unknown> = {};
  const secretInputs: Record<string, string> = {};
  const secretRemovals: Record<string, boolean> = {};
  const existingSecrets: Record<string, SecretView> = {};

  for (const prop of properties ?? []) {
    const raw = savedSettings && Object.hasOwn(savedSettings, prop.name)
      ? savedSettings[prop.name]
      : undefined;

    switch (prop.type) {
      case 'boolean':
        values[prop.name] = typeof raw === 'boolean' ? raw : Boolean(prop.default);
        break;
      case 'integer':
        if (typeof raw === 'number' && Number.isInteger(raw)) values[prop.name] = raw;
        else if (typeof prop.default === 'number' && Number.isInteger(prop.default)) values[prop.name] = prop.default;
        else values[prop.name] = '';
        break;
      case 'enum':
        values[prop.name] = stringArray(raw, prop.default);
        break;
      case 'string-list':
        values[prop.name] = stringArray(raw, prop.default).join('\n');
        break;
      case 'zone-reference':
        values[prop.name] = typeof raw === 'string' ? raw : '';
        break;
      case 'secret-reference':
        secretInputs[prop.name] = '';
        secretRemovals[prop.name] = false;
        if (isSecretView(raw)) existingSecrets[prop.name] = raw;
        break;
      case 'string':
      default:
        if (typeof raw === 'string') values[prop.name] = raw;
        else if (typeof prop.default === 'string') values[prop.name] = prop.default;
        else values[prop.name] = '';
        break;
    }
  }

  return { values, secretInputs, secretRemovals, existingSecrets };
}

export function buildSettingsPayload(
  properties: AddonProperty[] | undefined,
  draft: AddonPropertyDraft,
): { settings?: Record<string, unknown>; clientErrors: Map<string, string> } {
  const settings: Record<string, unknown> = {};
  const clientErrors = new Map<string, string>();

  for (const prop of properties ?? []) {
    switch (prop.type) {
      case 'boolean':
        settings[prop.name] = Boolean(draft.values[prop.name]);
        break;
      case 'integer': {
        const raw = draft.values[prop.name];
        if (raw === '' || raw === undefined || raw === null) {
          break;
        }
        if (typeof raw === 'number' && Number.isSafeInteger(raw)) {
          settings[prop.name] = raw;
        } else if (typeof raw === 'string') {
          const trimmed = raw.trim();
          if (trimmed === '') break;
          const parsed = Number(trimmed);
          if (/^-?\d+$/.test(trimmed) && Number.isSafeInteger(parsed)) {
            settings[prop.name] = parsed;
          } else {
            clientErrors.set(prop.name, 'Enter a valid whole number.');
          }
        } else {
          clientErrors.set(prop.name, 'Enter a valid whole number.');
        }
        break;
      }
      case 'enum': {
        const list = Array.isArray(draft.values[prop.name])
          ? (draft.values[prop.name] as string[]).filter(Boolean)
          : [];
        const keepEmpty = prop.default !== undefined && (prop.minItems ?? 0) === 0;
        if (list.length > 0 || prop.required || keepEmpty) {
          settings[prop.name] = list;
        }
        break;
      }
      case 'string-list': {
        const text = typeof draft.values[prop.name] === 'string' ? (draft.values[prop.name] as string) : '';
        const list = text.split('\n').map((line) => line.trim()).filter(Boolean);
        const keepEmpty = prop.default !== undefined && (prop.minItems ?? 0) === 0;
        if (list.length > 0 || prop.required || keepEmpty) {
          settings[prop.name] = list;
        }
        break;
      }
      case 'zone-reference': {
        const id = typeof draft.values[prop.name] === 'string' ? (draft.values[prop.name] as string).trim() : '';
        if (id) settings[prop.name] = id;
        break;
      }
      case 'secret-reference': {
        const existing = draft.existingSecrets[prop.name];
        const typed = draft.secretInputs[prop.name] ?? '';
        const removed = Boolean(draft.secretRemovals[prop.name]);
        if (removed) break;
        if (typed !== '') {
          const write: SecretWrite = {
            secret: existing?.secret || prop.name,
            set: typed,
            expectedVersion: existing?.version || '0',
          };
          settings[prop.name] = write;
        } else if (existing) {
          const keep: SecretWrite = { secret: existing.secret };
          if (existing.unresolved) keep.unresolved = true;
          settings[prop.name] = keep;
        }
        break;
      }
      case 'string':
      default: {
        const text = typeof draft.values[prop.name] === 'string' ? (draft.values[prop.name] as string) : '';
        const keepEmpty = prop.default !== undefined && !prop.format && (prop.minLength ?? 0) === 0;
        if (text !== '' || prop.required || keepEmpty) {
          settings[prop.name] = text;
        }
        break;
      }
    }
  }

  return {
    settings: Object.keys(settings).length > 0 ? settings : undefined,
    clientErrors,
  };
}
