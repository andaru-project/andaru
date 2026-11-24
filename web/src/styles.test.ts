import { readFileSync } from 'node:fs';
import { expect, it } from 'vitest';

// The build rewrites light-dark() into two variables that nothing sets, so a colour written with it is no
// colour at all in production: the page's ground, error text and diff tints were lost that way. Colours for
// the dark scheme go under its selector instead.
const read = (file: string) => readFileSync(file, 'utf8').replace(/\/\*[\s\S]*?\*\//g, '');

it('uses no light-dark() in the stylesheets', () => {
  for (const file of ['src/styles.css', 'src/theme.css', 'src/netplane/styles.css']) expect(read(file)).not.toContain('light-dark(');
});

it('gives each colour it took out of light-dark() its dark value under the dark scheme', () => {
  const dark = /:root\[data-mantine-color-scheme="dark"\] \{([^}]*)\}/.exec(read('src/styles.css'))?.[1] ?? '';
  for (const name of ['--andaru-ground', '--andaru-error-text', '--andaru-warning-text', '--andaru-diff-old', '--andaru-diff-new']) expect(dark).toContain(`${name}:`);
});

// Download and upload are told apart by more than strength: upload is dashed.
it('draws upload dashed', () => {
  expect(/path\.traffic-sent \{[^}]*stroke-dasharray/.test(read('src/styles.css'))).toBe(true);
});
