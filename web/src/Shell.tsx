import { Alert, AppShell, Badge, Burger, Button, Group, Image, Menu, NavLink, Stack, Text, Title } from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Link, Navigate, Route, Routes, useLocation, useMatch, useNavigate } from 'react-router';
import { ApiClient, reportError } from './api';
import mark from './andaru-icon.svg';
import { loadChangeState } from './changes';
import { PendingNotice } from './PendingNotice';
import { AuditScreen } from './screens/AuditScreen';
import { ChangesScreen } from './screens/ChangesScreen';
import { InterfacesScreen } from './screens/InterfacesScreen';
import { PasswordDialog } from './screens/PasswordDialog';
import { RolesScreen } from './screens/RolesScreen';
import { SessionScreen } from './screens/SessionScreen';
import { TokensScreen } from './screens/TokensScreen';
import { UsersScreen } from './screens/UsersScreen';
import type { Session, User } from './types';
import { useResource } from './useResource';

interface ShellProps {
  api: ApiClient;
  session: Session;
  connected: boolean;
  onLogout(): void;
  onSessionLost(): void;
}

const pollMilliseconds = 15_000;

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

  useEffect(() => {
    if (!canReadChanges) return;
    const visible = () => { if (document.visibilityState === 'visible') void reloadChanges(); };
    const timer = setInterval(visible, pollMilliseconds);
    document.addEventListener('visibilitychange', visible);
    return () => {
      clearInterval(timer);
      document.removeEventListener('visibilitychange', visible);
    };
  }, [canReadChanges, reloadChanges]);

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
  const home = groups[0]?.items[0]?.to ?? '/system/session';

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
          <Group gap="sm" wrap="nowrap">
            <Burger className="nav-burger" color="white" opened={opened} onClick={toggle} size="sm" aria-label={opened ? 'Close navigation' : 'Open navigation'} />
            <Image src={mark} alt="" w={28} h={28} />
            <Title order={1} size="h3">Andaru</Title>
          </Group>
          <Group gap="sm" wrap="nowrap">
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
        <Stack gap="md">
          {groups.map((group) => (
            <div key={group.label}>
              <Text size="xs" fw={700} c="dimmed" tt="uppercase" px="sm" mb={4}>{group.label}</Text>
              {group.items.map((item) => <NavLink className="nav-item" key={item.to} component={Link} to={item.to} label={item.label} active={pathname === item.to} aria-current={pathname === item.to ? 'page' : undefined} onClick={close} />)}
            </div>
          ))}
        </Stack>
      </AppShell.Navbar>
      <AppShell.Main>
        <Stack>
          {!connected && <Alert color="red" role="alert" title="Connection lost">The box does not answer. Pages show the last data it sent.</Alert>}
          {accountError && <Alert color="red" role="alert" title="Account request failed">{accountError}</Alert>}
          {/* Review and apply owns these controls on its own page. */}
          {!onChangesPage && <PendingNotice api={api} session={session} state={changes.data} onChanged={() => void reloadChanges()} onSessionLost={onSessionLost} />}
          <Routes>
            {pages.filter(([path]) => visible.has(path)).map(([path, element]) => <Route key={path} path={path} element={element} />)}
            <Route path="/system/session" element={<SessionScreen session={session} />} />
            <Route path="*" element={<Navigate to={home} replace />} />
          </Routes>
        </Stack>
      </AppShell.Main>
      {passwordUser && <PasswordDialog user={passwordUser} selfAccountId={session.account.id} onClose={() => setPasswordUser(null)} onSessionLost={onSessionLost} onSave={async (currentPassword, newPassword) => { await api.changePassword(passwordUser.id, { currentPassword, newPassword, expectedRevision: passwordUser.revision }); }} />}
    </AppShell>
  );
}
