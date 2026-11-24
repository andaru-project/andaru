import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { expect, it } from 'vitest';

it('loads no runtime asset from another origin', async () => {
  for (const file of ['../index.html', '../netplane.html']) {
    const html = await readFile(join(import.meta.dirname, file), 'utf8');
    expect(html).not.toMatch(/(?:src|href)=["']https?:\/\//i);
  }
});
