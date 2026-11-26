import { afterEach, describe, expect, it, vi } from 'vitest';
import { filenameFromDisposition, triggerDownload } from './download';

afterEach(() => {
  vi.restoreAllMocks();
});

describe('filenameFromDisposition', () => {
  it('parses plain, quoted, and RFC 8187 UTF-8 filenames', () => {
    expect(filenameFromDisposition('attachment; filename="audit-2026.json"', 'fallback.json')).toBe('audit-2026.json');
    expect(filenameFromDisposition('attachment; filename=audit.json', 'fallback.json')).toBe('audit.json');
    expect(filenameFromDisposition("attachment; filename*=UTF-8'en'audit%202026.json", 'fallback.json')).toBe('audit 2026.json');
    expect(filenameFromDisposition("attachment; filename*=UTF-8''%E2%9C%93.json; filename=plain.json", 'fallback.json')).toBe('✓.json');
  });

  it('falls back on empty, missing-boundary, traversal, or control-char filenames', () => {
    expect(filenameFromDisposition(null, 'fallback.json')).toBe('fallback.json');
    expect(filenameFromDisposition('attachment; filename=""', 'fallback.json')).toBe('fallback.json');
    expect(filenameFromDisposition('attachment; x-filename="evil.json"', 'fallback.json')).toBe('fallback.json');
    expect(filenameFromDisposition('attachment; filename=".."', 'fallback.json')).toBe('fallback.json');
    expect(filenameFromDisposition('attachment; filename="."', 'fallback.json')).toBe('fallback.json');
    expect(filenameFromDisposition("attachment; filename*=UTF-8''%2e%2e", 'fallback.json')).toBe('fallback.json');
    expect(filenameFromDisposition('attachment; filename="../secret.json"', 'fallback.json')).toBe('fallback.json');
    expect(filenameFromDisposition('attachment; filename="dir\\secret.json"', 'fallback.json')).toBe('fallback.json');
    expect(filenameFromDisposition("attachment; filename*=UTF-8''bad%00name.json", 'fallback.json')).toBe('fallback.json');
    expect(filenameFromDisposition("attachment; filename*=UTF-8''%20%20", 'fallback.json')).toBe('fallback.json');
  });
});

describe('triggerDownload', () => {
  it('creates an object URL, clicks a hidden anchor with download name, and removes it even if click throws', () => {
    vi.useFakeTimers();
    const origCreate = Object.getOwnPropertyDescriptor(URL, 'createObjectURL');
    const origRevoke = Object.getOwnPropertyDescriptor(URL, 'revokeObjectURL');
    const createObjectURL = vi.fn(() => 'blob:test');
    const revokeObjectURL = vi.fn();
    Object.defineProperty(URL, 'createObjectURL', { configurable: true, writable: true, value: createObjectURL });
    Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, writable: true, value: revokeObjectURL });
    let capturedDownload = '';
    let capturedHidden = false;
    let shouldThrow = false;
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      capturedDownload = this.download;
      capturedHidden = Boolean(this.hidden);
      if (shouldThrow) throw new Error('blocked click');
    });
    try {
      triggerDownload(new Blob(['{}']), 'audit.json');
      expect(createObjectURL).toHaveBeenCalledTimes(1);
      expect(capturedDownload).toBe('audit.json');
      expect(capturedHidden).toBe(true);
      expect(document.querySelector('a[download="audit.json"]')).toBeNull();

      shouldThrow = true;
      expect(() => triggerDownload(new Blob(['{}']), 'failing.json')).toThrow('blocked click');
      expect(document.querySelector('a[download="failing.json"]')).toBeNull();

      vi.runAllTimers();
      expect(revokeObjectURL).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
      clickSpy.mockRestore();
      if (origCreate) Object.defineProperty(URL, 'createObjectURL', origCreate);
      else delete (URL as unknown as Record<string, unknown>).createObjectURL;
      if (origRevoke) Object.defineProperty(URL, 'revokeObjectURL', origRevoke);
      else delete (URL as unknown as Record<string, unknown>).revokeObjectURL;
    }
  });
});
