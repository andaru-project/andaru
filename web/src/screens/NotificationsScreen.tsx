import { Alert, Anchor, Badge, Button, Group, Loader, Paper, Stack, Switch, Tabs, Text, Title } from '@mantine/core';
import { useState } from 'react';
import { Link, useNavigate } from 'react-router';
import { ApiClient, ApiError, reportError } from '../api';
import {
  isSafeAppLink,
  pushNotificationToast,
  requestBrowserNotificationPermission,
  setNotificationPrefs,
  severityColor,
  showBrowserNotification,
  useBrowserNotificationSupport,
  useNotificationPrefs,
  type BrowserNotificationSupport,
} from '../notifications';
import { formatTimestamp, useTimeZone } from '../timezone';
import type { Notification, NotificationList, NotificationSeverity, Session } from '../types';
import { useConnected, type Resource } from '../useResource';
import { Loaded } from './Loaded';
import { NotificationChannelsPanel } from './NotificationChannelsPanel';

interface NotificationsScreenProps {
  api: ApiClient;
  session: Session;
  notifications: Resource<NotificationList | undefined>;
  hostname?: string;
  onSaved?(): void;
  onSessionLost(): void;
}

const widgetLimit = 5;

const browserSupportHint: Partial<Record<BrowserNotificationSupport, string>> = {
  insecure: 'Browser notifications require HTTPS (or localhost). In-page alerts still work here.',
  unsupported: 'This browser does not support native notifications. In-page alerts still work here.',
  denied: "Browser notifications are blocked for this site. Allow them in your browser's site settings to turn them on.",
};

function SeverityBadge({ severity }: { severity: NotificationSeverity }) {
  return <Badge size="sm" className="plain-badge" color={severityColor[severity]} variant={severity === 'critical' ? 'filled' : 'light'}>{severity}</Badge>;
}

function BrowserAlertsCard({ hostname }: { hostname?: string }) {
  const navigate = useNavigate();
  const prefs = useNotificationPrefs();
  const support = useBrowserNotificationSupport();
  const [requesting, setRequesting] = useState(false);
  const [testNote, setTestNote] = useState('');

  const browserEnabled = prefs.browser && support === 'granted';
  const browserUnavailable = support === 'insecure' || support === 'unsupported' || support === 'denied';
  const hint = browserSupportHint[support];

  async function toggleBrowser(enabled: boolean) {
    setTestNote('');
    if (!enabled) {
      setNotificationPrefs({ browser: false });
      return;
    }
    setRequesting(true);
    try {
      // Request permission directly inside the click handler so Safari allows the prompt.
      const next = await requestBrowserNotificationPermission();
      setNotificationPrefs({ browser: next === 'granted' });
    } finally {
      setRequesting(false);
    }
  }

  function sendTest() {
    setTestNote('');
    const now = Date.now();
    const item = {
      id: -now,
      severity: 'info' as const,
      source: 'browser',
      subject: 'Test notification',
      detail: 'Notifications are working in this browser.',
      link: '/system/notifications',
    };
    if (browserEnabled) {
      const notice = showBrowserNotification(item, {
        hostname,
        onOpen: (target) => { void navigate(target); },
      });
      if (notice) {
        if (prefs.toasts) {
          pushNotificationToast({ ...item, id: `test-${now}`, detail: 'Sent a test browser notification.' });
        } else {
          setTestNote('Test browser notification sent.');
        }
        return;
      }
      setTestNote('This browser blocked native notifications here. In-page alerts will be used instead.');
    }
    if (prefs.toasts) {
      pushNotificationToast({ ...item, id: `test-${now}` });
    }
  }

  return (
    <Paper withBorder p="md">
      <Stack gap="xs">
        <Group justify="space-between" align="center" wrap="wrap" gap="xs">
          <Text fw={600} size="sm">This browser</Text>
          <Button
            size="xs"
            variant="default"
            disabled={!prefs.toasts && !browserEnabled}
            onClick={sendTest}
          >
            Send test
          </Button>
        </Group>
        <Group gap="lg" wrap="wrap" align="flex-start">
          <Switch
            label="In-page alerts"
            description="Pop up new alerts while this tab is open"
            checked={prefs.toasts}
            onChange={(event) => {
              setTestNote('');
              setNotificationPrefs({ toasts: event.currentTarget.checked });
            }}
          />
          <Switch
            label="Browser notifications"
            description="Use OS alerts when this tab is in the background or idle"
            checked={browserEnabled}
            disabled={requesting || browserUnavailable}
            onChange={(event) => void toggleBrowser(event.currentTarget.checked)}
          />
        </Group>
        {hint && <Text size="xs" c={support === 'denied' ? undefined : 'dimmed'} className={support === 'denied' ? 'warning-text' : undefined}>{hint}</Text>}
        {testNote && <Text size="xs" c="dimmed">{testNote}</Text>}
      </Stack>
    </Paper>
  );
}

