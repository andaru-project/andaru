import { afterEach, describe, expect, it } from 'vitest';
import { formatTime, formatTimestamp, getTimeZone, setTimeZone, timeZoneOptions } from './timezone';

afterEach(() => {
  setTimeZone('local');
});

describe('timezone formatting', () => {
  it('formats timestamps with explicit numeric offsets in ISO-like order', () => {
    const instant = '2026-11-23T18:24:36Z';
    expect(formatTimestamp(instant, 'UTC')).toBe('2026-11-23 18:24:36 +00:00');
    expect(formatTimestamp(instant, 'Europe/Berlin')).toBe('2026-11-23 19:24:36 +01:00');
    expect(formatTimestamp(instant, 'America/New_York')).toBe('2026-11-23 13:24:36 -05:00');
    expect(formatTimestamp(instant, 'Asia/Kolkata')).toBe('2026-11-23 23:54:36 +05:30');
  });

  it('formats times of day in 24-hour notation in the chosen time zone', () => {
    const instant = '2026-11-23T18:24:36Z';
    expect(formatTime(instant, 'UTC')).toBe('18:24:36');
    expect(formatTime(instant, 'Europe/Berlin')).toBe('19:24:36');
    expect(formatTime(instant, 'America/New_York')).toBe('13:24:36');
  });

  it('returns an em dash for invalid dates', () => {
    expect(formatTimestamp('not-a-date', 'UTC')).toBe('—');
    expect(formatTime('not-a-date', 'UTC')).toBe('—');
  });

  it('stores valid time zones and falls back to local for invalid ones', () => {
    setTimeZone('Europe/Berlin');
    expect(getTimeZone()).toBe('Europe/Berlin');
    setTimeZone('Mars/Olympus_Mons');
    expect(getTimeZone()).toBe('local');
  });

  it('offers browser local, UTC, and supported IANA time zones', () => {
    const options = timeZoneOptions();
    expect(options[0].value).toBe('local');
    expect(options[1]).toEqual({ value: 'UTC', label: 'UTC (+00:00)' });
    expect(options.some((option) => option.value === 'Europe/Berlin')).toBe(true);
  });
});
