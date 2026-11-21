import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError, apiErrorMessage } from './api';

export interface Resource<T> {
  data?: T;
  error: string;
  loading: boolean;
  reload(): Promise<void>;
}

// useResource loads data with the given function on mount and whenever the function changes.
// A failed reload keeps the last data, so a poll that loses the connection does not blank the page.
export function useResource<T>(load: () => Promise<T>, onSessionLost: () => void): Resource<T> {
  const [state, setState] = useState<{ data?: T; error: string; loading: boolean }>({ error: '', loading: true });
  const latest = useRef(0);

  const reload = useCallback(async () => {
    const request = ++latest.current;
    setState((current) => ({ ...current, loading: true }));
    try {
      const data = await load();
      if (request === latest.current) setState({ data, error: '', loading: false });
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) onSessionLost();
      if (request === latest.current) setState((current) => ({ ...current, error: apiErrorMessage(error), loading: false }));
    }
  }, [load, onSessionLost]);

  useEffect(() => { void reload(); }, [reload]);

  return { ...state, reload };
}

// useVisiblePoll calls reload on a timer and when the tab becomes visible, and never while it is hidden.
export function useVisiblePoll(reload: () => unknown, milliseconds: number, enabled = true) {
  useEffect(() => {
    if (!enabled) return;
    const visible = () => { if (document.visibilityState === 'visible') void reload(); };
    const timer = setInterval(visible, milliseconds);
    document.addEventListener('visibilitychange', visible);
    return () => {
      clearInterval(timer);
      document.removeEventListener('visibilitychange', visible);
    };
  }, [enabled, milliseconds, reload]);
}
