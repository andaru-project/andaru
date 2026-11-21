const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
export function bytes(value: number) {
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${value >= 10 || unit === 0 ? Math.round(value) : value.toFixed(1)} ${units[unit]}`;
}

// since puts a duration in its two largest units: "3 days 4 hours", "5 hours 12 minutes", "40 minutes".
export function since(seconds: number) {
  const parts: Array<[number, string]> = [[Math.floor(seconds / 86400), 'day'], [Math.floor(seconds / 3600) % 24, 'hour'], [Math.floor(seconds / 60) % 60, 'minute']];
  const first = parts.findIndex(([count]) => count > 0);
  if (first < 0) return 'less than a minute';
  return parts.slice(first, first + 2).filter(([count]) => count > 0).map(([count, unit]) => `${count} ${unit}${count === 1 ? '' : 's'}`).join(' ');
}
