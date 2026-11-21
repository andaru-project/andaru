import { readFileSync, writeFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { expect, it } from 'vitest';
import { themeCss } from './theme';

// Vitest runs from web/.
const file = resolve(process.cwd(), 'src/theme.css');

it('keeps theme.css equal to the theme, since the CSP refuses the stylesheet Mantine would inject', () => {
  if (process.env.UPDATE_THEME_CSS) writeFileSync(file, themeCss());
  expect(readFileSync(file, 'utf8'), 'run `npm run theme` after changing theme.ts').toBe(themeCss());
});

it('defines the primary colour statically', () => {
  expect(themeCss()).toContain('--mantine-primary-color-filled: var(--mantine-color-andaru-filled);');
  expect(themeCss()).toContain('--mantine-color-andaru-7: #17577d;');
});
