import { Badge } from '@mantine/core';
import { zoneColor } from '../zones';

// The zone an interface is in. The colour helps the eye; the name is what says it.
export function ZoneBadge({ name }: { name: string | undefined }) {
  return name ? <Badge color={zoneColor(name)} aria-label={`zone ${name}`}>{name}</Badge> : null;
}
