import { AccordionChevron, Alert, AppShell, Badge, Burger, Button, Group, Image, Menu, NavLink, Stack, Text, Title } from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Link, Navigate, Route, Routes, useLocation, useMatch, useNavigate } from 'react-router';
import { ApiClient, reportError } from './api';
import mark from './andaru-icon.svg';
import { loadChangeState } from './changes';
import { PendingNotice } from './PendingNotice';
import { AuditScreen } from './screens/AuditScreen';
import { ChangesScreen } from './screens/ChangesScreen';
import { DashboardScreen } from './screens/DashboardScreen';
import { InterfacesScreen } from './screens/InterfacesScreen';
import { PasswordDialog } from './screens/PasswordDialog';
import { RolesScreen } from './screens/RolesScreen';
import { SessionScreen } from './screens/SessionScreen';
import { TokensScreen } from './screens/TokensScreen';
import { UsersScreen } from './screens/UsersScreen';
import type { Session, User } from './types';
import { useResource, useVisiblePoll } from './useResource';

interface ShellProps {
  api: ApiClient;
  session: Session;
  connected: boolean;
  onLogout(): void;
  onSessionLost(): void;
}

const pollMilliseconds = 15_000;
const systemPollMilliseconds = 60_000;

// Branches the user closed, kept per browser. Storage may be unavailable; the tree then starts open.
const closedKey = 'andaru.nav.closed';
function storedClosed(): string[] {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(closedKey) ?? '[]');
    return Array.isArray(value) ? value.filter((item) => typeof item === 'string') as string[] : [];
  } catch {
    return [];
  }
}

