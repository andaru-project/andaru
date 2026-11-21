import { expect, it } from 'vitest';
import { themeCss } from './theme';

// `npm run theme` rewrites theme.css after a change to theme.ts.
it('keeps theme.css equal to the theme, since the CSP refuses the stylesheet Mantine would inject', async () => {
  await expect(themeCss()).toMatchFileSnapshot('./theme.css');
});

it('defines the primary colour statically', () => {
  expect(themeCss()).toContain('--mantine-primary-color-filled: var(--mantine-color-andaru-filled);');
});
