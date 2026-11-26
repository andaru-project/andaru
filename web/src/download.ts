const downloadRevokeMilliseconds = 60_000;

export function filenameFromDisposition(header: string | null, fallback: string): string {
  if (!header) return fallback;
  const star = /filename\*\s*=\s*UTF-8''([^;\r\n]+)/i.exec(header);
  if (star?.[1]) {
    try {
      const decoded = decodeURIComponent(star[1].trim());
      if (decoded && !decoded.includes('/') && !decoded.includes('\\')) return decoded;
    } catch {
      // Fall through to plain filename parameter.
    }
  }
  const plain = /filename\s*=\s*(?:"([^"\r\n]+)"|([^;\s\r\n]+))/i.exec(header);
  const candidate = (plain?.[1] ?? plain?.[2] ?? '').trim();
  if (candidate && !candidate.includes('/') && !candidate.includes('\\')) return candidate;
  return fallback;
}

export function triggerDownload(blob: Blob, filename: string): void {
  if (typeof URL.createObjectURL !== 'function') return;
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = filename;
  link.hidden = true;
  document.body.appendChild(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL?.(url), downloadRevokeMilliseconds);
}
