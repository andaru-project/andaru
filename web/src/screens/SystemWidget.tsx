import { Table, Text } from '@mantine/core';
import { useCallback, useState, type ReactNode } from 'react';
import { bytes, duration, plural } from '../format';
import type { SystemInfo } from '../types';
import { useVisiblePoll } from '../useResource';

function Row({ label, children }: { label: string; children: ReactNode }) {
  return <Table.Tr><Table.Th scope="row">{label}</Table.Th><Table.Td>{children}</Table.Td></Table.Tr>;
}

const list = (names: string[]) => (names.length > 0 ? names.join(', ') : 'none');
const words = (...parts: string[]) => parts.filter(Boolean).join(' ');

// loadedAt is the browser's clock when the description arrived. The box's clock may differ from the browser's, so
// the box's time and uptime are its own at that moment plus the browser's time since: they move on a minute at a
// time, and when the tab is looked at again, without asking the box.
export function SystemWidget({ info, loadedAt }: { info: SystemInfo; loadedAt: number }) {
  const [now, setNow] = useState(Date.now);
  useVisiblePoll(useCallback(() => setNow(Date.now()), []), 60_000);
  const passed = Math.max(now - loadedAt, 0);
  const { cpu, hardware } = info;
  const clock = Date.parse(info.time) + passed;
  const uptime = info.bootedAt ? (clock - Date.parse(info.bootedAt)) / 1000 : NaN;
  // Good news is only what the box calls so: a state this version does not know counts as unknown.
  const named = (states: string[]) => cpu.vulnerabilities.filter((item) => states.includes(item.state)).map((item) => item.name).join(', ');
  const vulnerable = named(['vulnerable']);
  const unknown = cpu.vulnerabilities.filter((item) => !['vulnerable', 'mitigated', 'not_affected'].includes(item.state)).map((item) => item.name).join(', ');
  const machine = words(hardware.vendor, hardware.product);
  const board = words(hardware.boardVendor, hardware.board);
  const firmware = words(hardware.biosVendor, hardware.biosVersion, hardware.biosDate);

  return (
    <Table className="facts" withRowBorders={false} verticalSpacing={2} horizontalSpacing={0}>
      <Table.Tbody>
        <Row label="Name">{info.hostname || '—'}</Row>
        <Row label="Andaru">{info.version}</Row>
        <Row label="Kernel">{info.kernel} ({info.architecture})</Row>
        {/* The box's own clock, in UTC so it reads the same from anywhere: a firewall with a wrong clock misjudges certificates and logs. */}
        {Number.isFinite(clock) && <Row label="Clock">{new Date(clock).toISOString().slice(0, 16).replace('T', ' ')} UTC</Row>}
        {Number.isFinite(uptime) && uptime >= 0 && <Row label="Up for">{duration(uptime)}</Row>}
        <Row label="Machine">{machine || '—'}</Row>
        {info.guest && <Row label="Runs as">a virtual machine{info.guest !== 'unknown' && ` (${info.guest})`}</Row>}
        {board && board !== machine && <Row label="Board">{board}</Row>}
        {hardware.serial && <Row label="Serial">{hardware.serial}</Row>}
        {firmware && <Row label="BIOS">{firmware}</Row>}
        <Row label="CPU">{cpu.model || info.architecture}, {plural(cpu.cores, 'core')}{cpu.threads !== cpu.cores && `, ${plural(cpu.threads, 'thread')}`}</Row>
        {cpu.level && <Row label="Level">{cpu.level}</Row>}
        {cpu.microcode && <Row label="Microcode">{cpu.microcode}</Row>}
        {info.memoryBytes > 0 && <Row label="Memory">{bytes(info.memoryBytes)}</Row>}
        <Row label="Cryptography">{list(cpu.features.cryptography)}</Row>
        <Row label="Security">{list(cpu.features.security)}</Row>
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
      </Table.Tbody>
    </Table>
  );
}
