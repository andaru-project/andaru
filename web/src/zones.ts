import type { Finding, Preview, SavedLink, Snapshot, Zone, ZoneColor } from './types';

const namePrefix: Record<string, string> = { vlan: 'vl', bridge: 'br', bond: 'bo', pppoe: 'pp' };

// kernelName mirrors internal/render: a virtual link's interface name derives from its ID, and an
// Ethernet link is the interface the preview resolved from its permanent MAC.
export function kernelName(link: SavedLink, resolved: Map<string, string>) {
  if (link.kind === 'ethernet') return resolved.get(link.id);
  return namePrefix[link.kind] + link.id.slice(link.id.indexOf('_') + 1).replaceAll('-', '').slice(0, 12);
}

// These findings mean the renderer will not put the link on any device, whatever its name matches.
const bindingCode = /^(HARDWARE_|NAME_COLLISION$|DEPENDENCY_UNAVAILABLE$|SECRET_UNRESOLVED$|UNSUPPORTED_COMBINATION$|INTERFACE_NAME_UNSUPPORTED$)/;

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

// A zone keeps one colour everywhere. When an explicit colour is saved on the zone it takes precedence;
// otherwise the two every box has take green (LAN) and red (WAN) and any other zone takes a deterministic
// shade from its name. Every colour in the palette has a legible outline on both light and dark grounds.
export const zoneColors: readonly ZoneColor[] = ['green', 'red', 'blue', 'orange', 'teal', 'pink', 'cyan', 'lime', 'violet', 'indigo'];
const zoneColorSet = new Set<string>(zoneColors);
const known = new Map<string, ZoneColor>([['lan', 'green'], ['wan', 'red']]);
const palette: readonly ZoneColor[] = ['blue', 'orange', 'teal', 'pink', 'cyan', 'lime'];

export function zoneColor(name: string, explicit?: string): ZoneColor {
  if (explicit && zoneColorSet.has(explicit)) return explicit as ZoneColor;
  const lower = name.toLowerCase();
  let hash = 5381;
  for (const character of lower) hash = (hash * 33 + character.codePointAt(0)!) % 2147483647;
  return known.get(lower) ?? palette[hash % palette.length];
}

export function zoneColorMap(zones: Zone[]): Map<string, ZoneColor> {
  const out = new Map<string, ZoneColor>();
  for (const z of zones) {
    if (z.color && zoneColorSet.has(z.color)) out.set(z.id, z.color);
  }
  return out;
}
