import { DEFAULT_THEME, createTheme, defaultCssVariablesResolver, mergeMantineTheme } from '@mantine/core';

// Andaru's ink blue: links, focus and primary buttons. White is 7.8:1 on shade 6 (light scheme) and 4.9:1 on shade 5
// (dark scheme). The amber mark stays an accent (styles.css), never text on a light surface.
const andaru = ['#e8f3f9', '#cfe4f0', '#a3cbe0', '#72afcf', '#4a97bf', '#2a78a3', '#17577d', '#134a6b', '#114463', '#0c3149'] as const;

export const theme = createTheme({
  colors: { andaru },
  primaryColor: 'andaru',
  // Dark fills use shade 5, not Mantine's 8: the focus ring and the loader are drawn in the primary fill and must
  // show on a dark surface (3.2:1), and autoContrast picks its text colour from the light shade, which shade 8 breaks
  // for gray (black on a dark gray).
  primaryShade: { light: 6, dark: 5 },
  // Text on a filled colour is black or white, whichever reads: white on Mantine's green or yellow is under 3:1.
  autoContrast: true,
  luminanceThreshold: 0.25,
  defaultRadius: 'sm',
});

// themeCss is the stylesheet MantineProvider would inject for this theme, as static text: andaru-web's
// Content-Security-Policy (style-src 'self') refuses injected <style> elements. Only what differs from Mantine's own
// stylesheet is emitted; theme.css holds the result, and theme.test.ts keeps the two equal.
export function themeCss(): string {
  const base = defaultCssVariablesResolver(DEFAULT_THEME);
  const mine = defaultCssVariablesResolver(mergeMantineTheme(DEFAULT_THEME, theme));
  const block = (selector: string, key: 'variables' | 'light' | 'dark') => {
    const before: Record<string, string> = base[key];
    const lines = Object.entries(mine[key]).filter(([name, value]) => before[name] !== value).map(([name, value]) => `  ${name}: ${value};`);
    return lines.length ? `${selector} {\n${lines.join('\n')}\n}\n` : '';
  };
  return '/* Generated from theme.ts by `npm run theme`; do not edit. */\n'
    + block(':root, :host', 'variables')
    + block(':root[data-mantine-color-scheme="light"], :host([data-mantine-color-scheme="light"])', 'light')
    + block(':root[data-mantine-color-scheme="dark"], :host([data-mantine-color-scheme="dark"])', 'dark');
}
