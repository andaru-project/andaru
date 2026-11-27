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

export function initialPropertyDraft(
  properties: AddonProperty[] | undefined,
  savedSettings: Record<string, unknown> | undefined,
): AddonPropertyDraft {
  const values: Record<string, unknown> = {};
  const secretInputs: Record<string, string> = {};
  const secretRemovals: Record<string, boolean> = {};
  const existingSecrets: Record<string, SecretView> = {};

  for (const prop of properties ?? []) {
    const raw = savedSettings && Object.prototype.hasOwnProperty.call(savedSettings, prop.name)
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
      case 'enum': {
        const list = Array.isArray(raw)
          ? raw.filter((item): item is string => typeof item === 'string')
          : Array.isArray(prop.default)
            ? prop.default.filter((item): item is string => typeof item === 'string')
            : [];
        values[prop.name] = list;
        break;
      }
      case 'string-list': {
        const list = Array.isArray(raw)
          ? raw.filter((item): item is string => typeof item === 'string')
          : Array.isArray(prop.default)
            ? prop.default.filter((item): item is string => typeof item === 'string')
            : [];
        values[prop.name] = list.join('\n');
        break;
      }
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
): Record<string, unknown> | undefined {
  const settings: Record<string, unknown> = {};

  for (const prop of properties ?? []) {
    switch (prop.type) {
      case 'boolean':
        settings[prop.name] = Boolean(draft.values[prop.name]);
        break;
      case 'integer': {
        const raw = draft.values[prop.name];
        if (typeof raw === 'number' && Number.isFinite(raw)) {
          settings[prop.name] = Math.trunc(raw);
        } else if (typeof raw === 'string' && raw.trim() !== '') {
          const parsed = Number(raw);
          if (Number.isInteger(parsed)) settings[prop.name] = parsed;
        }
        break;
      }
      case 'enum': {
        const list = Array.isArray(draft.values[prop.name])
          ? (draft.values[prop.name] as string[]).filter(Boolean)
          : [];
        if (list.length > 0 || prop.required) {
          settings[prop.name] = list;
        }
        break;
      }
      case 'string-list': {
        const text = typeof draft.values[prop.name] === 'string' ? (draft.values[prop.name] as string) : '';
        const list = text.split('\n').map((line) => line.trim()).filter(Boolean);
        if (list.length > 0 || prop.required) {
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
          };
          if (existing?.version) write.expectedVersion = existing.version;
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
        if (text !== '' || prop.required) {
          settings[prop.name] = text;
        }
        break;
      }
    }
  }

  return Object.keys(settings).length > 0 ? settings : undefined;
}
