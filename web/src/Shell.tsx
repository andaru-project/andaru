import { AccordionChevron, Alert, Anchor, AppShell, Badge, Burger, Button, Group, Image, Menu, Modal, NavLink, Select, Stack, Text, Title, useMantineColorScheme, type MantineColorScheme } from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import { Fragment, useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Link, Navigate, Route, Routes, useLocation, useMatch, useNavigate } from 'react-router';
import { ApiClient, reportError } from './api';
import mark from './andaru-icon.svg';
import { loadChangeState, pendingNotice } from './changes';
import { plural } from './format';
import { highestSeverity, severityColor } from './notifications';
import { PendingNotice } from './PendingNotice';
import { FleetWorkspace } from './netplane/FleetWorkspace';
import { formatTimestamp, setTimeZone, timeZoneOptions, useTimeZone } from './timezone';
import { AliasesScreen } from './screens/AliasesScreen';
import { AuditScreen } from './screens/AuditScreen';
import { ChangesScreen } from './screens/ChangesScreen';
import { DashboardScreen } from './screens/DashboardScreen';
import { InterfacesScreen } from './screens/InterfacesScreen';
import { Bell } from './screens/Marks';
import { NotificationsScreen } from './screens/NotificationsScreen';
import { PasswordDialog } from './screens/PasswordDialog';
import { RolesScreen } from './screens/RolesScreen';
import { RulesScreen } from './screens/RulesScreen';
import { SessionScreen } from './screens/SessionScreen';
import { SetupWizardScreen } from './screens/SetupWizardScreen';
import { TokensScreen } from './screens/TokensScreen';
import { UpgradeScreen } from './screens/UpgradeScreen';
import { UpdatesScreen } from './screens/UpdatesScreen';
import { SystemDetailsScreen, updateNotice } from './screens/SystemWidget';
import { UsersScreen } from './screens/UsersScreen';
import type { NotificationList, Session, User } from './types';
import { useEvents, useResource, useVisiblePoll, type Resource } from './useResource';

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
  '/network/topology': 'Topology',
  '/network/vlans': 'VLANs & Segments',
  '/network/vlans/flow': 'VLAN Flow',
  '/network/devices': 'Devices',
  '/network/clients': 'Clients',
  '/network/diagnostics': 'Diagnostics',
  '/firewall/rules': 'Rules',
  '/firewall/aliases': 'Aliases',
  '/changes': 'Review and apply',
  '/system/details': 'System details',
  '/system/updates': 'Software updates',
  '/system/notifications': 'Notifications',
  '/system/users': 'Users',
  '/system/roles': 'Roles',
  '/system/tokens': 'API tokens',
  '/system/audit': 'Security audit',
  '/system/upgrade': 'Finish the upgrade',
  '/system/session': 'Current session',
};

const schemes: Array<{ value: MantineColorScheme; label: string }> = [
  { value: 'auto', label: 'System' },
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
];

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

