import { expect, it } from 'vitest';
import { bytes, duration } from './format';

it('writes a size in the largest unit that keeps it readable', () => {
  expect(bytes(0)).toBe('0 B');
  expect(bytes(1023)).toBe('1023 B');
  expect(bytes(1536)).toBe('1.5 KiB');
  expect(bytes(16 * 1024 ** 3)).toBe('16 GiB');
  expect(bytes(25136041984)).toBe('23 GiB');
  expect(bytes(5 * 1024 ** 5)).toBe('5120 TiB');
  // The number as shown decides the unit: never "1024" of the unit below, never "10.0".
  expect(bytes(1024)).toBe('1.0 KiB');
  expect(bytes(1048575)).toBe('1.0 MiB');
  expect(bytes(1073217536)).toBe('1.0 GiB');
  expect(bytes(10199)).toBe('10 KiB');
});

it('writes a duration in its two largest units', () => {
  expect(duration(0)).toBe('less than a minute');
  expect(duration(59)).toBe('less than a minute');
  expect(duration(60)).toBe('1 minute');
  expect(duration(40 * 60)).toBe('40 minutes');
  expect(duration(5 * 3600 + 12 * 60)).toBe('5 hours 12 minutes');
  expect(duration(3600)).toBe('1 hour');
  expect(duration(3 * 86400 + 4 * 3600 + 59 * 60)).toBe('3 days 4 hours');
  expect(duration(2 * 86400 + 5 * 60)).toBe('2 days');
  expect(duration(86400)).toBe('1 day');
  expect(duration(-5)).toBe('less than a minute');
});
