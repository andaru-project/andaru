import { Badge, Group, Paper, Stack, Table, Text, Title } from '@mantine/core';
import { useCallback, useState, type ReactNode } from 'react';
import { Link } from 'react-router';
import { bytes, duration, plural } from '../format';
import type { SystemInfo, UpdateStatus } from '../types';
import { useVisiblePoll, type Resource } from '../useResource';
import { Loaded } from './Loaded';

type System = Resource<{ info: SystemInfo; loadedAt: number } | undefined>;

function Row({ label, children }: { label: string; children: ReactNode }) {
  return <Table.Tr><Table.Th scope="row">{label}</Table.Th><Table.Td>{children}</Table.Td></Table.Tr>;
}

function Facts({ children }: { children: ReactNode }) {
  return <Table className="facts" withRowBorders={false} verticalSpacing={2} horizontalSpacing={0}><Table.Tbody>{children}</Table.Tbody></Table>;
}

const list = (names: string[]) => (names.length > 0 ? names.join(', ') : 'none');
const words = (...parts: string[]) => parts.filter(Boolean).join(' ');

// loadedAt is the browser's clock when the description arrived. The box's clock may differ from the browser's, so
// the box's time and uptime are its own at that moment plus the browser's time since: they move on a minute at a
// time, and when the tab is looked at again, without asking the box.
function useBoxClock(info: SystemInfo, loadedAt: number) {
  const [now, setNow] = useState(Date.now);
  useVisiblePoll(useCallback(() => setNow(Date.now()), []), 60_000);
  const clock = Date.parse(info.time) + Math.max(now - loadedAt, 0);
  const uptime = info.bootedAt ? (clock - Date.parse(info.bootedAt)) / 1000 : NaN;
  return (
    <>
      {/* The box's own clock, in UTC so it reads the same from anywhere: a firewall with a wrong clock misjudges certificates and logs. */}
      {Number.isFinite(clock) && <Row label="Clock">{new Date(clock).toISOString().slice(0, 16).replace('T', ' ')} UTC</Row>}
      {Number.isFinite(uptime) && uptime >= 0 && <Row label="Uptime">{duration(uptime)}</Row>}
    </>
  );
}

function UpdatePill({ update }: { update?: UpdateStatus }) {
  if (!update || !update.supported) return null;
  if (update.rebootNeeded) {
    return (
      <Badge component={Link} to="/system/updates" size="xs" variant="filled" color="teal" className="update-pill plain-badge">
        Reboot to apply {update.staged?.version || 'update'}
      </Badge>
    );
  }
  if (update.state === 'available') {
    return (
      <Badge component={Link} to="/system/updates" size="xs" variant="filled" color="orange" className="update-pill plain-badge">
        Update available: {update.available?.version || 'new version'}
      </Badge>
    );
  }
  if (update.state === 'up_to_date') {
    return (
      <Badge component={Link} to="/system/updates" size="xs" variant="light" color="green" className="update-pill plain-badge">
        Up to date
      </Badge>
    );
  }
  if (update.state === 'error') {
    return (
      <Badge component={Link} to="/system/updates" size="xs" variant="light" color="red" className="update-pill plain-badge">
        Update check failed
      </Badge>
    );
  }
  return null;
}

// The dashboard's widget: what changes or says something is wrong, and a line each for the machine and its
// processor. The header names the box; the rest of what it is made of is looked up rarely, and is on the
// System details page.
export function SystemWidget({ info, loadedAt, update }: { info: SystemInfo; loadedAt: number; update?: UpdateStatus }) {
  const running = useBoxClock(info, loadedAt);
  // A guest that names no machine is still said to be one.
  const machine = words(info.hardware.vendor, info.hardware.product) || (info.guest ? 'virtual machine' : '');
  return (
    <Facts>
      <Row label="Version">
        <Group gap="xs" wrap="wrap">
          <span>{info.version}</span>
          <UpdatePill update={update} />
        </Group>
      </Row>
      {running}
      {machine && <Row label="Machine">{machine}</Row>}
      <Row label="CPU">{info.cpu.model || info.architecture}, {plural(info.cpu.cores, 'core')}</Row>
    </Facts>
  );
}

