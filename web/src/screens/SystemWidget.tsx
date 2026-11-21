import { ActionIcon, Popover, Table, Text } from '@mantine/core';
import { useCallback, useState, type ReactNode } from 'react';
import { bytes, duration, plural } from '../format';
import type { SystemInfo } from '../types';
import { useVisiblePoll } from '../useResource';

// Why each feature is worth a place on a firewall's front page, for the architecture that has it. The lists the
// box sends hold names only.
const reasons: Array<['x86' | 'arm' | 'any', string, string]> = [
  ['x86', 'AES-NI, PCLMULQDQ', 'AES-GCM in hardware: IPsec, OpenVPN and TLS. Without them AES runs several times slower.'],
  ['x86', 'VAES, VPCLMULQDQ', 'AES-GCM on several blocks at once: the next step up for IPsec and TLS on recent CPUs.'],
  ['x86', 'SHA-NI', 'SHA-1 and SHA-256 in hardware: VPN packet authentication, TLS, and checking signed images.'],
  ['x86', 'AVX2, AVX-512', "ChaCha20-Poly1305 in wide vectors: WireGuard's throughput."],
  ['x86', 'ADX, BMI2', 'Curve25519 arithmetic: WireGuard and TLS handshakes.'],
  ['x86', 'RDRAND, RDSEED', "Hardware entropy for the kernel's random numbers, which matters for keys made early in boot."],
  ['x86', 'Security', 'Protections the kernel uses against attacks on itself: NX, SMEP, SMAP, UMIP and CET.'],
  ['arm', 'AES, PMULL', 'AES-GCM in hardware: IPsec, OpenVPN and TLS. Without them AES runs several times slower.'],
  ['arm', 'SHA1, SHA2, SHA512, SHA3', 'Hashes in hardware: VPN packet authentication, TLS, and checking signed images.'],
  ['arm', 'ASIMD, SVE', "ChaCha20-Poly1305 in vectors: WireGuard's throughput."],
  ['arm', 'RNG', "Hardware entropy for the kernel's random numbers, which matters for keys made early in boot."],
  ['arm', 'Security', 'Protections the kernel uses against attacks on itself: PAuth, BTI and MTE.'],
  ['any', 'Virtualisation', 'Lets the box run virtual machines; an active IOMMU lets it hand them real devices.'],
];

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
  const family = info.architecture === 'aarch64' ? 'arm' : 'x86';

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
        <Row label="CPU features">
          {/* A click, a tap or the keyboard opens this, and nothing in it is reachable by hover alone. Focus goes
              into it and comes back, so Escape closes it from the keyboard. */}
          <Popover width="min(22.5rem, calc(100vw - 2rem))" position="bottom-start" withArrow trapFocus returnFocus>
            <Popover.Target>
              <ActionIcon variant="default" radius="xl" size="sm" aria-label="Why these CPU features matter"><Text span fw={700} size="xs">i</Text></ActionIcon>
            </Popover.Target>
            <Popover.Dropdown>
              <Text fw={600} size="sm" mb="xs" tabIndex={-1} data-autofocus>Why these CPU features matter to a firewall</Text>
              <dl className="reasons">
                {reasons.filter(([where]) => where === family || where === 'any').map(([, names, why]) => <div key={names}><dt>{names}</dt><dd>{why}</dd></div>)}
              </dl>
            </Popover.Dropdown>
          </Popover>
        </Row>
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
