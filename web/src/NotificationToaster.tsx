import { Badge, CloseButton, Group, Text } from '@mantine/core';
import { useEffect, useRef } from 'react';
import { useNavigate } from 'react-router';
import {
  clearNotificationToasts,
  diffNotifications,
  dismissNotificationToast,
  getNotificationPrefs,
  isSafeAppLink,
  pauseNotificationToast,
  pushNotificationToast,
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
const maxVisibleToasts = 4;
const maxBrowserBatch = 3;

export function NotificationToaster({ notifications, hostname }: NotificationToasterProps) {
  const navigate = useNavigate();
  const toasts = useNotificationToasts();
  const seenRef = useRef<Map<number, string> | null>(null);
  const nativeRef = useRef<Map<number, globalThis.Notification>>(new Map());
  const lastActivityRef = useRef(0);
  const hostnameRef = useRef(hostname);

  useEffect(() => {
    hostnameRef.current = hostname;
  }, [hostname]);

  // Track recent interaction in this tab so active viewers get in-page toasts while backgrounded/idle tabs use OS alerts.
  useEffect(() => {
    lastActivityRef.current = Date.now();
    const markActive = () => { lastActivityRef.current = Date.now(); };
    const onVisibility = () => {
      if (document.visibilityState === 'visible') markActive();
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
    const nextSeen = new Map<number, string>();
    for (const item of notifications.items) nextSeen.set(item.id, item.updated);

    if (seenRef.current === null) {
      seenRef.current = nextSeen;
      return;
    }

    const changed = diffNotifications(seenRef.current, notifications.items);
    const removedIds: number[] = [];
    for (const id of seenRef.current.keys()) {
      if (!nextSeen.has(id)) removedIds.push(id);
    }
    seenRef.current = nextSeen;

    for (const id of removedIds) {
      const notice = nativeRef.current.get(id);
      if (notice) {
        notice.close();
        nativeRef.current.delete(id);
      }
      dismissNotificationToast(id);
    }

    if (changed.length === 0) return;

    const prefs = getNotificationPrefs();
    const activeInTab = document.visibilityState === 'visible' && Date.now() - lastActivityRef.current < activeIdleMs;

    if (prefs.browser && !activeInTab) {
      // Iterate oldest-to-newest within the capped batch so newest lands last in any fallback toast stack.
      const batch = changed.slice(0, maxBrowserBatch).reverse();
      for (const item of batch) {
        const prior = nativeRef.current.get(item.id);
        if (prior) {
          prior.close();
          nativeRef.current.delete(item.id);
        }
        const notice = showBrowserNotification(item, {
          hostname: hostnameRef.current,
          onOpen: (target) => { void navigate(target); },
        });
        if (notice) {
          nativeRef.current.set(item.id, notice);
          notice.onclose = () => {
            if (nativeRef.current.get(item.id) === notice) nativeRef.current.delete(item.id);
          };
        } else if (prefs.toasts) {
          pushNotificationToast({
            id: item.id,
            severity: item.severity,
            subject: item.subject,
            detail: item.detail,
            source: item.source,
            link: item.link,
          });
        }
      }
      return;
    }

    if (!prefs.toasts) return;
    const batch = changed.slice(0, maxVisibleToasts).reverse();
    for (const item of batch) {
      pushNotificationToast({
        id: item.id,
        severity: item.severity,
        subject: item.subject,
        detail: item.detail,
        source: item.source,
        link: item.link,
      });
    }
  }, [notifications, navigate]);

  if (toasts.length === 0) return null;

  function openToast(toast: ToastMessage) {
    dismissNotificationToast(toast.id);
    void navigate(isSafeAppLink(toast.link) ? toast.link : '/system/notifications');
  }

  return (
    <section className="notification-toast-stack" aria-label="Live notifications">
      {toasts.map((toast) => (
        <div
          key={toast.id}
          className="notification-toast"
          data-severity={toast.severity}
          role={toast.severity === 'critical' ? 'alert' : 'status'}
          aria-live={toast.severity === 'critical' ? 'assertive' : 'polite'}
          onMouseEnter={() => pauseNotificationToast(toast.id)}
          onMouseLeave={() => resumeNotificationToast(toast.id)}
          onFocus={() => pauseNotificationToast(toast.id)}
          onBlur={(event) => {
            if (!event.currentTarget.contains(event.relatedTarget as Node | null)) {
              resumeNotificationToast(toast.id);
            }
          }}
        >
          <button type="button" className="notification-toast-body" onClick={() => openToast(toast)}>
            <Group gap="xs" wrap="nowrap" align="center">
              <Badge
                size="xs"
                className="plain-badge"
                color={severityColor[toast.severity]}
                variant={toast.severity === 'critical' ? 'filled' : 'light'}
              >
                {toast.severity}
              </Badge>
              <Text size="sm" fw={600} className="notification-toast-subject">{toast.subject}</Text>
            </Group>
            {toast.detail && (
              <Text size="xs" c="dimmed" className="notification-toast-detail">{toast.detail}</Text>
            )}
            {toast.source && (
              <Text size="xs" c="dimmed">{toast.source}</Text>
            )}
          </button>
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
