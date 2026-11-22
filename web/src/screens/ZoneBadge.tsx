import { Badge, VisuallyHidden } from '@mantine/core';
import { zoneColor } from '../zones';

// The zone an interface is in. The colour helps the eye, as a tint and an edge; the name is what says it, in
// the page's own text colour so it reads on any tint, and "zone" before it is for whoever hears the page and
// not the pill's shape.
export function ZoneBadge({ name }: { name: string | undefined }) {
  return name ? <Badge color={zoneColor(name)} variant="light" tt="none" style={{ '--badge-color': 'var(--mantine-color-text)', '--badge-bd': `1px solid var(--mantine-color-${zoneColor(name)}-outline)` }}><VisuallyHidden>zone {name}</VisuallyHidden><span aria-hidden="true">{name}</span></Badge> : null;
}
