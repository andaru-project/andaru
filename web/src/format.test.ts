import { expect, it } from 'vitest';
import { bytes, since } from './format';

it('writes a size in the largest unit that keeps it readable', () => {
  expect(bytes(0)).toBe('0 B');
  expect(bytes(1023)).toBe('1023 B');
  expect(bytes(1536)).toBe('1.5 KiB');
  expect(bytes(16 * 1024 ** 3)).toBe('16 GiB');
  expect(bytes(25136041984)).toBe('23 GiB');
  expect(bytes(5 * 1024 ** 5)).toBe('5120 TiB');
});

it('writes a duration in its two largest units', () => {
  expect(since(0)).toBe('less than a minute');
  expect(since(59)).toBe('less than a minute');
  expect(since(60)).toBe('1 minute');
  expect(since(40 * 60)).toBe('40 minutes');
  expect(since(5 * 3600 + 12 * 60)).toBe('5 hours 12 minutes');
  expect(since(3600)).toBe('1 hour');
  expect(since(3 * 86400 + 4 * 3600 + 59 * 60)).toBe('3 days 4 hours');
  expect(since(2 * 86400 + 5 * 60)).toBe('2 days');
});