export function Shell({ api, session, connected, onLogout, onSessionLost }: ShellProps) {
  const { colorScheme, setColorScheme } = useMantineColorScheme();
  const timeZone = useTimeZone();
  const [timeZoneAt, setTimeZoneAt] = useState<number | null>(null);
  const zoneChoices = useMemo(() => (timeZoneAt !== null ? timeZoneOptions(timeZone) : []), [timeZoneAt, timeZone]);
  const capabilities = useMemo(() => new Set(session.capabilities), [session.capabilities]);
  const [opened, { toggle, close }] = useDisclosure();
  const { pathname } = useLocation();
  const onChangesPage = useMatch('/changes') !== null;
  const navigate = useNavigate();
  const [passwordUser, setPasswordUser] = useState<User | null>(null);
  const [accountError, setAccountError] = useState('');

  const canReadChanges = capabilities.has('configuration.read');
  const loadChanges = useCallback(() => (canReadChanges ? loadChangeState(api) : Promise.resolve(undefined)), [api, canReadChanges]);
  const changes = useResource(loadChanges, onSessionLost);
  const reloadChanges = changes.reload;

  const canReadNotifications = capabilities.has('notifications.read');
  const loadNotifications = useCallback(() => (canReadNotifications ? api.notifications() : Promise.resolve(undefined)), [api, canReadNotifications]);
  const notifications = useResource(loadNotifications, onSessionLost);
  const reloadNotifications = notifications.reload;

  // What the box is, for the header and the dashboard, with the browser's clock when it arrived so its time and
  // uptime can run on. It changes with a reboot or an upgrade, which do happen behind an open tab, so it is read
  // again each minute: often enough for that, and for a first read that failed.
  const canReadStatus = capabilities.has('status.read');
  const loadSystem = useCallback(async () => (canReadStatus ? { info: await api.system(), loadedAt: Date.now() } : undefined), [api, canReadStatus]);
  const system = useResource(loadSystem, onSessionLost);
  useVisiblePoll(system.refresh, systemPollMilliseconds, canReadStatus);

  const loadUpdate = useCallback(() => (canReadStatus ? api.updateStatus() : Promise.resolve(undefined)), [api, canReadStatus]);
  const update = useResource(loadUpdate, onSessionLost);
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
  useEvents(handleEvent, canReadChanges || canReadNotifications);
  // The change state is read again every 15 seconds while a read has failed, until one answers, and while an apply is
  // activating or awaits confirmation: a healthy stream may bring no other event, and the events are not durable, so
  // a lost one must not hide the confirmation an apply waits for.
  const latestState = changes.data?.applies.items[0]?.state;
  const applying = latestState === 'activating' || latestState === 'awaiting_confirmation';
  useVisiblePoll(changes.refresh, retryMilliseconds, canReadChanges && (changes.error !== '' || applying));
  useVisiblePoll(notifications.refresh, retryMilliseconds, canReadNotifications && notifications.error !== '');

  // Whether the saved settings wait for decisions after an upgrade (settings safe mode). It changes only with a new
  // saved head (an upgrade, a restore, the answers), so it is read again when the head moves, never on a timer:
  // the preview is a POST, and polling it would count as activity and keep an idle session alive.
  // An upgrade that brings decisions for the same saved head moves no head, so a new build or boot reads it again too.
  const head = changes.data?.head;
  const build = system.data ? `${system.data.info.version} ${system.data.info.bootedAt ?? ''}` : '';
  const loadUpgrade = useCallback(() => (canReadChanges && head !== undefined ? api.upgradePreview() : Promise.resolve(undefined)), [api, canReadChanges, head, build]); // eslint-disable-line react-hooks/exhaustive-deps
  const upgrade = useResource(loadUpgrade, onSessionLost);
  const upgradeError = upgrade.error;
  const reloadUpgrade = upgrade.reload;
  // A check that failed is tried again every 15 seconds, and with each answered read of the change state, until it
  // reads: a healthy stream may bring no other event.
  useVisiblePoll(upgrade.refresh, retryMilliseconds, upgradeError !== '');
  const read = changes.data;
  useEffect(() => {
    if (upgradeError && read) void reloadUpgrade();
  }, [read, upgradeError, reloadUpgrade]);
  const upgradePending = (upgrade.data?.questions.length ?? 0) > 0;
  const onUpgradePage = useMatch('/system/upgrade') !== null;
  // The visit opens on the questions once, so the dashboard stays reachable for those who cannot answer.
  const openedUpgrade = useRef(false);
  useEffect(() => {
    if (!upgradePending || openedUpgrade.current) return;
    openedUpgrade.current = true;
    if (pathname === '/') navigate('/system/upgrade', { replace: true });
  }, [upgradePending, pathname, navigate]);

  // When the API answers again, show what the server says rather than what was last seen.
  const wasConnected = useRef(connected);
  useEffect(() => {
    if (connected && !wasConnected.current) {
      void reloadChanges();
      void reloadNotifications();
      // A check that failed while the box did not answer is tried again; the head may not have moved.
      if (upgradeError) void reloadUpgrade();
    }
    wasConnected.current = connected;
  }, [connected, reloadChanges, reloadNotifications, upgradeError, reloadUpgrade]);

  const groups = [
    {
      label: 'Network',
      items: [
        { to: '/network/interfaces', label: 'Interfaces', visible: canReadStatus },
        { to: '/network/topology', label: 'Topology', visible: canReadStatus },
        { to: '/network/vlans', label: 'VLANs & Segments', visible: canReadStatus },
        { to: '/network/vlans/flow', label: 'VLAN Flow', visible: canReadStatus },
        { to: '/network/devices', label: 'Devices', visible: canReadStatus },
        { to: '/network/clients', label: 'Clients', visible: canReadStatus },
        { to: '/network/diagnostics', label: 'Diagnostics', visible: canReadStatus },
      ],
    },
    { label: 'Firewall', items: [{ to: '/firewall/rules', label: 'Rules', visible: canReadChanges }, { to: '/firewall/aliases', label: 'Aliases', visible: canReadChanges }] },
    { label: 'Changes', items: [{ to: '/changes', label: 'Review and apply', visible: canReadChanges }] },
    {
      label: 'System',
      items: [
        { to: '/system/details', label: 'Details', visible: canReadStatus },
        { to: '/system/updates', label: 'Updates', visible: canReadStatus },
        { to: '/system/notifications', label: 'Notifications', visible: canReadNotifications },
        { to: '/system/users', label: 'Users', visible: capabilities.has('users.read') },
        { to: '/system/roles', label: 'Roles', visible: capabilities.has('users.read') || canReadChanges },
        { to: '/system/tokens', label: 'Tokens', visible: capabilities.has('tokens.manageSelf') || capabilities.has('tokens.manageAll') },
        { to: '/system/audit', label: 'Audit', visible: capabilities.has('audit.read') },
      ],
    },
  ].map((group) => ({ ...group, items: group.items.filter((item) => item.visible) })).filter((group) => group.items.length > 0);
  const visible = new Set(groups.flatMap((group) => group.items.map((item) => item.to)));

  const normalizedPath = pathname.endsWith('/') && pathname.length > 1 ? pathname.slice(0, -1) : pathname;
  const isNavItemActive = (to: string) => normalizedPath === to || (to === '/network/devices' && normalizedPath.startsWith('/network/devices/'));
  // One branch is open at a time, so the tree fits a phone: the one holding the current page, however the page was
  // reached, until another is opened.
  const currentBranch = groups.find((group) => group.items.some((item) => isNavItemActive(item.to)))?.label;
  const [openBranch, setOpenBranch] = useState(currentBranch);
  const [seenPath, setSeenPath] = useState(pathname);
  if (seenPath !== pathname) {
    setSeenPath(pathname);
    if (currentBranch) setOpenBranch(currentBranch);
  }
  const toggleBranch = (label: string) => setOpenBranch(openBranch === label ? undefined : label);
  const hostname = system.data?.info.hostname;
  useEffect(() => {
    const title = pageTitles[normalizedPath] ?? (normalizedPath.startsWith('/network/devices/') ? 'Device' : undefined);
    document.title = [title, hostname, 'Andaru'].filter(Boolean).join(' — ');
    return () => { document.title = 'Andaru administration'; };
  }, [normalizedPath, hostname]);

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

  const pageLink = (to: string, label: string, rightSection?: ReactNode) => {
    const active = isNavItemActive(to);
    return <NavLink className="nav-item" key={to} component={Link} to={to} label={label} rightSection={rightSection} active={active} aria-current={active ? 'page' : undefined} onClick={close} />;
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
    ['/network/interfaces', <InterfacesScreen api={api} session={session} onSessionLost={onSessionLost} />],
    ['/firewall/rules', <RulesScreen api={api} session={session} onSaved={() => void reloadChanges()} onSessionLost={onSessionLost} />],
    ['/firewall/aliases', <AliasesScreen api={api} session={session} onSaved={() => void reloadChanges()} onSessionLost={onSessionLost} />],
    ['/changes', <ChangesScreen api={api} session={session} state={changes.data} stateError={changes.error} connected={connected} onChanged={() => void reloadChanges()} onSessionLost={onSessionLost} />],
    ['/system/details', <SystemDetailsScreen system={system} />],
    ['/system/updates', <UpdatesScreen api={api} session={session} update={update} onSessionLost={onSessionLost} />],
    ['/system/notifications', <NotificationsScreen api={api} session={session} notifications={notifications} onSessionLost={onSessionLost} />],
    ['/system/users', <UsersScreen api={api} session={session} onSessionLost={onSessionLost} />],
    ['/system/roles', <RolesScreen api={api} session={session} onSaved={() => void reloadChanges()} onSessionLost={onSessionLost} />],
    ['/system/tokens', <TokensScreen api={api} session={session} onSessionLost={onSessionLost} />],
    ['/system/audit', <AuditScreen api={api} onSessionLost={onSessionLost} />],
  ];

  return (
    <AppShell className="app-shell" data-nav-open={opened || undefined} header={{ height: 56 }} navbar={{ width: 240, breakpoint: 900, collapsed: { mobile: !opened } }} padding="md">
      <AppShell.Header className="app-header">
        <Group h="100%" px="md" justify="space-between" wrap="nowrap">
          <Group className="app-brand" gap="sm" wrap="nowrap">
            <Burger className="nav-burger" color="white" opened={opened} onClick={toggle} size="sm" aria-label={opened ? 'Close navigation' : 'Open navigation'} />
            <Image src={mark} alt="" w={28} h={28} />
            <Title className="app-title" order={1} size="h3">Andaru</Title>
            {/* The host name says which firewall this is, at every width. The version is on the dashboard and the details page. */}
            {system.data?.info.hostname && <Text className="app-box" size="sm">{system.data.info.hostname}</Text>}
          </Group>
          <Group className="app-session" gap="sm" wrap="nowrap">
            {/* Connected is the usual state and says so quietly; a lost connection is the one that shouts. */}
            <Badge color={connected ? 'green' : 'red'} variant={connected ? 'dot' : 'filled'} className={connected ? 'quiet-badge' : undefined} role="status">{connected ? 'Connected' : 'Connection lost'}</Badge>
            {canReadNotifications && <HeaderNotifications notifications={notifications} />}
            <Menu position="bottom-end">
              <Menu.Target><Button className="account-button" variant="default" size="xs">{session.account.displayName || session.account.login}</Button></Menu.Target>
              <Menu.Dropdown>
                <Menu.Item onClick={() => void navigate('/system/session')}>Session details</Menu.Item>
                {capabilities.has('users.read') && <Menu.Item onClick={() => void changePassword()}>Change password</Menu.Item>}
                <Menu.Divider />
                <Menu.Label>Appearance</Menu.Label>
                {schemes.map(({ value, label }) => (
                  <Menu.Item key={value} role="menuitemradio" rightSection={colorScheme === value ? <span aria-hidden="true">✓</span> : undefined} aria-checked={colorScheme === value} onClick={() => setColorScheme(value)}>{label}</Menu.Item>
                ))}
                <Menu.Divider />
                <Menu.Item onClick={() => setTimeZoneAt(Date.now())}>Time zone…</Menu.Item>
                <Menu.Divider />
                <Menu.Item onClick={onLogout}>Sign out</Menu.Item>
              </Menu.Dropdown>
            </Menu>
          </Group>
        </Group>
      </AppShell.Header>
      <AppShell.Navbar className="app-navbar" p="xs" component="nav" aria-label="Main navigation">
        {pageLink('/', 'Dashboard')}
        {groups.map((group) => {
          const open = openBranch === group.label;
          return (
            <div key={group.label}>
              {/* A branch label is not a page, so it is a button: an anchor without an address takes no keyboard
                  focus. It holds no children of its own, or NavLink would toggle on the Space key and the browser
                  again on the click that key makes. */}
              {/* A closed branch carries its pages' badges, so pending changes and updates stay in sight. */}
              <NavLink className="nav-branch" component="button" type="button" label={group.label} aria-expanded={open} rightSection={<Group gap={4} wrap="nowrap">{!open && group.items.map((item) => <Fragment key={item.to}>{navBadges[item.to]}</Fragment>)}<AccordionChevron className="nav-chevron" data-open={open || undefined} /></Group>} onClick={() => toggleBranch(group.label)} />
              {open && <div className="nav-children">{group.items.map((item) => pageLink(item.to, item.label, navBadges[item.to]))}</div>}
            </div>
          );
        })}
      </AppShell.Navbar>
      <AppShell.Main>
        <Stack>
          {!connected && <Alert color="red" role="alert" title="Connection lost">The box does not answer. Pages show the last data it sent.</Alert>}
          {accountError && <Alert color="red" role="alert" title="Account request failed">{accountError}</Alert>}
          {/* Review and apply owns these controls on its own page. */}
          {/* The Changes page says this itself. Elsewhere, a state that cannot be read must not look like nothing pending. */}
          {!onChangesPage && changes.error && <Alert color="orange" role="alert" title="Whether changes are pending cannot be read">{changes.error}</Alert>}
          {upgrade.error && !onUpgradePage && <Alert color="orange" role="alert" title="Whether the settings wait for decisions cannot be read">{upgrade.error}</Alert>}
          {upgradePending && !onUpgradePage && <Alert color="orange" role="status" title="Finish the upgrade">The saved settings need decisions before anything can be saved or applied. <Anchor component={Link} to="/system/upgrade">Answer the questions</Anchor></Alert>}
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
            {canReadChanges && <Route path="/system/upgrade" element={<UpgradeScreen api={api} session={session} onFinished={() => { void reloadUpgrade(); void reloadChanges(); }} onSessionLost={onSessionLost} />} />}
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
            <Route path="/system/session" element={<SessionScreen session={session} />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </Stack>
      </AppShell.Main>
      {passwordUser && <PasswordDialog user={passwordUser} selfAccountId={session.account.id} onClose={() => setPasswordUser(null)} onSessionLost={onSessionLost} onSave={async (currentPassword, newPassword) => { await api.changePassword(passwordUser.id, { currentPassword, newPassword, expectedRevision: passwordUser.revision }); }} />}
      <Modal opened={timeZoneAt !== null} onClose={() => setTimeZoneAt(null)} title="Display time zone" centered closeButtonProps={{ 'aria-label': 'Close' }}>
        <Stack>
          <Text size="sm" c="dimmed">Used to display timestamps across Andaru in this browser. The appliance clock on the dashboard stays in UTC.</Text>
          <Select label="Time zone" data={zoneChoices} value={timeZone} searchable allowDeselect={false} onChange={(value) => { if (value) setTimeZone(value); }} />
          {timeZoneAt !== null && <Text size="sm" className="tabular">Preview: {formatTimestamp(timeZoneAt, timeZone)}</Text>}
          <Group justify="flex-end"><Button variant="default" onClick={() => setTimeZoneAt(null)}>Done</Button></Group>
        </Stack>
      </Modal>
    </AppShell>
  );
}
