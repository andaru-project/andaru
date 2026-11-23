import { createElement } from 'react';
import { afterEach, describe, expect, it } from 'vitest';
import { formatTime, formatTimestamp, getTimeZone, setTimeZone, timeZoneOptions, useTimeZone } from './timezone';
import { act, render, screen } from './test/render';

afterEach(() => {
  setTimeZone('local');
});

describe('timezone formatting', () => {
  it('formats timestamps with explicit numeric offsets in ISO-like order, including DST transitions', () => {
    const winter = '2026-11-23T18:24:36Z';
    const summer = '2026-07-15T18:24:36Z';
    expect(formatTimestamp(winter, 'UTC')).toBe('2026-11-23 18:24:36 +00:00');
    expect(formatTimestamp(winter, 'Europe/Berlin')).toBe('2026-11-23 19:24:36 +01:00');
    expect(formatTimestamp(summer, 'Europe/Berlin')).toBe('2026-07-15 20:24:36 +02:00');
    expect(formatTimestamp(winter, 'America/New_York')).toBe('2026-11-23 13:24:36 -05:00');
    expect(formatTimestamp(winter, 'Asia/Kolkata')).toBe('2026-11-23 23:54:36 +05:30');
  });

  it('formats times of day in 24-hour notation including midnight in the chosen time zone', () => {
    expect(formatTime('2026-11-23T18:24:36Z', 'UTC')).toBe('18:24:36');
    expect(formatTime('2026-11-23T23:05:09Z', 'Europe/Berlin')).toBe('00:05:09');
    expect(formatTime('2026-11-23T18:24:36Z', 'America/New_York')).toBe('13:24:36');
  });

  it('falls back safely on invalid dates and invalid explicit time zones', () => {
    expect(formatTimestamp('not-a-date', 'UTC')).toBe('—');
    expect(formatTime('not-a-date', 'UTC')).toBe('—');
    expect(formatTimestamp('2026-11-23T18:24:36Z', '')).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} [+-]\d{2}:\d{2}$/);
    expect(formatTimestamp('2026-11-23T18:24:36Z', 'Mars/Olympus_Mons')).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} [+-]\d{2}:\d{2}$/);
    expect(formatTime('2026-11-23T18:24:36Z', 'Mars/Olympus_Mons')).toMatch(/^\d{2}:\d{2}:\d{2}$/);
  });

  it('stores valid time zones, falls back to local for invalid ones, and reacts to cross-tab storage clear', () => {
    function Probe() {
      return createElement('span', null, useTimeZone());
    }
    render(createElement(Probe));
    expect(screen.getByText('local')).toBeInTheDocument();

    act(() => { setTimeZone('Europe/Berlin'); });
    expect(getTimeZone()).toBe('Europe/Berlin');
    expect(screen.getByText('Europe/Berlin')).toBeInTheDocument();

    act(() => { window.dispatchEvent(new StorageEvent('storage', { key: null })); });
    expect(getTimeZone()).toBe('local');
    expect(screen.getByText('local')).toBeInTheDocument();

    act(() => { setTimeZone('Mars/Olympus_Mons'); });
    expect(getTimeZone()).toBe('local');
  });

  it('offers browser local, UTC, supported IANA time zones, and preserves an active legacy alias', () => {
    const options = timeZoneOptions('US/Eastern');
    expect(options[0].value).toBe('local');
    expect(options[1]).toEqual({ value: 'UTC', label: 'UTC (+00:00)' });
    expect(options.some((option) => option.value === 'Europe/Berlin')).toBe(true);
    expect(options.some((option) => option.value === 'US/Eastern')).toBe(true);
  });
});
