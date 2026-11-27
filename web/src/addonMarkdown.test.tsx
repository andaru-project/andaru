import { expect, it } from 'vitest';
import { AddonMarkdown } from './addonMarkdown';
import { render, screen } from './test/render';

it('renders paragraphs, lists, emphasis, code, and safe http(s) links', () => {
  const source = [
    'Use `TS_AUTHKEY` for **unattended** *setup*.',
    '',
    '- Read [Tailscale docs](https://tailscale.com/kb)',
    '- Check status',
  ].join('\n');

  const { container } = render(<AddonMarkdown id="desc-1" source={source} />);
  expect(container.querySelector('#desc-1')).toBeInTheDocument();
  expect(screen.getByText('TS_AUTHKEY').tagName).toBe('CODE');
  expect(screen.getByText('unattended').tagName).toBe('STRONG');
  expect(screen.getByText('setup').tagName).toBe('EM');

  const link = screen.getByRole('link', { name: 'Tailscale docs' });
  expect(link).toHaveAttribute('href', 'https://tailscale.com/kb');
  expect(link).toHaveAttribute('target', '_blank');
  expect(link).toHaveAttribute('rel', 'noopener noreferrer');
  expect(container.querySelectorAll('li')).toHaveLength(2);
});

it('escapes raw HTML and rejects non-http(s) links', () => {
  const source = [
    '<script>alert(1)</script> <img src=x onerror=alert(1)>',
    '',
    'Click [bad js](javascript:alert(1)) or [bad data](data:text/html,hi)',
  ].join('\n');

  const { container } = render(<AddonMarkdown source={source} />);
  expect(container.querySelector('script')).toBeNull();
  expect(container.querySelector('img')).toBeNull();
  expect(container.querySelector('a')).toBeNull();
  expect(screen.getByText(/<script>alert\(1\)<\/script>/)).toBeInTheDocument();
  expect(screen.getByText(/bad js/)).toBeInTheDocument();
  expect(screen.getByText(/bad data/)).toBeInTheDocument();
});
