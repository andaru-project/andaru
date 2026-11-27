import { Badge, CloseButton, Group, Text } from '@mantine/core';
import { useEffect, useRef } from 'react';
import { useNavigate } from 'react-router';
import {
  clearNotificationToasts,
  diffNotifications,
  dismissNotificationToast,
  getBrowserNotificationSupport,
  getNotificationPrefs,
  isSafeAppLink,
  maxVisibleToasts,
  pauseNotificationToast,
  pushNotificationToast,
  resumeHiddenNotificationToasts,
  resumeNotificationToast,
  severityColor,
  showBrowserNotification,
  useNotificationToasts,
  type ToastMessage,
} from './notifications';
import type { NotificationList } from './types';

interface NotificationToasterProps {
  notifications?: NotificationList;
  hostname?: string;
}

const activeIdleMs = 3 * 60_000;
const maxBrowserBatch = 3;

function isHovered(element: HTMLElement): boolean {
  try {
    return element.matches(':hover');
  } catch {
    return false;
  }
}

export function NotificationToaster({ notifications, hostname }: NotificationToasterProps) {
  const navigate = useNavigate();
  const navigateRef = useRef(navigate);
  const toasts = useNotificationToasts();
  const seenRef = useRef<Map<number, string> | null>(null);
  const nativeRef = useRef<Map<number, globalThis.Notification>>(new Map());
  const lastActivityRef = useRef(0);

  useEffect(() => {
    navigateRef.current = navigate;
  }, [navigate]);

  // Track recent interaction in this tab so active viewers get in-page toasts while backgrounded/idle tabs use OS alerts.
  // Returning to a visible tab also starts timers for any fallback toasts queued while hidden.
  useEffect(() => {
    lastActivityRef.current = Date.now();
    const markActive = () => { lastActivityRef.current = Date.now(); };
    const onVisibility = () => {
      if (document.visibilityState === 'visible') {
        markActive();
        resumeHiddenNotificationToasts();
      }
    };
    window.addEventListener('pointerdown', markActive, { passive: true });
    window.addEventListener('keydown', markActive, { passive: true });
    window.addEventListener('focus', markActive);
    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      window.removeEventListener('pointerdown', markActive);
      window.removeEventListener('keydown', markActive);
      window.removeEventListener('focus', markActive);
      document.removeEventListener('visibilitychange', onVisibility);
    };
  }, []);

  // Clear open toasts and OS notification handles on unmount (e.g. sign-out).
  useEffect(() => {
    const openNative = nativeRef.current;
    return () => {
      clearNotificationToasts();
      for (const notice of openNative.values()) notice.close();
      openNative.clear();
    };
  }, []);

  // Diff each notifications snapshot against the previous one. The first loaded snapshot seeds seenRef silently so
  // refreshing or signing in never replays alerts that were already active.
  useEffect(() => {
    if (!notifications) return;
    const nextSeen = new Map(notifications.items.map((item) => [item.id, item.updated]));
    const previous = seenRef.current;
    seenRef.current = nextSeen;

    if (previous === null) return;

    const closeNative = (id: number) => {
      const notice = nativeRef.current.get(id);
      if (notice) {
        notice.close();
        nativeRef.current.delete(id);
      }
    };

    for (const id of previous.keys()) {
      if (!nextSeen.has(id)) {
        closeNative(id);
        dismissNotificationToast(id);
      }
    }

    const changed = diffNotifications(previous, notifications.items);
    if (changed.length === 0) return;

    const prefs = getNotificationPrefs();
    const activeInTab = document.visibilityState === 'visible' && Date.now() - lastActivityRef.current < activeIdleMs;
    const useBrowser = prefs.browser && getBrowserNotificationSupport() === 'granted' && !activeInTab;

    if (!useBrowser && !prefs.toasts) return;

    // Iterate oldest-to-newest within the capped batch so newest lands last in the toast stack.
    const limit = useBrowser ? maxBrowserBatch : maxVisibleToasts;
    const batch = changed.slice(0, limit).reverse();
    for (const item of batch) {
      closeNative(item.id);
      if (useBrowser) {
        const notice = showBrowserNotification(item, {
          hostname,
          onOpen: (target) => { void navigateRef.current(target); },
        });
        if (notice) {
          nativeRef.current.set(item.id, notice);
          notice.onclose = () => {
            if (nativeRef.current.get(item.id) === notice) nativeRef.current.delete(item.id);
          };
          continue;
        }
      }
      if (prefs.toasts) pushNotificationToast(item);
    }
  }, [notifications, hostname]);

  function openToast(toast: ToastMessage) {
    dismissNotificationToast(toast.id);
    void navigate(isSafeAppLink(toast.link) ? toast.link : '/system/notifications');
  }

  // Keep the stack container mounted so live-region insertions inside it are announced reliably by screen readers.
  return (
    <section className="notification-toast-stack" aria-label="Live notifications">
      {toasts.map((toast) => (
        <div
          key={toast.id}
          className="notification-toast"
          data-severity={toast.severity}
          onMouseEnter={() => pauseNotificationToast(toast.id)}
          onMouseLeave={(event) => {
            if (!event.currentTarget.contains(document.activeElement)) {
              resumeNotificationToast(toast.id);
            }
          }}
          onFocus={() => pauseNotificationToast(toast.id)}
          onBlur={(event) => {
            if (
              !event.currentTarget.contains(event.relatedTarget as Node | null) &&
              !isHovered(event.currentTarget)
            ) {
              resumeNotificationToast(toast.id);
            }
          }}
        >
          <div className="notification-toast-content" role={toast.severity === 'critical' ? 'alert' : 'status'}>
            <button type="button" className="notification-toast-body" onClick={() => openToast(toast)}>
              <Group component="span" gap="xs" wrap="nowrap" align="center">
                <Badge
                  component="span"
                  size="xs"
                  className="plain-badge"
                  color={severityColor[toast.severity]}
                  variant={toast.severity === 'critical' ? 'filled' : 'light'}
                >
                  {toast.severity}
                </Badge>
                {' '}
                <Text component="span" size="sm" fw={600} className="notification-toast-subject">{toast.subject}</Text>
              </Group>
              {toast.detail && (
                <>
                  {' '}
                  <Text component="span" size="xs" c="dimmed" className="notification-toast-detail">{toast.detail}</Text>
                </>
              )}
              {toast.source && (
                <>
                  {' '}
                  <Text component="span" size="xs" c="dimmed" className="notification-toast-source">{toast.source}</Text>
                </>
              )}
            </button>
          </div>
          <CloseButton
            size="sm"
            className="notification-toast-close"
            aria-label={`Close notification: ${toast.subject}`}
            onClick={() => dismissNotificationToast(toast.id)}
          />
        </div>
      ))}
    </section>
  );
}
