import { readFile } from 'node:fs/promises';
import { expect, it } from 'vitest';

it('loads no runtime asset from another origin', async () => {
  const html = await readFile(new URL('../index.html', import.meta.url), 'utf8');
  expect(html).not.toMatch(/(?:src|href)=["']https?:\/\//i);
});
