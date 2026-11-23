import { useSyncExternalStore } from 'react';

const storageKey = 'andaru.timezone';
const listeners = new Set<() => void>();
const timestampFormatters = new Map<string, Intl.DateTimeFormat>();
const timeFormatters = new Map<string, Intl.DateTimeFormat>();

function isValidTimeZone(zone: string): boolean {
  if (!zone || zone === 'local') return true;
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: zone });
    return true;
  } catch {
    return false;
  }
}

function readStored(): string {
  try {
    const value = localStorage.getItem(storageKey);
    return value && isValidTimeZone(value) ? value : 'local';
  } catch {
    return 'local';
  }
}

let current = readStored();

function notify() {
  for (const listener of listeners) listener();
}

function onStorage(event: StorageEvent) {
  if (event.key !== null && event.key !== storageKey) return;
  current = readStored();
  notify();
}

export function getTimeZone(): string {
  return current;
}

export function setTimeZone(next: string) {
  const normalized = next && isValidTimeZone(next) ? next : 'local';
  current = normalized;
  try {
    if (normalized === 'local') localStorage.removeItem(storageKey);
    else localStorage.setItem(storageKey, normalized);
  } catch { /* storage may be unavailable; still applies for this visit */ }
  notify();
}

function subscribe(listener: () => void) {
  if (listeners.size === 0 && typeof window !== 'undefined') window.addEventListener('storage', onStorage);
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
    if (listeners.size === 0 && typeof window !== 'undefined') window.removeEventListener('storage', onStorage);
  };
}

export function useTimeZone(): string {
  return useSyncExternalStore(subscribe, getTimeZone, () => 'local');
}

export function browserTimeZone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
}

function resolvedZone(choice: string): string | undefined {
  return !choice || choice === 'local' || !isValidTimeZone(choice) ? undefined : choice;
}

function offsetFromPart(raw: string | undefined): string {
  if (!raw || raw === 'GMT' || raw === 'UTC') return '+00:00';
  return raw.replace(/^(?:GMT|UTC)/, '').replace(/\u2212/g, '-');
}

function getTimestampFormatter(zone: string | undefined): Intl.DateTimeFormat {
  const key = zone ?? browserTimeZone();
  let formatter = timestampFormatters.get(key);
  if (!formatter) {
    formatter = new Intl.DateTimeFormat('en-US', {
      timeZone: zone,
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
      hourCycle: 'h23',
      timeZoneName: 'longOffset',
    });
    timestampFormatters.set(key, formatter);
  }
  return formatter;
}

function getTimeFormatter(zone: string | undefined): Intl.DateTimeFormat {
  const key = zone ?? browserTimeZone();
  let formatter = timeFormatters.get(key);
  if (!formatter) {
    formatter = new Intl.DateTimeFormat('en-GB', {
      timeZone: zone,
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
      hourCycle: 'h23',
    });
    timeFormatters.set(key, formatter);
  }
  return formatter;
}

// formatTimestamp formats a timestamp as "YYYY-MM-DD HH:MM:SS ±HH:MM" in the configured time zone.
export function formatTimestamp(input: string | number | Date, choice = current): string {
  const date = input instanceof Date ? input : new Date(input);
  if (Number.isNaN(date.getTime())) return '—';
  const parts = getTimestampFormatter(resolvedZone(choice)).formatToParts(date);
  const pick = (type: Intl.DateTimeFormatPartTypes) => parts.find((part) => part.type === type)?.value ?? '';
  return `${pick('year')}-${pick('month')}-${pick('day')} ${pick('hour')}:${pick('minute')}:${pick('second')} ${offsetFromPart(pick('timeZoneName'))}`;
}

// formatTime formats a time of day as "HH:MM:SS" in the configured time zone.
export function formatTime(input: string | number | Date, choice = current): string {
  const date = input instanceof Date ? input : new Date(input);
  if (Number.isNaN(date.getTime())) return '—';
  return getTimeFormatter(resolvedZone(choice)).format(date);
}

export function timeZoneOptions(active = current): Array<{ value: string; label: string }> {
  const local = browserTimeZone();
  const intl = Intl as typeof Intl & { supportedValuesOf?(key: 'timeZone'): string[] };
  const supported = intl.supportedValuesOf?.('timeZone') ?? ['UTC', local];
  const zones = [...new Set(supported.filter((zone) => zone !== 'UTC'))];
  if (active !== 'local' && active !== 'UTC' && isValidTimeZone(active) && !zones.includes(active)) zones.unshift(active);
  return [
    { value: 'local', label: `Browser local (${local})` },
    { value: 'UTC', label: 'UTC (+00:00)' },
    ...zones.map((zone) => ({ value: zone, label: zone })),
  ];
}
