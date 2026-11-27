import { AccordionChevron, ActionIcon, AppShell, Badge, Burger, Button, Group, Image, Menu, Modal, NavLink, Select, Stack, Text, Title, useMantineColorScheme, type MantineColorScheme } from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import { Fragment, useEffect, useMemo, useState, type ReactNode } from 'react';
import { Link, useLocation } from 'react-router';
import mark from './andaru-icon.svg';
import { formatTimestamp, setTimeZone, timeZoneOptions, useTimeZone } from './timezone';
import { UserMark } from './screens/Marks';

export interface NavGroup {
  label: string;
  items: Array<{ to: string; label: string }>;
}

// The frame Andaru's shell and the standalone Netplane shell share: the header, the account menu, the branch
// navigation and the document title. Each product brings its own pages, header actions and account items.
interface AppFrameProps {
  product: string;
  /** Shown beside the product name in the header and the document title, such as Andaru's host name. */
  subtitle?: string;
  /** The document title while the frame is not mounted; the product name by default. */
  baseTitle?: string;
  pageTitles: Record<string, string>;
  connected: boolean;
  /** Header controls between the connection badge and the account menu. */
  headerActions?: ReactNode;
  accountLabel: string;
  accountTitle?: string;
  /** Account menu items above the appearance choices. */
  accountItems?: ReactNode;
  timeZoneNote: string;
  /** Without it, the account menu has no sign out. */
  onSignOut?: () => void;
  homeLabel: string;
  groups: NavGroup[];
  navBadges?: Record<string, ReactNode>;
  children: ReactNode;
}

const schemes: Array<{ value: MantineColorScheme; label: string }> = [
  { value: 'auto', label: 'System' },
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
];

export function AppFrame({ product, subtitle, baseTitle = product, pageTitles, connected, headerActions, accountLabel, accountTitle, accountItems, timeZoneNote, onSignOut, homeLabel, groups, navBadges = {}, children }: AppFrameProps) {
  const { colorScheme, setColorScheme } = useMantineColorScheme();
  const timeZone = useTimeZone();
  const [timeZoneAt, setTimeZoneAt] = useState<number | null>(null);
  const zoneChoices = useMemo(() => (timeZoneAt !== null ? timeZoneOptions(timeZone) : []), [timeZoneAt, timeZone]);
  const [opened, { toggle, close }] = useDisclosure();
  const { pathname } = useLocation();

  const normalizedPath = pathname.endsWith('/') && pathname.length > 1 ? pathname.slice(0, -1) : pathname;
  const isNavItemActive = (to: string) =>
    normalizedPath === to
    || (to === '/network/devices' && normalizedPath.startsWith('/network/devices/'))
    || (to === '/system/addons' && normalizedPath.startsWith('/system/addons/'));
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
  useEffect(() => {
    const title = pageTitles[normalizedPath]
      ?? (normalizedPath.startsWith('/network/devices/')
        ? 'Device'
        : normalizedPath.startsWith('/system/addons/')
          ? 'Add-on'
          : undefined);
    document.title = [title, subtitle, product].filter(Boolean).join(' — ');
    return () => { document.title = baseTitle; };
  }, [normalizedPath, subtitle, product, baseTitle, pageTitles]);

  const pageLink = (to: string, label: string, rightSection?: ReactNode) => {
    const active = isNavItemActive(to);
    return <NavLink className="nav-item" key={to} component={Link} to={to} label={label} rightSection={rightSection} active={active} aria-current={active ? 'page' : undefined} onClick={close} />;
  };

  return (
    <AppShell className="app-shell" data-nav-open={opened || undefined} header={{ height: 56 }} navbar={{ width: 240, breakpoint: 900, collapsed: { mobile: !opened } }} padding="md">
      <AppShell.Header className="app-header">
        <Group h="100%" px="md" justify="space-between" wrap="nowrap">
          <Group className="app-brand" gap="sm" wrap="nowrap">
            <Burger className="nav-burger" color="white" opened={opened} onClick={toggle} size="sm" aria-label={opened ? 'Close navigation' : 'Open navigation'} />
            <Image src={mark} alt="" w={28} h={28} />
            <Title className="app-title" order={1} size="h3">{product}</Title>
            {subtitle && <Text className="app-box" size="sm">{subtitle}</Text>}
          </Group>
          <Group className="app-session" gap="sm" wrap="nowrap">
            {/* Connected is the usual state and says so quietly; a lost connection is the one that shouts. */}
            <Badge color={connected ? 'green' : 'red'} variant={connected ? 'dot' : 'filled'} className={connected ? 'quiet-badge' : undefined} role="status">{connected ? 'Connected' : 'Connection lost'}</Badge>
            {headerActions}
            <Menu position="bottom-end" floatingStrategy="fixed" width={220} middlewares={{ flip: false, shift: true }} transitionProps={{ duration: 0 }}>
              <Menu.Target>
                <ActionIcon className="account-button" variant="default" size={30} aria-label={accountLabel} title={accountTitle}>
                  <UserMark />
                </ActionIcon>
              </Menu.Target>
              <Menu.Dropdown className="account-menu-dropdown">
                {accountItems && <>{accountItems}<Menu.Divider /></>}
                <Menu.Label>Appearance</Menu.Label>
                {schemes.map(({ value, label }) => (
                  <Menu.Item key={value} role="menuitemradio" rightSection={colorScheme === value ? <span aria-hidden="true">✓</span> : undefined} aria-checked={colorScheme === value} onClick={() => setColorScheme(value)}>{label}</Menu.Item>
                ))}
                <Menu.Divider />
                <Menu.Item onClick={() => setTimeZoneAt(Date.now())}>Time zone…</Menu.Item>
                {onSignOut && <><Menu.Divider /><Menu.Item onClick={onSignOut}>Sign out</Menu.Item></>}
              </Menu.Dropdown>
            </Menu>
          </Group>
        </Group>
      </AppShell.Header>
      <AppShell.Navbar className="app-navbar" p="xs" component="nav" aria-label="Main navigation">
        {pageLink('/', homeLabel)}
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
        {children}
      </AppShell.Main>
      <Modal opened={timeZoneAt !== null} onClose={() => setTimeZoneAt(null)} title="Display time zone" centered lockScroll={false} closeButtonProps={{ 'aria-label': 'Close' }}>
        <Stack>
          <Text size="sm" c="dimmed">{timeZoneNote}</Text>
          <Select label="Time zone" data={zoneChoices} value={timeZone} searchable allowDeselect={false} onChange={(value) => { if (value) setTimeZone(value); }} />
          {timeZoneAt !== null && <Text size="sm" className="tabular">Preview: {formatTimestamp(timeZoneAt, timeZone)}</Text>}
          <Group justify="flex-end"><Button variant="default" onClick={() => setTimeZoneAt(null)}>Done</Button></Group>
        </Stack>
      </Modal>
    </AppShell>
  );
}