function Card({ title, children }: { title: string; children: ReactNode }) {
  const id = `system-${title.toLowerCase()}`;
  return <Paper component="section" withBorder p="md" aria-labelledby={id}><Title order={3} size="h5" mb="sm" id={id}>{title}</Title><Facts>{children}</Facts></Paper>;
}

// Everything the box says of itself, in three cards.
export function SystemFacts({ info, loadedAt }: { info: SystemInfo; loadedAt: number }) {
  const running = useBoxClock(info, loadedAt);
  const { cpu, hardware } = info;
  // Good news is only what the box calls so: a state this version does not know counts as unknown.
  const named = (states: string[]) => cpu.vulnerabilities.filter((item) => states.includes(item.state)).map((item) => item.name).join(', ');
  const vulnerable = named(['vulnerable']);
  const unknown = cpu.vulnerabilities.filter((item) => !['vulnerable', 'mitigated', 'not_affected'].includes(item.state)).map((item) => item.name).join(', ');
  const machine = words(hardware.vendor, hardware.product);
  const board = words(hardware.boardVendor, hardware.board);
  const firmware = words(hardware.biosVendor, hardware.biosVersion, hardware.biosDate);
  return (
    <Stack className="details">
      <Card title="Software">
        <Row label="Hostname">{info.hostname || '—'}</Row>
        <Row label="Version">{info.version}</Row>
        <Row label="Kernel">{info.kernel}</Row>
        {running}
      </Card>
      <Card title="Hardware">
        <Row label="Machine">{[machine, info.guest && `virtual machine${info.guest !== 'unknown' ? ` (${info.guest})` : ''}`].filter(Boolean).join(' · ') || '—'}</Row>
        {board && board !== machine && <Row label="Board">{board}</Row>}
        {hardware.serial && <Row label="Serial">{hardware.serial}</Row>}
        {firmware && <Row label="BIOS">{firmware}</Row>}
        {info.memoryBytes > 0 && <Row label="Memory">{bytes(info.memoryBytes)}</Row>}
      </Card>
      <Card title="Processor">
        <Row label="CPU">{cpu.model || info.architecture}, {plural(cpu.cores, 'core')}{cpu.threads !== cpu.cores && `, ${plural(cpu.threads, 'thread')}`}</Row>
        {cpu.level && <Row label="Level">{cpu.level}</Row>}
        {cpu.microcode && <Row label="Microcode">{cpu.microcode}</Row>}
        <Row label="Hardware crypto">{list(cpu.features.cryptography)}</Row>
        <Row label="CPU security">{list(cpu.features.security)}</Row>
        <Row label="Virtualisation">{list([...cpu.features.virtualization, ...(info.iommu ? ['IOMMU active'] : [])])}</Row>
        <Row label="CPU vulnerabilities">
          {cpu.vulnerabilities.length === 0 ? 'no report' : !vulnerable && !unknown ? 'all mitigated' : (
            <>
              {vulnerable && <Text span inherit className="error-text">not mitigated: {vulnerable}</Text>}
              {vulnerable && unknown && '; '}
              {unknown && <>unknown: {unknown}</>}
            </>
          )}
        </Row>
      </Card>
    </Stack>
  );
}

export function SystemDetailsScreen({ system }: { system: System }) {
  return (
    <Stack>
      <div>
        <Title order={2}>System details</Title>
        <Text c="dimmed">What the box is made of and what it runs.</Text>
      </div>
      <Loaded resource={system} what="the system description">{(loaded) => loaded && <SystemFacts info={loaded.info} loadedAt={loaded.loadedAt} />}</Loaded>
    </Stack>
  );
}
