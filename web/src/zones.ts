import type { Finding, Preview, SavedLink, Snapshot } from './types';

const namePrefix: Record<string, string> = { vlan: 'vl', bridge: 'br', bond: 'bo', pppoe: 'pp' };

// kernelName mirrors internal/render: a virtual link's interface name derives from its ID, and an
// Ethernet link is the interface the preview resolved from its permanent MAC.
export function kernelName(link: SavedLink, resolved: Map<string, string>) {
  if (link.kind === 'ethernet') return resolved.get(link.id);
  return namePrefix[link.kind] + link.id.slice(link.id.indexOf('_') + 1).replaceAll('-', '').slice(0, 12);
}

// These findings mean the renderer will not put the link on any device, whatever its name matches.
const bindingCode = /^(HARDWARE_|NAME_COLLISION$|DEPENDENCY_UNAVAILABLE$)/;

// bindingFindings is the first such finding of each saved link, by the link's place in the configuration.
export function bindingFindings(preview: Preview) {
  const findings = new Map<number, Finding>();
  for (const finding of preview.findings) {
    const index = /^\/links\/(\d+)(\/|$)/.exec(finding.path)?.[1];
    if (index !== undefined && bindingCode.test(finding.code) && !findings.has(Number(index))) findings.set(Number(index), finding);
  }
  return findings;
}

// zonesByInterface says which zone the saved configuration puts each interface in, by the interface's name on
// the box. It is the saved assignment: until it is applied, the live one may differ. An interface gets a zone
// only when the configuration is clear about it: the link is enabled and will be bound, and no other link
// claims the same interface, enabled or not: two links on one interface is a conflict to sort out, not a zone.
export function zonesByInterface(snapshot: Snapshot, preview: Preview) {
  const resolved = new Map(preview.resolved.map((item) => [item.link, item.interface]));
  const findings = bindingFindings(preview);
  const zones = new Map<string, string>();
  const claimed = new Set<string>();
  const contested = new Set<string>();
  snapshot.value.links.forEach((link, index) => {
    const name = findings.has(index) ? undefined : kernelName(link, resolved);
    if (!name) return;
    if (claimed.has(name)) contested.add(name);
    claimed.add(name);
    const segment = snapshot.value.segments.find((item) => item.link === link.id);
    const zone = segment && snapshot.value.zones.find((item) => item.id === segment.zone);
    if (zone && link.enabled) zones.set(name, zone.name);
  });
  for (const name of contested) zones.delete(name);
  return zones;
}

// A zone keeps one colour everywhere. The two every box has take the colours firewalls have long given them,
// green inside and red outside; any other zone takes one from its name, so it is the same on every page. Each
// of these gets dark text from the theme at 5.6:1 or better, where grape, indigo and violet would get white
// under 4.5:1; and each has a shade that shows as a line on the light ground, which yellow has not.
const known = new Map([['lan', 'green'], ['wan', 'red']]);
const palette = ['blue', 'orange', 'teal', 'pink', 'cyan', 'lime'];
export function zoneColor(name: string) {
  const lower = name.toLowerCase();
  let hash = 5381;
  for (const character of lower) hash = (hash * 33 + character.codePointAt(0)!) % 2147483647;
  return known.get(lower) ?? palette[hash % palette.length];
}