export function Shell({ api, session, connected, onLogout, onSessionLost }: ShellProps) {
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

  // What the box is, for the header and the dashboard, with the browser's clock when it arrived so its time and
  // uptime can run on. It changes with a reboot or an upgrade, which do happen behind an open tab, so it is read
  // again each minute: often enough for that, and for a first read that failed.
  const canReadStatus = capabilities.has('status.read');
  const loadSystem = useCallback(async () => (canReadStatus ? { info: await api.system(), loadedAt: Date.now() } : undefined), [api, canReadStatus]);
  const system = useResource(loadSystem, onSessionLost);
  useVisiblePoll(system.refresh, systemPollMilliseconds, canReadStatus);

  useVisiblePoll(changes.refresh, pollMilliseconds, canReadChanges);

  // When the API answers again, show what the server says rather than what was last seen.
  const wasConnected = useRef(connected);
  useEffect(() => {
    if (connected && !wasConnected.current) void reloadChanges();
    wasConnected.current = connected;
  }, [connected, reloadChanges]);

  const groups = [
    { label: 'Network', items: [{ to: '/network/interfaces', label: 'Interfaces', visible: capabilities.has('status.read') }] },
    { label: 'Changes', items: [{ to: '/changes', label: 'Review and apply', visible: canReadChanges }] },
    {
      label: 'System',
      items: [
        { to: '/system/users', label: 'Users', visible: capabilities.has('users.read') },
        { to: '/system/roles', label: 'Roles', visible: capabilities.has('users.read') || canReadChanges },
        { to: '/system/tokens', label: 'Tokens', visible: capabilities.has('tokens.manageSelf') || capabilities.has('tokens.manageAll') },
        { to: '/system/audit', label: 'Audit', visible: capabilities.has('audit.read') },
      ],
    },
  ].map((group) => ({ ...group, items: group.items.filter((item) => item.visible) })).filter((group) => group.items.length > 0);
  const visible = new Set(groups.flatMap((group) => group.items.map((item) => item.to)));

  // Closed branches, as this visit has them. Opening the branch of the page one lands on is a change like a click,
  // so both are stored the same way; labels this account does not have are dropped.
  const [closed, setClosed] = useState(storedClosed);
  const labels = groups.map((group) => group.label).join('\n');
  useEffect(() => {
    try {
      localStorage.setItem(closedKey, JSON.stringify(closed.filter((label) => labels.split('\n').includes(label))));
    } catch { /* the tree still works for this visit */ }
  }, [closed, labels]);
  const toggleBranch = (label: string) => setClosed(closed.includes(label) ? closed.filter((item) => item !== label) : [...closed, label]);
  // The branch that holds the current page is open, however the page was reached.
  const currentBranch = groups.find((group) => group.items.some((item) => item.to === pathname))?.label;
  const [seenPath, setSeenPath] = useState('');
  if (seenPath !== pathname) {
    setSeenPath(pathname);
    if (currentBranch && closed.includes(currentBranch)) setClosed(closed.filter((item) => item !== currentBranch));
  }
  const pageLink = (to: string, label: string) => <NavLink className="nav-item" key={to} component={Link} to={to} label={label} active={pathname === to} aria-current={pathname === to ? 'page' : undefined} onClick={close} />;

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
    ['/changes', <ChangesScreen api={api} session={session} state={changes.data} stateError={changes.error} connected={connected} onChanged={() => void reloadChanges()} onSessionLost={onSessionLost} />],
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
            {system.data && <Text className="app-box" size="sm">{[system.data.info.hostname, system.data.info.version].filter(Boolean).join(' · ')}</Text>}
          </Group>
          <Group className="app-session" gap="sm" wrap="nowrap">
            <Badge color={connected ? 'green' : 'red'} role="status">{connected ? 'Connected' : 'Connection lost'}</Badge>
            <Menu position="bottom-end">
              <Menu.Target><Button className="account-button" variant="default" size="xs">{session.account.displayName || session.account.login}</Button></Menu.Target>
              <Menu.Dropdown>
                <Menu.Item onClick={() => void navigate('/system/session')}>Session details</Menu.Item>
                {capabilities.has('users.read') && <Menu.Item onClick={() => void changePassword()}>Change password</Menu.Item>}
                <Menu.Item onClick={onLogout}>Sign out</Menu.Item>
              </Menu.Dropdown>
            </Menu>
          </Group>
        </Group>
      </AppShell.Header>
      <AppShell.Navbar className="app-navbar" p="xs" component="nav" aria-label="Main navigation">
        {pageLink('/', 'Dashboard')}
        {groups.map((group) => {
          const open = !closed.includes(group.label);
          return (
            <div key={group.label}>
              {/* A branch label is not a page, so it is a button: an anchor without an address takes no keyboard
                  focus. It holds no children of its own, or NavLink would toggle on the Space key and the browser
                  again on the click that key makes. */}
              <NavLink className="nav-branch" component="button" type="button" label={group.label} aria-expanded={open} rightSection={<AccordionChevron className="nav-chevron" data-open={open || undefined} />} onClick={() => toggleBranch(group.label)} />
              {open && <div className="nav-children">{group.items.map((item) => pageLink(item.to, item.label))}</div>}
            </div>
          );
        })}
      </AppShell.Navbar>
      <AppShell.Main>
        <Stack>
          {!connected && <Alert color="red" role="alert" title="Connection lost">The box does not answer. Pages show the last data it sent.</Alert>}
          {accountError && <Alert color="red" role="alert" title="Account request failed">{accountError}</Alert>}
          {/* Review and apply owns these controls on its own page. */}
          {!onChangesPage && <PendingNotice api={api} session={session} state={changes.data} onChanged={() => void reloadChanges()} onSessionLost={onSessionLost} />}
          <Routes>
            <Route path="/" element={<DashboardScreen api={api} session={session} changes={changes} system={system} onSessionLost={onSessionLost} />} />
            {pages.filter(([path]) => visible.has(path)).map(([path, element]) => <Route key={path} path={path} element={element} />)}
            <Route path="/system/session" element={<SessionScreen session={session} />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </Stack>
      </AppShell.Main>
      {passwordUser && <PasswordDialog user={passwordUser} selfAccountId={session.account.id} onClose={() => setPasswordUser(null)} onSessionLost={onSessionLost} onSave={async (currentPassword, newPassword) => { await api.changePassword(passwordUser.id, { currentPassword, newPassword, expectedRevision: passwordUser.revision }); }} />}
    </AppShell>
  );
}
