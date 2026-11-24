// SPDX-License-Identifier: AGPL-3.0-or-later
import { Badge, Button, Group, type MantineSpacing, Text } from '@mantine/core';
import type { CSSProperties, ReactNode } from 'react';
import { Link } from 'react-router';
import type { Device, VLANFlowFinding, VLANMode } from '../api';
import { devicePath, vlanFlowPath } from '../routing';
import { findingKey, modeShort, onActivate, portLabel } from '../vlanUtils';
import { FindingBadge } from './Badges';

export function CardHeader({
  children,
  gap = 'xs',
  py = 'sm',
  style,
}: {
  children: ReactNode;
  gap?: MantineSpacing;
  py?: MantineSpacing;
  style?: CSSProperties;
}) {
  return (
    <Group
      justify="space-between"
      px="md"
      py={py}
      wrap="wrap"
      gap={gap}
      style={{ borderBottom: '1px solid var(--np-border-subtle)', ...style }}
    >
      {children}
    </Group>
  );
}

export function Stat({
  label,
  children,
  mb,
  className,
}: {
  label: string;
  children: ReactNode;
  mb?: number;
  className?: string;
}) {
  return (
    <div className={className}>
      <Text size="10px" fw={700} tt="uppercase" c="dimmed" mb={mb}>
        {label}
      </Text>
      {children}
    </div>
  );
}

export function PortSide({
  label,
  vlanId,
  mode,
  pvid,
  tagged,
}: {
  label: string;
  vlanId: number;
  mode: VLANMode | null;
  pvid?: number;
  tagged: number[];
}) {
  const reported = pvid != null || tagged.length > 0;
  return (
    <div className="np-port-compare-side">
      <Text size="xs" fw={700} ff="monospace">{label}</Text>
      {mode && (
        <Text size="xs" ff="monospace" mt={4}>
          VLAN {vlanId} mode: <strong>{mode} ({modeShort(mode)})</strong>
        </Text>
      )}
      {reported && (
        <>
          <Text size="xs" ff="monospace" mt={2}>
            PVID: <strong>{pvid ?? '—'}</strong>
          </Text>
          <Text size="xs" ff="monospace">
            Tagged: <strong>{tagged.length > 0 ? tagged.join(', ') : 'none'}</strong>
          </Text>
        </>
      )}
    </div>
  );
}

export function PortButton({
  devicesById,
  deviceId,
  link,
  prefix = '',
  suffix = '',
  onClick,
}: {
  devicesById: Map<string, Device>;
  deviceId: string;
  link?: string | null;
  prefix?: string;
  suffix?: string;
  onClick?: () => void;
}) {
  const text = `${prefix}${portLabel(devicesById, deviceId, link)}${suffix} →`;
  if (onClick) {
    return (
      <Button size="compact-xs" variant="default" onClick={onClick}>
        {text}
      </Button>
    );
  }
  return (
    <Button size="compact-xs" variant="default" component={Link} to={devicePath(deviceId, link)}>
      {text}
    </Button>
  );
}

export function FindingCallout({
  finding,
  title,
  body,
  actionLabel,
  onAction,
}: {
  finding: VLANFlowFinding;
  title?: string;
  body?: string;
  actionLabel?: string;
  onAction?: () => void;
}) {
  const to = finding.vlans[0] != null
    ? vlanFlowPath({ mode: 'single', vlanId: finding.vlans[0], findingKey: findingKey(finding) })
    : null;
  // Resolved before the layout: a caller's onAction wins, the trace link is the fallback.
  const action = (variant?: 'subtle') =>
    !actionLabel ? null : onAction ? (
      <Button size="compact-xs" variant={variant} onClick={onAction}>{actionLabel}</Button>
    ) : to ? (
      <Button size="compact-xs" variant={variant} component={Link} to={to}>{actionLabel}</Button>
    ) : null;
  if (title) {
    const icon = finding.kind === 'leak' ? '⚠' : finding.kind === 'tag_mismatch' ? '⇄' : '⊘';
    return (
      <div className={`np-callout ${finding.severity}`}>
        <Group justify="space-between" align="flex-start" wrap="wrap" gap="xs">
          <div style={{ flex: 1, minWidth: 200 }}>
            <Text size="xs" fw={700}>{icon} {title}</Text>
            <Text size="xs" mt={3}>{body ?? finding.message}</Text>
          </div>
          {action()}
        </Group>
      </div>
    );
  }
  return (
    <div className={`np-callout ${finding.severity}`}>
      <Group justify="space-between" mb={4}>
        <FindingBadge finding={finding} />
        {action('subtle')}
      </Group>
      <Text size="xs">{body ?? finding.message}</Text>
    </div>
  );
}

export function FindingCounts({
  findings,
  empty,
  gap = 6,
}: {
  findings: VLANFlowFinding[];
  empty?: ReactNode;
  gap?: MantineSpacing;
}) {
  const leak = findings.filter((f) => f.kind === 'leak').length;
  const dead = findings.filter((f) => f.kind === 'dead_end').length;
  const mismatch = findings.filter((f) => f.kind === 'tag_mismatch').length;
  return (
    <Group gap={gap} wrap="wrap">
      {leak > 0 && <Badge size="xs" variant="light" color="red">{leak} Leak</Badge>}
      {dead > 0 && <Badge size="xs" variant="light" color="yellow">{dead} Dead-end</Badge>}
      {mismatch > 0 && <Badge size="xs" variant="light" color="orange">{mismatch} Mismatch</Badge>}
      {findings.length === 0 && empty}
    </Group>
  );
}

export function AlertRow({
  severity,
  selected,
  badge,
  title,
  subtitle,
  subtitleMono,
  action,
  onClick,
  style,
  children,
}: {
  severity?: 'crit' | 'warn';
  selected?: boolean;
  badge?: ReactNode;
  title?: ReactNode;
  subtitle?: ReactNode;
  subtitleMono?: boolean;
  action?: ReactNode;
  onClick?: () => void;
  style?: CSSProperties;
  children?: ReactNode;
}) {
  const cls = ['np-alert-row', severity, selected ? 'selected' : ''].filter(Boolean).join(' ');
  return (
    <div
      className={cls}
      style={onClick ? { cursor: 'pointer', ...style } : style}
      role={onClick ? 'button' : undefined}
      tabIndex={onClick ? 0 : undefined}
      onClick={onClick}
      onKeyDown={onClick ? onActivate(onClick) : undefined}
    >
      {children ?? (
        <>
          <div>
            <Group gap="xs" mb={2} wrap="wrap">
              {badge}
              {title}
            </Group>
            {subtitle && (
              <Text size="xs" c="dimmed" ff={subtitleMono ? 'monospace' : undefined}>
                {subtitle}
              </Text>
            )}
          </div>
          {action}
        </>
      )}
    </div>
  );
}
