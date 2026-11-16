import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { expect, it } from 'vitest';

it('loads no runtime asset from another origin', async () => {
  const html = await readFile(join(import.meta.dirname, '../index.html'), 'utf8');
  expect(html).not.toMatch(/(?:src|href)=["']https?:\/\//i);
});
