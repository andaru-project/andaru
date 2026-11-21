import { ActionIcon, Popover, Table, Text } from '@mantine/core';
import { useEffect, useState, type ReactNode } from 'react';
import { bytes, since } from '../format';
import type { SystemInfo } from '../types';

// Why each feature is worth a place on a firewall's front page. The lists the box sends hold names only.
const reasons: Array<[string, string]> = [
  ['AES-NI, PCLMULQDQ', 'AES-GCM in hardware: IPsec, OpenVPN and TLS. Without them AES runs several times slower.'],
  ['VAES, VPCLMULQDQ', 'AES-GCM on several blocks at once: the next step up for IPsec and TLS on recent CPUs.'],
  ['SHA-NI', 'SHA-1 and SHA-256 in hardware: VPN packet authentication, TLS, and checking signed images.'],
  ['AVX2, AVX-512', "ChaCha20-Poly1305 in wide vectors: WireGuard's throughput."],
  ['ADX, BMI2', 'Curve25519 arithmetic: WireGuard and TLS handshakes.'],
  ['RDRAND, RDSEED', "Hardware entropy for the kernel's random numbers, which matters for keys made early in boot."],
  ['AES, PMULL (arm64)', 'AES-GCM in hardware, as AES-NI and PCLMULQDQ.'],
  ['SHA1, SHA2, SHA512, SHA3 (arm64)', 'Hashes in hardware.'],
  ['ASIMD, SVE (arm64)', "ChaCha20-Poly1305 in vectors: WireGuard's throughput."],
  ['RNG (arm64)', 'Hardware entropy.'],
  ['Security', 'Protections the kernel uses against attacks on itself: NX, SMEP, SMAP, UMIP and CET on x86; PAuth, BTI and MTE on arm64.'],
  ['Virtualisation', 'VT-x or AMD-V let the box run virtual machines; an active IOMMU lets it hand them real devices.'],
];

function Row({ label, children }: { label: string; children: ReactNode }) {
  return <Table.Tr><Table.Th scope="row">{label}</Table.Th><Table.Td>{children}</Table.Td></Table.Tr>;
}

const list = (names: string[]) => (names.length > 0 ? names.join(', ') : 'none');

// loadedAt is the browser's clock when the description arrived: the box's clock may differ from the browser's,
// so the uptime is the box's own at that moment plus the time since.
export function SystemWidget({ info, loadedAt }: { info: SystemInfo; loadedAt: number }) {
  // The uptime moves on by the browser's clock, a minute at a time, without asking the box again.
  const [now, setNow] = useState(loadedAt);
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 60_000);
    return () => clearInterval(timer);
  }, []);
  const { cpu, hardware } = info;
  const uptime = info.bootedAt ? (Date.parse(info.time) - Date.parse(info.bootedAt) + now - loadedAt) / 1000 : undefined;
  const vulnerable = cpu.vulnerabilities.filter((item) => item.state === 'vulnerable').map((item) => item.name);
  const unknown = cpu.vulnerabilities.filter((item) => item.state === 'unknown').map((item) => item.name);
  const firmware = [hardware.biosVendor, hardware.biosVersion, hardware.biosDate].filter(Boolean).join(' ');
  const machine = [hardware.vendor, hardware.product].filter(Boolean).join(' ');
  const board = [hardware.boardVendor, hardware.board].filter(Boolean).join(' ');

  return (
      <Table className="facts" withRowBorders={false} verticalSpacing={2} horizontalSpacing={0}>
        <Table.Tbody>
          <Row label="Name">{info.hostname || '—'}</Row>
          <Row label="Andaru">{info.version}</Row>
          <Row label="Kernel">{info.kernel} ({info.architecture})</Row>
          {uptime !== undefined && <Row label="Up for">{since(Math.max(uptime, 0))}</Row>}
          <Row label="Machine">{machine || '—'}</Row>
          {info.guest && <Row label="Runs as">a virtual machine ({info.guest})</Row>}
          {board && board !== machine && <Row label="Board">{board}</Row>}
          {hardware.serial && <Row label="Serial">{hardware.serial}</Row>}
          {firmware && <Row label="BIOS">{firmware}</Row>}
          <Row label="CPU">{cpu.model || info.architecture}, {cpu.cores === cpu.threads ? `${cpu.cores} cores` : `${cpu.cores} cores, ${cpu.threads} threads`}</Row>
          {cpu.level && <Row label="Level">{cpu.level}</Row>}
          {cpu.microcode && <Row label="Microcode">{cpu.microcode}</Row>}
          <Row label="Memory">{bytes(info.memoryBytes)}</Row>
          <Row label="CPU features">
            {/* A click, a tap or the keyboard opens this; nothing in it is reachable by hover alone. */}
            <Popover width={360} position="bottom-start" withArrow>
              <Popover.Target>
                <ActionIcon variant="default" radius="xl" size="sm" aria-label="Why these CPU features matter"><Text span fw={700} size="xs">i</Text></ActionIcon>
              </Popover.Target>
              <Popover.Dropdown>
                <Text fw={600} size="sm" mb="xs">Why these CPU features matter to a firewall</Text>
                <dl className="reasons">
                  {reasons.map(([names, why]) => <div key={names}><dt>{names}</dt><dd>{why}</dd></div>)}
                </dl>
              </Popover.Dropdown>
            </Popover>
          </Row>
          <Row label="Cryptography">{list(cpu.features.cryptography)}</Row>
          <Row label="Security">{list(cpu.features.security)}</Row>
          <Row label="Virtualisation">{list([...cpu.features.virtualization, ...(info.iommu ? ['IOMMU active'] : [])])}</Row>
          <Row label="CPU vulnerabilities">
            {cpu.vulnerabilities.length === 0 ? 'no report' : vulnerable.length === 0 && unknown.length === 0 ? 'all mitigated' : (
              <>
                {vulnerable.length > 0 && <Text span inherit className="error-text">not mitigated: {vulnerable.join(', ')}</Text>}
                {vulnerable.length > 0 && unknown.length > 0 && '; '}
                {unknown.length > 0 && <>unknown: {unknown.join(', ')}</>}
              </>
            )}
          </Row>
        </Table.Tbody>
      </Table>
  );
}
