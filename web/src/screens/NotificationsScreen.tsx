import { Alert, Anchor, Badge, Button, Group, Loader, Paper, Stack, Tabs, Text, Title } from '@mantine/core';
import { useState } from 'react';
import { Link } from 'react-router';
import { ApiClient, ApiError, reportError } from '../api';
import { isSafeAppLink, severityColor } from '../notifications';
import { formatTimestamp, useTimeZone } from '../timezone';
import type { Notification, NotificationList, NotificationSeverity, Session } from '../types';
import type { Resource } from '../useResource';
import { Loaded } from './Loaded';
import { NotificationChannelsPanel } from './NotificationChannelsPanel';

interface NotificationsScreenProps {
  api: ApiClient;
  session: Session;
  notifications: Resource<NotificationList | undefined>;
  onSaved?(): void;
  onSessionLost(): void;
}

const widgetLimit = 5;

function SeverityBadge({ severity }: { severity: NotificationSeverity }) {
  return <Badge size="sm" className="plain-badge" color={severityColor[severity]} variant={severity === 'critical' ? 'filled' : 'light'}>{severity}</Badge>;
}

export function NotificationsScreen({ api, session, notifications, onSaved, onSessionLost }: NotificationsScreenProps) {
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

  const activeContent = (
    <Stack>
      {notifications.error && <Alert color="orange" role="alert" title="The notifications could not be read">{notifications.error}</Alert>}
      {dismissError && <Alert color="red" role="alert" title="Could not dismiss the notification">{dismissError}</Alert>}
      {notifications.data === undefined
        ? (!notifications.error && <Loader aria-label="Loading notifications" />)
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