export function NotificationsScreen({ api, session, notifications, hostname, onSaved, onSessionLost }: NotificationsScreenProps) {
  const connected = useConnected();
  const timeZone = useTimeZone();
  const canReadActive = session.capabilities.includes('notifications.read');
  const canReadChannels = session.capabilities.includes('configuration.read');
  const canManage = session.capabilities.includes('notifications.manage');
  const [tab, setTab] = useState<'active' | 'channels'>(canReadActive ? 'active' : 'channels');
  const [dismissing, setDismissing] = useState<number | null>(null);
  const [dismissError, setDismissError] = useState('');
  const items = notifications.data?.items ?? [];

  async function dismiss(item: Notification) {
    setDismissing(item.id);
    setDismissError('');
    try {
      await api.dismissNotification(item.id);
    } catch (error) {
      // Not found is gone already, or no longer the caller's to see: the list read next says which.
      if (!(error instanceof ApiError && error.status === 404)) {
        reportError(error, onSessionLost, setDismissError);
        if (error instanceof ApiError && error.status === 401) {
          setDismissing(null);
          return;
        }
      }
    }
    // The change event reloads the list too, but the stream may be down. The button stays busy until the list is read,
    // so a dismissed notification is not offered again.
    await notifications.reload();
    setDismissing(null);
  }

  const showNotificationsError = Boolean(notifications.error) && (connected || notifications.data === undefined);
  const activeContent = (
    <Stack>
      <BrowserAlertsCard hostname={hostname} />
      {showNotificationsError && <Alert color="orange" role="alert" title="The notifications could not be read">{notifications.error}</Alert>}
      {dismissError && <Alert color="red" role="alert" title="Could not dismiss the notification">{dismissError}</Alert>}
      {notifications.data === undefined
        ? (!showNotificationsError && <Loader aria-label="Loading notifications" />)
        : items.length === 0
          ? <Alert role="status">No active notifications.</Alert>
          : (
            <Stack gap="sm">
              {items.map((item) => (
                <Paper key={item.id} component="article" withBorder p="md">
                  <Stack gap="xs">
                    <Group justify="space-between" align="flex-start" wrap="nowrap">
                      <Group gap="xs" wrap="wrap">
                        <SeverityBadge severity={item.severity} />
                        <Text fw={600}>{item.subject}</Text>
                      </Group>
                      {canManage && (
                        <Button
                          size="xs"
                          variant="default"
                          loading={dismissing === item.id}
                          disabled={dismissing !== null}
                          aria-label={`Dismiss ${item.subject}`}
                          onClick={() => void dismiss(item)}
                        >
                          Dismiss
                        </Button>
                      )}
                    </Group>
                    <Group gap="xs" wrap="wrap">
                      <Text size="xs" c="dimmed" className="tabular">{formatTimestamp(item.updated, timeZone)} · {item.source}</Text>
                      {isSafeAppLink(item.link) && (
                        <Anchor component={Link} to={item.link} size="xs" aria-label={`Open ${item.subject}`}>
                          Open <span aria-hidden="true">→</span>
                        </Anchor>
                      )}
                    </Group>
                    {item.detail && <Text size="sm" className="notification-detail">{item.detail}</Text>}
                  </Stack>
                </Paper>
              ))}
            </Stack>
          )}
    </Stack>
  );

  const showTabs = canReadActive && canReadChannels;
  const activeTab = showTabs ? tab : canReadActive ? 'active' : 'channels';

  return (
    <Stack>
      <Group justify="space-between" align="flex-end">
        <div>
          <Title order={2}>Notifications</Title>
          <Text c="dimmed">
            {activeTab === 'channels'
              ? 'Delivery channels configured to forward appliance alerts.'
              : 'Active alerts and notices from the appliance, newest first.'}
          </Text>
        </div>
        {activeTab === 'active' && (
          <Button size="xs" variant="default" loading={notifications.loading && items.length > 0} onClick={() => { setDismissError(''); void notifications.reload(); }}>Refresh</Button>
        )}
      </Group>

      {showTabs ? (
        <Tabs value={tab} onChange={(value) => setTab(value === 'channels' ? 'channels' : 'active')}>
          <Tabs.List mb="md">
            <Tabs.Tab value="active">Active</Tabs.Tab>
            <Tabs.Tab value="channels">Channels</Tabs.Tab>
          </Tabs.List>
          <Tabs.Panel value="active">{activeContent}</Tabs.Panel>
          <Tabs.Panel value="channels">
            {tab === 'channels' && (
              <NotificationChannelsPanel api={api} canManage={canManage} onSaved={onSaved} onSessionLost={onSessionLost} />
            )}
          </Tabs.Panel>
        </Tabs>
      ) : canReadChannels && !canReadActive ? (
        <NotificationChannelsPanel api={api} canManage={canManage} onSaved={onSaved} onSessionLost={onSessionLost} />
      ) : (
        activeContent
      )}
    </Stack>
  );
}

export function NotificationsWidget({ resource }: { resource: Resource<NotificationList | undefined> }) {
  const timeZone = useTimeZone();
  return (
    <Loaded resource={resource} what="notifications">
      {(list) => {
        if (!list) return null;
        const shown = list.items.slice(0, widgetLimit);
        return shown.length === 0 ? <Text size="sm" c="dimmed">No active notifications.</Text> : (
          <Stack gap="xs">
            {shown.map((item) => (
              <div key={item.id}>
                <Group justify="space-between" gap="xs" wrap="nowrap" align="flex-start">
                  <Text size="sm" fw={600}>{item.subject}</Text>
                  <SeverityBadge severity={item.severity} />
                </Group>
                <Text size="xs" c="dimmed" className="tabular">{formatTimestamp(item.updated, timeZone)} · {item.source}</Text>
              </div>
            ))}
            {list.items.length > widgetLimit && <Text size="xs" c="dimmed">and {list.items.length - widgetLimit} more</Text>}
          </Stack>
        );
      }}
    </Loaded>
  );
}
