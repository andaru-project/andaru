import { useSyncExternalStore } from 'react';

const storageKey = 'andaru.timezone';
const listeners = new Set<() => void>();

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
  listeners.add(listener);
  const onStorage = (event: StorageEvent) => {
    if (event.key !== storageKey) return;
    current = readStored();
    listener();
  };
  if (typeof window !== 'undefined') window.addEventListener('storage', onStorage);
  return () => {
    listeners.delete(listener);
    if (typeof window !== 'undefined') window.removeEventListener('storage', onStorage);
  };
}

export function useTimeZone(): string {
  return useSyncExternalStore(subscribe, getTimeZone, getTimeZone);
}

export function browserTimeZone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
}

function resolvedZone(choice: string): string | undefined {
  return choice === 'local' ? undefined : choice;
}

function offsetFromPart(raw: string | undefined): string {
  if (!raw || raw === 'GMT' || raw === 'UTC') return '+00:00';
  return raw.replace(/^GMT/, '').replace(/\u2212/g, '-');
}

// formatTimestamp formats a timestamp as "YYYY-MM-DD HH:MM:SS ±HH:MM" in the configured time zone.
export function formatTimestamp(input: string | number | Date, choice = current): string {
  const date = input instanceof Date ? input : new Date(input);
  if (Number.isNaN(date.getTime())) return '—';
  const parts = new Intl.DateTimeFormat('en-US', {
    timeZone: resolvedZone(choice),
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hourCycle: 'h23',
    timeZoneName: 'longOffset',
  }).formatToParts(date);
  const pick = (type: Intl.DateTimeFormatPartTypes) => parts.find((part) => part.type === type)?.value ?? '';
  return `${pick('year')}-${pick('month')}-${pick('day')} ${pick('hour')}:${pick('minute')}:${pick('second')} ${offsetFromPart(pick('timeZoneName'))}`;
}

// formatTime formats a time of day as "HH:MM:SS" in the configured time zone.
export function formatTime(input: string | number | Date, choice = current): string {
  const date = input instanceof Date ? input : new Date(input);
  if (Number.isNaN(date.getTime())) return '—';
  return new Intl.DateTimeFormat('en-GB', {
    timeZone: resolvedZone(choice),
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hourCycle: 'h23',
  }).format(date);
}

export function timeZoneOptions(): Array<{ value: string; label: string }> {
  const local = browserTimeZone();
  const intl = Intl as typeof Intl & { supportedValuesOf?(key: 'timeZone'): string[] };
  const supported = intl.supportedValuesOf?.('timeZone') ?? ['UTC', local];
  const zones = supported.filter((zone) => zone !== 'UTC');
  return [
    { value: 'local', label: `Browser local (${local})` },
    { value: 'UTC', label: 'UTC (+00:00)' },
    ...zones.map((zone) => ({ value: zone, label: zone })),
  ];
}
