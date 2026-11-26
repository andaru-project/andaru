import { plural } from './format';

export const minAliasRefreshSeconds = 60;
export const maxAliasRefreshSeconds = 86400;
export const defaultUrlRefreshSeconds = 3600;
export const defaultHostnamesRefreshSeconds = 300;
export const maxAliasHostnames = 20;

const controlOrFormatChars = /[\p{Cc}\p{Cf}]/u;
const urlCharLimit = 2048;
const hostnameCharLimit = 253;
const dnsHostnamePattern = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*$/;
const ipv4Pattern = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/;

export function isInsecureHttpUrl(raw: string): boolean {
  return /^http:\/\//i.test(raw.trim());
}

const invalidAliasUrlMsg = 'Expected an https:// or http:// URL with a host, a valid port if any, no credentials, and no spaces or invisible characters.';

// Matches internal/config.aliasURL: https or http, non-empty host, optional port 1..65535, no userinfo/whitespace/control/format chars.
export function validateAliasUrl(raw: string): string | undefined {
  const trimmed = raw.trim();
  if (!trimmed) return 'Enter a URL.';
  if (trimmed.length > urlCharLimit) return `URL must be ${urlCharLimit} characters or fewer.`;
  if (controlOrFormatChars.test(trimmed) || /\s/.test(trimmed)) return invalidAliasUrlMsg;
  const match = /^https?:\/\/([^/?#\\@]+)(?:[/?#].*)?$/i.exec(trimmed);
  if (!match || match[1].endsWith(':') || /%(?![0-9a-fA-F]{2})/.test(trimmed)) {
    return invalidAliasUrlMsg;
  }
  const authority = match[1];
  // Extract optional explicit port after IPv6 literal or hostname so out-of-range / port 0 / non-numeric ports fail cleanly.
  const portMatch = authority.startsWith('[')
    ? /^\[[^\]]+\](?::(\d+))?$/.exec(authority)
    : /^[^:]+(?::(\d+))?$/.exec(authority);
  if (!portMatch) return invalidAliasUrlMsg;
  if (portMatch[1] !== undefined) {
    const port = Number(portMatch[1]);
    if (!Number.isSafeInteger(port) || port < 1 || port > 65535) return invalidAliasUrlMsg;
  }
  try {
    const parsed = new URL(trimmed);
    if (
      (parsed.protocol !== 'http:' && parsed.protocol !== 'https:')
      || !parsed.hostname
      || parsed.username !== ''
      || parsed.password !== ''
    ) {
      return invalidAliasUrlMsg;
    }
  } catch {
    return invalidAliasUrlMsg;
  }
  return undefined;
}

function isNumericResolverLabel(label: string): boolean {
  if (/^\d+$/.test(label)) return true;
  if (/^0x[0-9a-f]+$/.test(label)) {
    try {
      return BigInt(label) <= 0xffffffffffffffffn;
    } catch {
      return false;
    }
  }
  return false;
}

// Matches internal/config validatePolicy hostnames rules: valid DNS name (no trailing dot), not an IP,
// and last label is not a decimal or 0x-prefixed hex integer (such as 127.1 or 0x7f.1).
export function validateAliasHostname(raw: string): string | undefined {
  const trimmed = raw.trim();
  if (!trimmed) return 'Expected a host name, such as vpn.example.com, not an address.';
  if (trimmed.length > hostnameCharLimit) return `Host name must be ${hostnameCharLimit} characters or fewer.`;
  if (controlOrFormatChars.test(trimmed) || /\s/.test(trimmed)) {
    return 'Expected a host name, such as vpn.example.com, not an address.';
  }
  const lower = trimmed.toLowerCase();
  if (lower.includes(':') || ipv4Pattern.test(lower) || !dnsHostnamePattern.test(lower)) {
    return 'Expected a host name, such as vpn.example.com, not an address.';
  }
  const labels = lower.split('.');
  if (isNumericResolverLabel(labels[labels.length - 1])) {
    return 'Expected a host name, such as vpn.example.com, not an address.';
  }
  return undefined;
}

export function normalizeAliasHostnames(raw: string[]): string[] {
  return raw.map((item) => item.trim()).filter(Boolean);
}

export function validateAliasHostnames(raw: string[]): string | undefined {
  const names = normalizeAliasHostnames(raw);
  if (names.length === 0) return 'Enter at least one host name.';
  if (names.length > maxAliasHostnames) return `At most ${maxAliasHostnames} host names can be configured.`;
  const seen = new Set<string>();
  for (const name of names) {
    const err = validateAliasHostname(name);
    if (err) return err;
    const key = name.toLowerCase();
    if (seen.has(key)) return 'Each host name can only be listed once.';
    seen.add(key);
  }
  return undefined;
}

export function validateAliasRefreshSeconds(raw: string): string | undefined {
  const trimmed = raw.trim();
  if (!trimmed || !/^\d+$/.test(trimmed)) {
    return `Expected a refresh interval from ${minAliasRefreshSeconds} to ${maxAliasRefreshSeconds} seconds.`;
  }
  const value = Number(trimmed);
  if (!Number.isSafeInteger(value) || value < minAliasRefreshSeconds || value > maxAliasRefreshSeconds) {
    return `Expected a refresh interval from ${minAliasRefreshSeconds} to ${maxAliasRefreshSeconds} seconds.`;
  }
  return undefined;
}

// Formats a refresh interval (60..86400 seconds) accurately using the largest non-zero units.
export function formatRefreshInterval(seconds: number): string {
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const secs = seconds % 60;
  const parts: string[] = [];
  if (days > 0) parts.push(plural(days, 'day'));
  if (hours > 0) parts.push(plural(hours, 'hour'));
  if (minutes > 0) parts.push(plural(minutes, 'minute'));
  if (secs > 0 || parts.length === 0) parts.push(plural(secs, 'second'));
  return parts.join(' ');
}
