import type { Preview, SavedLink, Snapshot } from './types';

const namePrefix: Record<string, string> = { vlan: 'vl', bridge: 'br', bond: 'bo', pppoe: 'pp' };

// kernelName mirrors internal/render: a virtual link's interface name derives from its ID, and an
// Ethernet link is the interface the preview resolved from its permanent MAC.
export function kernelName(link: SavedLink, resolved: Map<string, string>) {
  if (link.kind === 'ethernet') return resolved.get(link.id);
  return namePrefix[link.kind] + link.id.slice(link.id.indexOf('_') + 1).replaceAll('-', '').slice(0, 12);
}

// zonesByInterface says which zone the saved configuration puts each interface in, by the interface's name on
// the box. It is the saved assignment: until it is applied, the live one may differ.
export function zonesByInterface(snapshot: Snapshot, preview: Preview) {
  const resolved = new Map(preview.resolved.map((item) => [item.link, item.interface]));
  const zones = new Map<string, string>();
  for (const link of snapshot.value.links) {
    const name = kernelName(link, resolved);
    const segment = snapshot.value.segments.find((item) => item.link === link.id);
    const zone = segment && snapshot.value.zones.find((item) => item.id === segment.zone);
    if (name && zone) zones.set(name, zone.name);
  }
  return zones;
}

// A zone keeps one colour everywhere. The two every box has take the colours firewalls have long given them,
// green inside and red outside; any other zone takes one from its name, so it is the same on every page.
const palette = ['blue', 'grape', 'orange', 'teal', 'pink', 'indigo', 'cyan', 'violet'];
export function zoneColor(name: string) {
  const known: Record<string, string> = { lan: 'green', wan: 'red' };
  let sum = 0;
  for (const character of name) sum = (sum * 31 + character.codePointAt(0)!) % 9973;
  return known[name.toLowerCase()] ?? palette[sum % palette.length];
}
