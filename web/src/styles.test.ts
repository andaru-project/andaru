import { readFileSync } from 'node:fs';
import { expect, it } from 'vitest';

// The build rewrites light-dark() into two variables that nothing sets, so a colour written with it is no
// colour at all in production: the page's ground, error text and diff tints were lost that way. Colours for
// the dark scheme go under its selector instead.
it('uses no light-dark() in the stylesheet', () => {
  const css = readFileSync('src/styles.css', 'utf8').replace(/\/\*[\s\S]*?\*\//g, '');
  expect(css).not.toContain('light-dark(');
});
