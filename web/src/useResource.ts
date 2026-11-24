import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError, apiErrorMessage } from './api';

export interface Resource<T> {
  data?: T;
  error: string;
  loading: boolean;
  reload(): Promise<void>;
  // refresh is a reload a poll may ask for: it does nothing while a request is still out, so answers slower than
  // the poll are not thrown away one after another.
  refresh(): void;
}

// useResource loads data with the given function on mount and whenever the function changes.
// A failed reload keeps the last data, so a poll that loses the connection does not blank the page.
export function useResource<T>(load: () => Promise<T>, onSessionLost: () => void): Resource<T> {
  const [state, setState] = useState<{ data?: T; error: string; loading: boolean }>({ error: '', loading: true });
  const latest = useRef(0);
  const out = useRef(0);

  const reload = useCallback(async () => {
    const request = ++latest.current;
    setState((current) => ({ ...current, loading: true }));
    out.current++;
    try {
      const data = await load();
      if (request === latest.current) setState({ data, error: '', loading: false });
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) onSessionLost();
      if (request === latest.current) setState((current) => ({ ...current, error: apiErrorMessage(error), loading: false }));
    } finally {
      out.current--;
    }
  }, [load, onSessionLost]);
  const refresh = useCallback(() => { if (out.current === 0) void reload(); }, [reload]);

  useEffect(() => { void reload(); }, [reload]);

  return { ...state, reload, refresh };
}

// usePolledResource is a resource that is read again on a timer while the tab is visible.
export function usePolledResource<T>(load: () => Promise<T>, onSessionLost: () => void, milliseconds: number) {
  const resource = useResource(load, onSessionLost);
  useVisiblePoll(resource.refresh, milliseconds);
  return resource;
}

// useVisiblePoll calls reload on a timer and when the tab becomes visible, and never while it is hidden.
export function useVisiblePoll(reload: () => void, milliseconds: number, enabled = true) {
  useEffect(() => {
    if (!enabled) return;
    const visible = () => { if (document.visibilityState === 'visible') reload(); };
    const timer = setInterval(visible, milliseconds);
    document.addEventListener('visibilitychange', visible);
    return () => {
      clearInterval(timer);
      document.removeEventListener('visibilitychange', visible);
    };
  }, [enabled, milliseconds, reload]);
}

const reopenMilliseconds = 15_000;

// eventType reads the bus event type from an SSE message's JSON payload. An empty or unreadable
// message returns undefined so callers fall back to refreshing all subscribed state.
function eventType(data: unknown): string | undefined {
  if (typeof data !== 'string' || data === '') return undefined;
  try {
    const parsed: unknown = JSON.parse(data);
    if (parsed && typeof parsed === 'object' && 'type' in parsed && typeof parsed.type === 'string') return parsed.type;
  } catch {
    return undefined;
  }
  return undefined;
}

// useEvents calls onEvent when the engine's event stream opens and at each event on it, in place of a poll: the
// events say only that something changed, and onEvent fetches what. On open or a stream failure (once until it opens
// again) type is undefined so every subscribed resource refreshes; on a typed event, type names the bus event so
// only what changed reloads. The browser reopens a stream that drops; one the server refuses (the session ended,
// the engine is down) is opened again 15 seconds later. A hidden tab holds no stream: a browser allows six
// connections to a host, and each stream keeps one.
export function useEvents(onEvent: (type?: string) => void, enabled = true) {
  useEffect(() => {
    if (!enabled) return;
    let source: EventSource | undefined;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let failed = false;
    const close = () => {
      source?.close();
      source = undefined;
      clearTimeout(timer);
    };
    const open = () => {
      close();
      const opened = new EventSource('/api/v1/events');
      opened.onopen = () => {
        failed = false;
        onEvent();
      };
      opened.onmessage = (event) => {
        onEvent(eventType(event.data));
      };
      opened.onerror = () => {
        if (!failed) onEvent();
        failed = true;
        if (opened.readyState === EventSource.CLOSED) timer = setTimeout(open, reopenMilliseconds);
      };
      source = opened;
    };
    const follow = () => { if (document.visibilityState === 'visible') open(); else close(); };
    follow();
    document.addEventListener('visibilitychange', follow);
    return () => {
      document.removeEventListener('visibilitychange', follow);
      close();
    };
  }, [enabled, onEvent]);
}
