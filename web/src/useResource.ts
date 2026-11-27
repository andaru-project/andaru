import { createContext, useCallback, useContext, useEffect, useRef, useState } from 'react';
import { ApiError, apiErrorMessage } from './api';

export const ConnectionContext = createContext(true);

export function useConnected(): boolean {
  return useContext(ConnectionContext);
}

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
// Pass reloadOnReconnect=true for shell-level resources that should always re-fetch when the connection returns.
export function useResource<T>(load: () => Promise<T>, onSessionLost: () => void, reloadOnReconnect = false): Resource<T> {
  const connected = useConnected();
  const [state, setState] = useState<{ data?: T; error: string; disconnected: boolean; loading: boolean }>({
    error: '',
    disconnected: false,
    loading: true,
  });
  const [prevLoad, setPrevLoad] = useState(() => load);
  if (load !== prevLoad) {
    setPrevLoad(() => load);
    if (!state.loading) setState((current) => ({ ...current, loading: true }));
  }
  const [prevConnected, setPrevConnected] = useState(connected);
  if (connected !== prevConnected) {
    setPrevConnected(connected);
    if (connected && state.disconnected) {
      setState((current) => (current.disconnected ? { ...current, error: '', disconnected: false } : current));
    }
  }
  const latest = useRef(0);
  const out = useRef(0);
  const hasError = useRef(false);

  const fetchInto = useCallback(() => {
    const request = ++latest.current;
    out.current++;
    let pending: Promise<T>;
    try {
      pending = load();
    } catch (error) {
      pending = Promise.reject(error);
    }
    return pending.then(
      (data) => {
        if (request === latest.current) {
          hasError.current = false;
          setState({ data, error: '', disconnected: false, loading: false });
        }
      },
      (error: unknown) => {
        if (error instanceof ApiError && error.status === 401) onSessionLost();
        if (request === latest.current) {
          hasError.current = true;
          const message = apiErrorMessage(error);
          const disconnected = error instanceof ApiError && error.status === 0;
          setState((current) => ({ ...current, error: message, disconnected, loading: false }));
        }
      },
    ).finally(() => {
      out.current--;
    });
  }, [load, onSessionLost]);
  const reload = useCallback(() => {
    setState((current) => ({ ...current, loading: true }));
    return fetchInto();
  }, [fetchInto]);
  const refresh = useCallback(() => { if (out.current === 0) void reload(); }, [reload]);

  useEffect(() => { void fetchInto(); }, [fetchInto]);

  // Re-fetch on reconnect: always when reloadOnReconnect is set, or whenever the last read errored
  // (stale disconnect errors are already cleared during render above so they never flash).
  const wasConnected = useRef(connected);
  useEffect(() => {
    if (connected && !wasConnected.current && (reloadOnReconnect || (hasError.current && out.current === 0))) {
      void fetchInto();
    }
    wasConnected.current = connected;
  }, [connected, fetchInto, reloadOnReconnect]);

  return { data: state.data, error: state.error, loading: state.loading, reload, refresh };
}

// usePolledResource is a resource that is read again on a timer while the tab is visible.
export function usePolledResource<T>(load: () => Promise<T>, onSessionLost: () => void, milliseconds: number) {
  const resource = useResource(load, onSessionLost);
  useVisiblePoll(resource.refresh, milliseconds);
  return resource;
}

// useVisiblePoll calls reload on a timer and when the tab becomes visible. While hidden it ticks only when
// includeHidden is set (used to retry failed notification reads when background browser alerts are active).
export function useVisiblePoll(reload: () => void, milliseconds: number, enabled = true, includeHidden = false) {
  useEffect(() => {
    if (!enabled) return;
    const poll = () => { if (includeHidden || document.visibilityState === 'visible') reload(); };
    const onVisible = () => { if (document.visibilityState === 'visible') reload(); };
    const timer = setInterval(poll, milliseconds);
    document.addEventListener('visibilitychange', onVisible);
    return () => {
      clearInterval(timer);
      document.removeEventListener('visibilitychange', onVisible);
    };
  }, [enabled, includeHidden, milliseconds, reload]);
}

const reopenMilliseconds = 15_000;

// eventType reads the bus event type from an SSE message's JSON payload. An empty or unreadable
// message returns undefined so callers fall back to refreshing all subscribed state.
function eventType(data: string): string | undefined {
  try {
    const type: unknown = JSON.parse(data)?.type;
    return typeof type === 'string' ? type : undefined;
  } catch {
    return undefined;
  }
}

// useEvents calls onEvent when the engine's event stream opens and at each event on it, in place of a poll: the
// events say only that something changed, and onEvent fetches what. On open or a stream failure (once until it opens
// again) type is undefined so every subscribed resource refreshes; on a typed event, type names the bus event so
// only what changed reloads. The browser reopens a stream that drops; one the server refuses (the session ended,
// the engine is down) is opened again 15 seconds later. A hidden tab normally holds no stream (browsers cap
// connections per host), unless keepOpenWhenHidden is set so background browser notifications arrive in real time.
export function useEvents(onEvent: (type?: string) => void, enabled = true, keepOpenWhenHidden = false) {
  const keepOpenRef = useRef(keepOpenWhenHidden);
  const syncRef = useRef<(() => void) | null>(null);

  useEffect(() => {
    keepOpenRef.current = keepOpenWhenHidden;
    syncRef.current?.();
  }, [keepOpenWhenHidden]);

  useEffect(() => {
    if (!enabled) return;
    let source: EventSource | undefined;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let failed = false;
    const close = () => {
      source?.close();
      source = undefined;
      clearTimeout(timer);
      timer = undefined;
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
        if (opened.readyState === EventSource.CLOSED) {
          source = undefined;
          clearTimeout(timer);
          timer = setTimeout(open, reopenMilliseconds);
        }
      };
      source = opened;
    };
    const sync = () => {
      if (keepOpenRef.current || document.visibilityState === 'visible') {
        if (!source && timer === undefined) open();
      } else {
        close();
      }
    };
    syncRef.current = sync;
    sync();
    document.addEventListener('visibilitychange', sync);
    return () => {
      syncRef.current = null;
      document.removeEventListener('visibilitychange', sync);
      close();
    };
  }, [enabled, onEvent]);
}
