// SPDX-License-Identifier: AGPL-3.0-or-later
import { Badge, useComputedColorScheme } from '@mantine/core';
import type { VLANFlowFinding, VLANFlowVLAN } from '../api';
import { type ColorPair, getVlanColor, vlanHealth, vlanLabel } from '../vlanUtils';

export function DriverBadge({ driver }: { driver: string }) {
  if (driver === 'andaru') {
    return (
      <Badge
        size="xs"
        style={{ backgroundColor: '#ffb21c', color: '#17212c', fontWeight: 700 }}
      >
        {driver}
      </Badge>
    );
  }
  const colorMap: Record<string, string> = {
    openwrt: 'teal',
    rtlplayground: 'blue',
    unifi: 'cyan',
    pfsense: 'gray',
    opnsense: 'gray',
    snmp: 'gray',
  };
  return (
    <Badge size="xs" variant="light" color={colorMap[driver] ?? 'gray'}>
      {driver}
    </Badge>
  );
}

export function VlanChip({
  vid,
  vlansById,
  colorMap,
}: {
  vid?: number | null;
  vlansById: Map<number, VLANFlowVLAN>;
  colorMap: Map<number, ColorPair>;
}) {
  const isDark = useComputedColorScheme('light') === 'dark';
  if (vid == null) return <span style={{ color: 'var(--np-text-dim)' }}>—</span>;
  const color = getVlanColor(vid, colorMap, isDark);
  return (
    <Badge
      size="sm"
      variant="outline"
      color="gray"
      className="np-mono"
      styles={{ root: { textTransform: 'none', fontWeight: 600, color: 'var(--np-text)' } }}
      leftSection={<span className="np-vlan-swatch" style={{ background: color }} />}
    >
      {vlanLabel(vlansById, vid)}
    </Badge>
  );
}

export function FindingBadge({ finding }: { finding: Pick<VLANFlowFinding, 'kind' | 'severity'> }) {
  if (finding.kind === 'leak') {
    return <Badge size="xs" variant="light" color="red">CRIT · LEAK</Badge>;
  }
  if (finding.kind === 'tag_mismatch') {
    return <Badge size="xs" variant="light" color="orange">WARN · MISMATCH</Badge>;
  }
  return <Badge size="xs" variant="light" color="yellow">WARN · DEAD-END</Badge>;
}

export function VlanHealthBadge({
  vlan,
  findings,
  compact = false,
}: {
  vlan: VLANFlowVLAN;
  findings: VLANFlowFinding[];
  compact?: boolean;
}) {
  switch (vlanHealth(vlan, findings)) {
    case 'leak':
      return <Badge size="xs" variant="light" color="red">⚠ Leak</Badge>;
    case 'dead_end':
      return <Badge size="xs" variant="light" color="yellow">{compact ? '⊘ Dead-end' : '⊘ Dead-End'}</Badge>;
    case 'mismatch':
      return <Badge size="xs" variant="light" color="orange">⇄ Mismatch</Badge>;
    case 'unverified':
      return <Badge size="xs" variant="outline" color="gray">{compact ? '?' : '? Unverified'}</Badge>;
    case 'ok':
      return <Badge size="xs" variant="light" color="green">{compact ? '✓' : '✓ Clean'}</Badge>;
  }
}
