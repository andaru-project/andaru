import { Badge, VisuallyHidden } from '@mantine/core';
import { zoneColor } from '../zones';

// The zone an interface is in. The colour helps the eye; the name is what says it, and "zone" before it is for
// whoever hears the page and not the pill's shape.
export function ZoneBadge({ name }: { name: string | undefined }) {
  return name ? <Badge color={zoneColor(name)} variant="light"><VisuallyHidden>zone {name}</VisuallyHidden><span aria-hidden="true">{name}</span></Badge> : null;
}
