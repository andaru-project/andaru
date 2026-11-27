import { Alert, Anchor, Badge, Button, Menu, Stack, Text } from '@mantine/core';
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Link, Navigate, Route, Routes, useLocation, useMatch, useNavigate } from 'react-router';
import { ApiClient, reportError } from './api';
import { AppFrame } from './AppFrame';
import { loadChangeState, pendingNotice } from './changes';
import { plural } from './format';
import { highestSeverity, severityColor, useBrowserNotificationsActive } from './notifications';
import { NotificationToaster } from './NotificationToaster';
import { PendingNotice } from './PendingNotice';
import { FleetWorkspace } from './netplane/FleetWorkspace';
import { AliasesScreen } from './screens/AliasesScreen';
import { AuditScreen } from './screens/AuditScreen';
import { BackupScreen } from './screens/BackupScreen';
import { ChangesScreen } from './screens/ChangesScreen';
import { DashboardScreen } from './screens/DashboardScreen';
import { DhcpScreen } from './screens/DhcpScreen';
import { DnsScreen } from './screens/DnsScreen';
import { InterfacesScreen } from './screens/InterfacesScreen';
import { Bell } from './screens/Marks';
import { NatScreen } from './screens/NatScreen';
import { NotificationsScreen } from './screens/NotificationsScreen';
import { PasswordDialog } from './screens/PasswordDialog';
import { RolesScreen } from './screens/RolesScreen';
import { RulesScreen } from './screens/RulesScreen';
import { SessionScreen } from './screens/SessionScreen';
import { SetupWizardScreen } from './screens/SetupWizardScreen';
import { TokensScreen } from './screens/TokensScreen';
import { TrafficFlowScreen } from './screens/TrafficFlowScreen';
import { UpgradeScreen } from './screens/UpgradeScreen';
import { UpdatesScreen } from './screens/UpdatesScreen';
import { formatSafeModeReasons, SystemDetailsScreen, updateNotice } from './screens/SystemWidget';
import { UsersScreen } from './screens/UsersScreen';
import { VirtualIpsScreen } from './screens/VirtualIpsScreen';
import { ZonesScreen } from './screens/ZonesScreen';
import type { NotificationList, Session, User } from './types';
import { ConnectionContext, useEvents, useResource, useVisiblePoll, type Resource } from './useResource';

interface ShellProps {
  api: ApiClient;
  session: Session;
  connected: boolean;
  onLogout(): void;
  onSessionLost(): void;
}

const systemPollMilliseconds = 60_000;
const retryMilliseconds = 15_000;

const pageTitles: Record<string, string> = {
  '/': 'Dashboard',
  '/setup': 'Setup assistant',
  '/network/interfaces': 'Interfaces',
  '/network/dhcp': 'DHCP',
  '/network/dns': 'DNS',
  '/network/virtual-ips': 'Virtual IPs',
  '/network/topology': 'Topology',
  '/network/vlans': 'VLANs & Segments',
  '/network/vlans/flow': 'VLAN Flow',
  '/network/devices': 'Devices',
  '/network/clients': 'Clients',
  '/network/diagnostics': 'Diagnostics & events',
  '/diagnostics/traffic-flow': 'Traffic simulator',
  '/firewall/rules': 'Rules',
  '/firewall/nat': 'NAT',
  '/firewall/aliases': 'Aliases',
  '/firewall/zones': 'Zones',
  '/changes': 'Review and apply',
  '/system/details': 'System details',
  '/system/updates': 'Software updates',
  '/system/backups': 'Backup & restore',
  '/system/notifications': 'Notifications',
  '/system/users': 'Users',
  '/system/roles': 'Roles',
  '/system/tokens': 'API tokens',
  '/system/audit': 'Security audit',
  '/system/upgrade': 'Finish the upgrade',
  '/system/session': 'Current session',
};

const pendingBadges: Record<NonNullable<ReturnType<typeof pendingNotice>>['kind'], { color: string; variant: 'light' | 'filled'; label: string }> = {
  unapplied: { color: 'yellow', variant: 'light', label: 'Pending' },
  activating: { color: 'blue', variant: 'light', label: 'Applying' },
  awaiting: { color: 'orange', variant: 'filled', label: 'Confirm' },
  failed: { color: 'red', variant: 'light', label: 'Failed' },
  rolled_back: { color: 'red', variant: 'light', label: 'Rolled back' },
};

