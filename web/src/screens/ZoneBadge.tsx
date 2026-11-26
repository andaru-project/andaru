import { Badge, Pill, VisuallyHidden, type ComboboxRenderPillInput } from '@mantine/core';
import type { ReactNode } from 'react';
import { firewallZone } from '../types';
import { zoneColor } from '../zones';

// The zone an interface is in. The colour helps the eye, as a tint and an edge; the name is what says it, in
// the page's own text colour so it reads on any tint, and "zone" before it is for whoever hears the page and
// not the pill's shape.
export function ZoneBadge({ name, color }: { name: string | undefined; color?: string }) {
  return name ? <ZonePill name={name} color={color}><VisuallyHidden>zone {name}</VisuallyHidden><span aria-hidden="true">{name}</span></ZonePill> : null;
}

// ZonePill is the zone's look alone, for a list of zones to choose from, where the name says all.
export function ZonePill({ name, color, children }: { name: string; color?: string; children?: ReactNode }) {
  const shade = zoneColor(name, color);
  return <Badge color={shade} variant="light" tt="none" style={{ '--badge-color': 'var(--mantine-color-text)', '--badge-bd': `1px solid var(--mantine-color-${shade}-outline)` }}>{children ?? name}</Badge>;
}

// Selected zone pill for Mantine MultiSelects: coloured by saved zone colour or zone name; firewallZone and missing IDs stay plain.
export function ZoneSelectionPill({ option, value, onRemove, disabled, colors }: ComboboxRenderPillInput & { colors?: Map<string, string> }) {
  const label = option?.label ?? String(value);
  const coloured = option !== undefined && option.value !== firewallZone && !label.endsWith(' (missing)');
  const shade = coloured ? zoneColor(label, colors?.get(String(option.value))) : undefined;
  return (
    <Pill
      withRemoveButton={!disabled && !option?.disabled}
      onRemove={onRemove}
      removeButtonProps={{ 'aria-label': `Remove ${label}` }}
      style={shade ? { background: `var(--mantine-color-${shade}-light)`, boxShadow: `inset 0 0 0 1px var(--mantine-color-${shade}-outline)` } : undefined}
    >
      {label}
    </Pill>
  );
}
