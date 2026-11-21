const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];

// bytes writes a size in the largest unit that keeps it readable: one decimal below ten, none from ten up.
export function bytes(value: number) {
  let unit = 0;
  // The number as it will be shown decides the unit, so 1023.9 MiB reads 1.0 GiB and not 1024 MiB.
  const shown = () => (unit === 0 || value >= 9.95 ? Math.round(value) : Math.round(value * 10) / 10);
  while (shown() >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${unit > 0 && shown() < 10 ? shown().toFixed(1) : shown()} ${units[unit]}`;
}

// duration writes a length of time in its two largest units: "3 days 4 hours", "5 hours 12 minutes", "40 minutes".
export function duration(seconds: number) {
  const parts: Array<[number, string]> = [[Math.floor(seconds / 86400), 'day'], [Math.floor(seconds / 3600) % 24, 'hour'], [Math.floor(seconds / 60) % 60, 'minute']];
  const first = parts.findIndex(([count]) => count > 0);
  if (first < 0) return 'less than a minute';
  return parts.slice(first, first + 2).filter(([count]) => count > 0).map(([count, unit]) => plural(count, unit)).join(' ');
}

export const plural = (count: number, unit: string) => `${count} ${unit}${count === 1 ? '' : 's'}`;