function HeaderNotifications({ notifications }: { notifications: Resource<NotificationList | undefined> }) {
  const items = notifications.data?.items;
  const count = items?.length;
  const highest = items ? highestSeverity(items) : undefined;
  // A read that fails after one that answered leaves the count out of date: it is not shown as if it were current.
  const unavailable = notifications.error !== '';
  const active = !unavailable && highest !== undefined;
  const label = unavailable
    ? 'Notifications unavailable'
    : count === undefined
      ? 'Notifications'
      : active
        ? `${plural(count, 'notification')}, highest severity ${highest}`
        : '0 notifications';
  return (
    <Button
      component={Link}
      to="/system/notifications"
      size="xs"
      variant={active ? 'filled' : 'default'}
      color={active ? severityColor[highest] : undefined}
      className={active ? 'notification-button' : 'notification-button notification-neutral'}
      leftSection={<Bell />}
      aria-label={label}
      data-severity={active ? highest : unavailable ? 'unavailable' : 'none'}
    >
      {unavailable ? '—' : count !== undefined ? <span className="tabular">{count}</span> : null}
    </Button>
  );
}

export function Shell(props: ShellProps) {
  return (
    <ConnectionContext.Provider value={props.connected}>
      <ShellBody {...props} />
    </ConnectionContext.Provider>
  );
}

