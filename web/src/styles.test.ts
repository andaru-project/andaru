import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { expect, it } from 'vitest';

// The build rewrites light-dark() into two variables that nothing sets, so a colour written with it is no
// colour at all in production: the page's ground, error text and diff tints were lost that way. Colours for
// the dark scheme go under its selector instead.
const read = (file: string) => readFileSync(file, 'utf8').replace(/\/\*[\s\S]*?\*\//g, '');

function listTsxFiles(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) out.push(...listTsxFiles(full));
    else if (entry.name.endsWith('.tsx') && !entry.name.endsWith('.test.tsx')) out.push(full);
  }
  return out;
}

it('uses no light-dark() in the stylesheets', () => {
  for (const file of ['src/styles.css', 'src/theme.css', 'src/netplane/styles.css']) expect(read(file)).not.toContain('light-dark(');
});

it('gives each colour it took out of light-dark() its dark value under the dark scheme', () => {
  const dark = /:root\[data-mantine-color-scheme="dark"\] \{([^}]*)\}/.exec(read('src/styles.css'))?.[1] ?? '';
  for (const name of ['--andaru-ground', '--andaru-error-text', '--andaru-warning-text', '--andaru-diff-old', '--andaru-diff-new']) expect(dark).toContain(`${name}:`);
});

// The navbar is as tall as the viewport: the links under the fold are reached by scrolling it.
it('scrolls the navigation', () => {
  expect(/\.app-navbar \{[^}]*overflow-y: auto/.test(read('src/styles.css'))).toBe(true);
});

// On a phone a dialog is as wide as the screen, not a share of the page: a dropdown that overflowed the page
// widened the dialog with it and the two chased each other.
it('sizes a phone dialog by the screen', () => {
  expect(/\.mantine-Modal-content \{[^}]*flex: 0 0 100vw/.test(read('src/styles.css'))).toBe(true);
});

// Download and upload are told apart by more than strength: upload is dashed.
it('draws upload dashed', () => {
  expect(/path\.traffic-sent \{[^}]*stroke-dasharray/.test(read('src/styles.css'))).toBe(true);
});

// Andaru's CSP (style-src 'self') blocks Mantine InlineStyles <style> tags injected by SimpleGrid/Grid and responsive props,
// and Andaru's .data-table mobile stacking expects data-label attributes that Netplane tables do not carry.
it('uses no Mantine SimpleGrid/Grid, responsive style objects, or unlabelled .data-table under src/netplane/', () => {
  for (const file of listTsxFiles('src/netplane')) {
    const src = readFileSync(file, 'utf8');
    expect(src, `${file} must not use SimpleGrid or Grid`).not.toMatch(/\bSimpleGrid\b|<Grid\b/);
    expect(src, `${file} must not use responsive style objects`).not.toMatch(/=\{\{[^}]*\b(?:base|xs|sm|md|lg|xl)\s*:/);
    expect(src, `${file} must use np-data-table instead of data-table`).not.toContain('className="data-table"');
  }
});
