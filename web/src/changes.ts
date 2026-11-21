import { useCallback, useRef, useSyncExternalStore } from 'react';
import type { ApiClient } from './api';
import type { Applies, Apply } from './types';

// ChangeState is what the shell polls: every apply, and the saved head's version.
export interface ChangeState {
  applies: Applies;
  head: string;
}

export async function loadChangeState(api: ApiClient): Promise<ChangeState> {
  const [applies, history] = await Promise.all([api.listApplies(), api.versions(1)]);
  return { applies, head: history.items[0]?.id ?? '0' };
}

// newer compares two saved versions: decimal strings without leading zeros.
export function newer(a: string, b: string) {
  return a.length !== b.length ? a.length > b.length : a > b;
}

// liveApply is the generation that runs now; null is the factory network, saved version 0.
export function liveApply(applies: Applies) {
  return applies.items.find((apply) => apply.id === applies.pointers.live) ?? null;
}

export type Notice =
  | { kind: 'awaiting' | 'activating'; apply: Apply }
  | { kind: 'failed' | 'rolled_back'; apply: Apply }
  | { kind: 'unapplied' }
  | null;

export function pendingNotice({ applies, head }: ChangeState): Notice {
  const latest = applies.items[0];
  if (latest?.state === 'awaiting_confirmation') return { kind: 'awaiting', apply: latest };
  if (latest?.state === 'activating') return { kind: 'activating', apply: latest };
  if (latest && (latest.state === 'failed' || latest.state === 'rolled_back') && !newer(head, latest.savedVersion)) return { kind: latest.state, apply: latest };
  if (newer(head, liveApply(applies)?.savedVersion ?? '0')) return { kind: 'unapplied' };
  return null;
}

export const stateLabels: Record<Apply['state'], string> = {
  activating: 'Activating',
  awaiting_confirmation: 'Awaiting confirmation',
  confirmed: 'Confirmed',
  rolled_back: 'Rolled back',
  failed: 'Failed',
};

// describe is the server's answer about one apply, as the page reports it.
export function describe(result: Apply) {
  return `Apply ${result.id}: ${stateLabels[result.state]}.${result.reason ? ` ${result.reason}` : ''}`;
}

export function clock(iso: string) {
  return new Date(iso).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });
}

type Declaration = Record<string, unknown>;

// objectAt finds one object of a declaration view. A change whose ID is its collection's name
// is a singleton (management, dns, nat.outbound) or the order of an ordered list.
export function objectAt(declaration: Declaration, collection: string, id: string): unknown {
  const path = collection === 'addresses' || collection === 'services' ? ['objects', collection] : collection.split('.');
  let node: unknown = declaration;
  for (const key of path) node = node && typeof node === 'object' ? (node as Declaration)[key] : undefined;
  const items = Array.isArray(node) ? (node as Array<{ id?: string }>) : undefined;
  if (id === collection) return items ? items.map((item) => item.id) : node;
  return items?.find((item) => item.id === id);
}

export interface ObjectDifference {
  collection: string;
  id: string;
  label: string;
  kind: 'created' | 'changed' | 'deleted';
  before: unknown;
  after: unknown;
}

// differences compares each changed object between two declaration views, for display only.
// Objects whose value ends where it started (created then deleted, edited back) are left out.
export function differences(live: Declaration, head: Declaration, changes: Array<{ collection: string; id: string }>) {
  const seen = new Set<string>();
  const result: ObjectDifference[] = [];
  for (const { collection, id } of changes) {
    const key = JSON.stringify([collection, id]);
    if (seen.has(key)) continue;
    seen.add(key);
    const before = objectAt(live, collection, id);
    const after = objectAt(head, collection, id);
    if (JSON.stringify(before) === JSON.stringify(after)) continue;
    const named = (after ?? before) as { name?: unknown } | undefined;
    const label = id === collection ? (Array.isArray(after ?? before) ? `${collection} order` : collection) : typeof named?.name === 'string' ? named.name : id;
    result.push({ collection, id, label, kind: before === undefined ? 'created' : after === undefined ? 'deleted' : 'changed', before, after });
  }
  return result;
}

// useCountdown is display only: the server owns the deadline and the rollback. The clock is an
// external store: its time is taken when a deadline appears and on each tick, so the first render
// is already right and every snapshot read between ticks returns the same value. Seconds left
// round down, so a countdown never shows time that is not left.
export function useCountdown(deadline: string | undefined) {
  const clock = useRef({ deadline: '', now: 0 });
  const subscribe = useCallback((tick: () => void) => {
    if (!deadline) return () => {};
    const timer = setInterval(() => {
      clock.current = { deadline, now: Date.now() };
      tick();
    }, 1000);
    return () => clearInterval(timer);
  }, [deadline]);
  const snapshot = () => {
    if (!deadline) return 0;
    if (clock.current.deadline !== deadline) clock.current = { deadline, now: Date.now() };
    return clock.current.now;
  };
  const now = useSyncExternalStore(subscribe, snapshot);
  if (!deadline) return '';
  const seconds = Math.max(0, Math.floor((Date.parse(deadline) - now) / 1000));
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`;
}