function ShellBody({ api, session, connected, onLogout, onSessionLost }: ShellProps) {
  const capabilities = useMemo(() => new Set(session.capabilities), [session.capabilities]);
  const { pathname } = useLocation();
  const onChangesPage = useMatch('/changes') !== null;
  const navigate = useNavigate();
  const [passwordUser, setPasswordUser] = useState<User | null>(null);
  const [accountError, setAccountError] = useState('');

  const canReadChanges = capabilities.has('configuration.read');
  const loadChanges = useCallback(() => (canReadChanges ? loadChangeState(api) : Promise.resolve(undefined)), [api, canReadChanges]);
  const changes = useResource(loadChanges, onSessionLost, true);
  const reloadChanges = changes.reload;

  const canReadNotifications = capabilities.has('notifications.read');
  const browserAlertsActive = useBrowserNotificationsActive();
  const loadNotifications = useCallback(() => (canReadNotifications ? api.notifications() : Promise.resolve(undefined)), [api, canReadNotifications]);
  const notifications = useResource(loadNotifications, onSessionLost, true);
  const reloadNotifications = notifications.reload;

  // What the box is, for the header and the dashboard, with the browser's clock when it arrived so its time and
  // uptime can run on. It changes with a reboot or an upgrade, which do happen behind an open tab, so it is read
  // again each minute: often enough for that, and for a first read that failed.
  const canReadStatus = capabilities.has('status.read');
  const loadSystem = useCallback(async () => (canReadStatus ? { info: await api.system(), loadedAt: Date.now() } : undefined), [api, canReadStatus]);
  const system = useResource(loadSystem, onSessionLost, true);
  useVisiblePoll(system.refresh, systemPollMilliseconds, canReadStatus);

  const loadUpdate = useCallback(() => (canReadStatus ? api.updateStatus() : Promise.resolve(undefined)), [api, canReadStatus]);
  const update = useResource(loadUpdate, onSessionLost, true);
  useVisiblePoll(update.refresh, systemPollMilliseconds, canReadStatus);

  // Saves, applies and notification changes reach the shell on one event stream, dispatched by event type so each
  // change reloads only its own resource. With no type (the stream opened or failed) or one it does not know, every
  // resource reloads: a reader without the capability reads nothing.
  const handleEvent = useCallback((type?: string) => {
    const changed = type === 'config.head.changed' || type === 'apply.changed';
    const noticed = type === 'notifications.changed';
    if (!noticed) void reloadChanges();
    if (!changed) void reloadNotifications();
  }, [reloadChanges, reloadNotifications]);
  useEvents(handleEvent, canReadChanges || canReadNotifications, canReadNotifications && browserAlertsActive);
  // The change state is read again every 15 seconds while a read has failed, until one answers, and while an apply is
  // activating or awaits confirmation: a healthy stream may bring no other event, and the events are not durable, so
  // a lost one must not hide the confirmation an apply waits for.
  const latestState = changes.data?.applies.items[0]?.state;
  const applying = latestState === 'activating' || latestState === 'awaiting_confirmation';
  useVisiblePoll(changes.refresh, retryMilliseconds, canReadChanges && (changes.error !== '' || applying));
  useVisiblePoll(notifications.refresh, retryMilliseconds, canReadNotifications && notifications.error !== '', browserAlertsActive);

  // Whether the saved settings wait for decisions after an upgrade (settings safe mode). It changes only with a new
  // saved head (an upgrade, a restore, the answers), so it is read again when the head moves, never on a timer:
  // the preview is a POST, and polling it would count as activity and keep an idle session alive.
  // An upgrade that brings decisions for the same saved head moves no head, so a new build or boot reads it again too.
  const head = changes.data?.head;
  const build = system.data ? `${system.data.info.version} ${system.data.info.bootedAt ?? ''}` : '';
  const loadUpgrade = useCallback(() => (canReadChanges && head !== undefined ? api.upgradePreview() : Promise.resolve(undefined)), [api, canReadChanges, head, build]); // eslint-disable-line react-hooks/exhaustive-deps
  const upgrade = useResource(loadUpgrade, onSessionLost);
  const upgradeError = upgrade.error;
  const refreshUpgrade = upgrade.refresh;
  const reloadUpgrade = upgrade.reload;
  // A check that failed is tried again every 15 seconds, and with each answered read of the change state, until it
  // reads: a healthy stream may bring no other event.
  useVisiblePoll(upgrade.refresh, retryMilliseconds, upgradeError !== '');
  const read = changes.data;
  useEffect(() => {
    if (upgradeError && read) refreshUpgrade();
  }, [read, upgradeError, refreshUpgrade]);
  const upgradePending = (upgrade.data?.questions.length ?? 0) > 0;
  // Once upgrade decisions are answered (or absent), addons-release-hold clears /run/andaru/addons-held
  // asynchronously; refresh system status promptly until settingsHold disappears.
  const waitingHoldRelease = !upgradePending && (system.data?.info.safeMode?.reasons.includes('settingsHold') ?? false);
  useVisiblePoll(system.refresh, 2_000, canReadStatus && connected && waitingHoldRelease);
  const onUpgradePage = useMatch('/system/upgrade') !== null;
  // The visit opens on the questions once, so the dashboard stays reachable for those who cannot answer.
  const openedUpgrade = useRef(false);
  useEffect(() => {
    if (!upgradePending || openedUpgrade.current) return;
    openedUpgrade.current = true;
    if (pathname === '/') navigate('/system/upgrade', { replace: true });
  }, [upgradePending, pathname, navigate]);

  const groups = [
    {
      label: 'Network',
      items: [
        { to: '/network/interfaces', label: 'Interfaces', visible: canReadStatus },
        { to: '/network/dhcp', label: 'DHCP', visible: canReadChanges || canReadStatus },
        { to: '/network/dns', label: 'DNS', visible: canReadChanges },
        { to: '/network/virtual-ips', label: 'Virtual IPs', visible: canReadChanges },
        { to: '/network/topology', label: 'Topology', visible: canReadStatus },
        { to: '/network/vlans', label: 'VLANs & Segments', visible: canReadStatus },
        { to: '/network/vlans/flow', label: 'VLAN Flow', visible: canReadStatus },
        { to: '/network/devices', label: 'Devices', visible: canReadStatus },
        { to: '/network/clients', label: 'Clients', visible: canReadStatus },
      ],
    },
    { label: 'Firewall', items: [{ to: '/firewall/rules', label: 'Rules', visible: canReadChanges }, { to: '/firewall/nat', label: 'NAT', visible: canReadChanges }, { to: '/firewall/aliases', label: 'Aliases', visible: canReadChanges }, { to: '/firewall/zones', label: 'Zones', visible: canReadChanges }] },
    {
      label: 'Diagnostics',
      items: [
        { to: '/diagnostics/traffic-flow', label: 'Traffic simulator', visible: canReadChanges },
        { to: '/network/diagnostics', label: 'Tools & events', visible: canReadStatus },
      ],
    },
    { label: 'Changes', items: [{ to: '/changes', label: 'Review and apply', visible: canReadChanges }] },
    {
      label: 'System',
      items: [
        { to: '/system/details', label: 'Details', visible: canReadStatus },
        { to: '/system/updates', label: 'Updates', visible: canReadStatus },
        { to: '/system/backups', label: 'Backups', visible: capabilities.has('backups.create') || capabilities.has('backups.restore') },
        { to: '/system/notifications', label: 'Notifications', visible: canReadNotifications || canReadChanges },
        { to: '/system/users', label: 'Users', visible: capabilities.has('users.read') },
        { to: '/system/roles', label: 'Roles', visible: capabilities.has('users.read') || canReadChanges },
        { to: '/system/tokens', label: 'Tokens', visible: capabilities.has('tokens.manageSelf') || capabilities.has('tokens.manageAll') },
        { to: '/system/audit', label: 'Audit', visible: capabilities.has('audit.read') },
      ],
    },
  ].map((group) => ({ ...group, items: group.items.filter((item) => item.visible) })).filter((group) => group.items.length > 0);
  const visible = new Set(groups.flatMap((group) => group.items.map((item) => item.to)));
  const hostname = system.data?.info.hostname;

  const pending = changes.data ? pendingNotice(changes.data) : null;
  const badge = pending ? pendingBadges[pending.kind] : undefined;
  const changesBadge = badge ? <Badge size="xs" color={badge.color} variant={badge.variant}>{badge.label}</Badge> : undefined;
  const updNotice = updateNotice(update.data);
  const updateBadge = updNotice?.navLabel
    ? <Badge size="xs" color={updNotice.color} variant={updNotice.variant}>{updNotice.navLabel}</Badge>
    : undefined;
  const navBadges: Record<string, ReactNode> = {
    '/changes': changesBadge,
    '/system/updates': updateBadge,
  };

  async function changePassword() {
    setAccountError('');
    try {
      setPasswordUser(await api.getUser(session.account.id));
    } catch (error) {
      reportError(error, onSessionLost, setAccountError);
    }
  }

  const pages: Array<[string, ReactNode]> = [
    ['/network/interfaces', <InterfacesScreen api={api} session={session} onSaved={() => void reloadChanges()} onSessionLost={onSessionLost} />],
    ['/network/dhcp', <DhcpScreen api={api} session={session} onSaved={() => void reloadChanges()} onSessionLost={onSessionLost} />],
    ['/network/dns', <DnsScreen api={api} session={session} onSaved={() => void reloadChanges()} onSessionLost={onSessionLost} />],
    ['/network/virtual-ips', <VirtualIpsScreen api={api} session={session} onSaved={() => void reloadChanges()} onSessionLost={onSessionLost} />],
    ['/firewall/rules', <RulesScreen api={api} session={session} onSaved={() => void reloadChanges()} onSessionLost={onSessionLost} />],
    ['/firewall/nat', <NatScreen api={api} session={session} onSaved={() => void reloadChanges()} onSessionLost={onSessionLost} />],
    ['/firewall/aliases', <AliasesScreen api={api} session={session} onSaved={() => void reloadChanges()} onSessionLost={onSessionLost} />],
    ['/firewall/zones', <ZonesScreen api={api} session={session} onSaved={() => void reloadChanges()} onSessionLost={onSessionLost} />],
    ['/diagnostics/traffic-flow', <TrafficFlowScreen api={api} session={session} onSessionLost={onSessionLost} />],
    ['/changes', <ChangesScreen api={api} session={session} state={changes.data} stateError={changes.error} connected={connected} onChanged={() => void reloadChanges()} onSessionLost={onSessionLost} />],
    ['/system/details', <SystemDetailsScreen system={system} api={api} canRename={capabilities.has('configuration.apply')} onSessionLost={onSessionLost} />],
    ['/system/updates', <UpdatesScreen api={api} session={session} update={update} onSessionLost={onSessionLost} />],
    ['/system/backups', <BackupScreen api={api} session={session} hostname={hostname} onRestored={() => { void reloadChanges(); void reloadNotifications(); void system.reload(); }} onSessionLost={onSessionLost} />],
    ['/system/notifications', <NotificationsScreen api={api} session={session} notifications={notifications} hostname={hostname} onSaved={() => void reloadChanges()} onSessionLost={onSessionLost} />],
    ['/system/users', <UsersScreen api={api} session={session} onSessionLost={onSessionLost} />],
    ['/system/roles', <RolesScreen api={api} session={session} onSaved={() => void reloadChanges()} onSessionLost={onSessionLost} />],
    ['/system/tokens', <TokensScreen api={api} session={session} onSessionLost={onSessionLost} />],
    ['/system/audit', <AuditScreen api={api} onSessionLost={onSessionLost} />],
  ];

  return (
    <AppFrame
      product="Andaru"
      // The host name says which firewall this is, at every width. The version is on the dashboard and the details page.
      subtitle={hostname}
      baseTitle="Andaru administration"
      pageTitles={pageTitles}
      connected={connected}
      headerActions={canReadNotifications && <HeaderNotifications notifications={notifications} />}
      accountLabel={`Account (${session.account.login})`}
      accountTitle={session.account.login}
      accountItems={
        <>
          {(() => {
            const display = session.account.displayName?.trim() ?? '';
            return (
              <div className="account-menu-header" role="presentation">
                <Text component="div" size="sm" fw={600} truncate="end" title={session.account.login}>{session.account.login}</Text>
                {display && display !== session.account.login && (
                  <Text component="div" size="xs" c="dimmed" truncate="end" title={display}>{display}</Text>
                )}
              </div>
            );
          })()}
          <Menu.Divider />
          <Menu.Item onClick={() => void navigate('/system/session')}>Session details</Menu.Item>
          {capabilities.has('users.read') && <Menu.Item onClick={() => void changePassword()}>Change password</Menu.Item>}
        </>
      }
      timeZoneNote="Used to display timestamps across Andaru in this browser. The appliance clock on the dashboard stays in UTC."
      onSignOut={onLogout}
      homeLabel="Dashboard"
      groups={groups}
      navBadges={navBadges}
    >
      <Stack>
        {accountError && <Alert color="red" role="alert" title="Account request failed">{accountError}</Alert>}
        {/* Review and apply owns these controls on its own page. */}
        {/* While disconnected, the header indicator already owns the offline state; only show server read failures while connected. */}
        {connected && !onChangesPage && changes.error && <Alert color="orange" role="alert" title="Could not check pending changes">{changes.error}</Alert>}
        {connected && upgrade.error && !onUpgradePage && <Alert color="orange" role="alert" title="Could not check upgrade status">{upgrade.error}</Alert>}
        {upgradePending && !onUpgradePage && <Alert color="orange" role="status" title="Finish the upgrade">The saved settings need decisions before anything can be saved or applied. <Anchor component={Link} to="/system/upgrade">Answer the questions</Anchor></Alert>}
        {system.data?.info.safeMode?.active && (() => {
          const reasons = formatSafeModeReasons(system.data.info.safeMode);
          return (
            <Alert color="orange" role="status" title="Safe mode active">
              Add-ons are disabled{reasons ? ` (${reasons})` : ''}.
            </Alert>
          );
        })()}
        {capabilities.has('configuration.apply') && changes.data?.head === '0' && pathname !== '/setup' && (
          <Alert color="blue" role="status" title="Initial setup">
            This appliance has no saved network configuration yet. <Anchor component={Link} to="/setup">Open setup assistant</Anchor>
          </Alert>
        )}
        {!onChangesPage && <PendingNotice api={api} session={session} state={changes.data} onChanged={() => void reloadChanges()} onSessionLost={onSessionLost} />}
        <Routes>
          <Route path="/" element={<DashboardScreen api={api} session={session} system={system} notifications={notifications} update={update.data} onSessionLost={onSessionLost} />} />
          {capabilities.has('configuration.apply') && (
            <Route
              path="/setup"
              element={
                <SetupWizardScreen
                  api={api}
                  session={session}
                  onSessionLost={onSessionLost}
                  onComplete={() => {
                    void reloadChanges();
                    void system.reload();
                    void navigate('/');
                  }}
                />
              }
            />
          )}
          {canReadChanges && <Route path="/system/upgrade" element={<UpgradeScreen api={api} session={session} onFinished={() => { void reloadUpgrade(); void reloadChanges(); void system.reload(); }} onSessionLost={onSessionLost} />} />}
          {pages.filter(([path]) => visible.has(path)).map(([path, element]) => <Route key={path} path={path} element={element} />)}
          {canReadStatus && (
            <Route
              path="/network/*"
              element={
                <FleetWorkspace
                  csrfToken={session.csrfToken}
                  canReadTargets={canReadChanges}
                  canWrite={capabilities.has('configuration.write')}
                  onSessionLost={onSessionLost}
                />
              }
            />
          )}
          <Route path="/system/session" element={<SessionScreen api={api} session={session} onSessionLost={onSessionLost} />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </Stack>
      {canReadNotifications && <NotificationToaster notifications={notifications.data} hostname={hostname} />}
      {passwordUser && <PasswordDialog user={passwordUser} selfAccountId={session.account.id} onClose={() => setPasswordUser(null)} onSessionLost={onSessionLost} onSave={async (currentPassword, newPassword) => { await api.changePassword(passwordUser.id, { currentPassword, newPassword, expectedRevision: passwordUser.revision }); }} />}
    </AppFrame>
  );
}
