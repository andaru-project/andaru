const downloadRevokeMilliseconds = 60_000;

function sanitizeFilename(raw: string): string | null {
  const trimmed = raw.trim();
  if (!trimmed || trimmed === '.' || trimmed === '..') return null;
  if (trimmed.includes('/') || trimmed.includes('\\')) return null;
  for (let i = 0; i < trimmed.length; i++) {
    const code = trimmed.charCodeAt(i);
    if (code <= 0x1f || code === 0x7f) return null;
  }
  return trimmed;
}

export function filenameFromDisposition(header: string | null, fallback: string): string {
  if (!header) return fallback;
  const star = /(?:^|;)\s*filename\*\s*=\s*UTF-8'[^']*'([^;\r\n]+)/i.exec(header);
  if (star?.[1]) {
    try {
      const safe = sanitizeFilename(decodeURIComponent(star[1].trim()));
      if (safe) return safe;
    } catch {
      // Fall through to plain filename parameter.
    }
  }
  const plain = /(?:^|;)\s*filename\s*=\s*(?:"([^"\r\n]*)"|([^;\s"\r\n]+))/i.exec(header);
  const candidate = plain?.[1] ?? plain?.[2] ?? '';
  return sanitizeFilename(candidate) ?? fallback;
}

export function triggerDownload(blob: Blob, filename: string): void {
  if (typeof URL.createObjectURL !== 'function') return;
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = filename;
  link.hidden = true;
  document.body.appendChild(link);
  try {
    link.click();
  } finally {
    link.remove();
    setTimeout(() => URL.revokeObjectURL?.(url), downloadRevokeMilliseconds);
  }
}
