import { Anchor, Code } from '@mantine/core';
import type { ReactNode } from 'react';
import { addonText, safeExternalHttpUrl } from './addons';
import type { AddonText } from './types';

const bulletPrefix = /^[-*]\s+/;

function renderInline(line: string, keyPrefix: string): ReactNode[] {
  const out: ReactNode[] = [];
  let cursor = 0;
  let token = 0;

  while (cursor < line.length) {
    const ch = line[cursor];

    if (ch === '`') {
      const end = line.indexOf('`', cursor + 1);
      if (end > cursor + 1) {
        out.push(<Code key={`${keyPrefix}-c-${token++}`}>{line.slice(cursor + 1, end)}</Code>);
        cursor = end + 1;
        continue;
      }
    }

    if (ch === '[') {
      const closeBracket = line.indexOf(']', cursor + 1);
      if (closeBracket > cursor + 1 && line[closeBracket + 1] === '(') {
        const closeParen = line.indexOf(')', closeBracket + 2);
        if (closeParen > closeBracket + 2) {
          const label = line.slice(cursor + 1, closeBracket);
          const href = safeExternalHttpUrl(line.slice(closeBracket + 2, closeParen));
          if (href) {
            out.push(
              <Anchor
                key={`${keyPrefix}-a-${token++}`}
                href={href}
                target="_blank"
                rel="noopener noreferrer"
                size="inherit"
              >
                {label}
              </Anchor>,
            );
          } else if (label) {
            out.push(label);
          }
          cursor = closeParen + 1;
          continue;
        }
      }
    }

    if (ch === '*' && line[cursor + 1] === '*') {
      const end = line.indexOf('**', cursor + 2);
      if (end > cursor + 2) {
        out.push(<strong key={`${keyPrefix}-b-${token++}`}>{line.slice(cursor + 2, end)}</strong>);
        cursor = end + 2;
        continue;
      }
    }

    if (ch === '*' && line[cursor + 1] !== '*') {
      const end = line.indexOf('*', cursor + 1);
      if (end > cursor + 1) {
        out.push(<em key={`${keyPrefix}-i-${token++}`}>{line.slice(cursor + 1, end)}</em>);
        cursor = end + 1;
        continue;
      }
    }

    let next = cursor + 1;
    while (next < line.length && line[next] !== '`' && line[next] !== '[' && line[next] !== '*') {
      next++;
    }
    out.push(line.slice(cursor, next));
    cursor = next;
  }

  return out;
}

interface AddonMarkdownProps {
  source?: AddonText;
  id?: string;
  className?: string;
}

// Renders manifest prose safely as React nodes without raw HTML evaluation.
// Only paragraphs, bullet lists, inline code, emphasis, and http(s) links are supported.
export function AddonMarkdown({ source, id, className }: AddonMarkdownProps) {
  const raw = addonText(source).trim();
  if (!raw) return null;

  const lines = raw.replace(/\r\n/g, '\n').split('\n');
  const blocks: Array<{ kind: 'p'; lines: string[] } | { kind: 'ul'; items: string[] }> = [];
  let i = 0;

  while (i < lines.length) {
    const trimmed = lines[i].trim();
    if (!trimmed) {
      i++;
      continue;
    }
    if (bulletPrefix.test(trimmed)) {
      const items: string[] = [];
      while (i < lines.length && bulletPrefix.test(lines[i].trim())) {
        items.push(lines[i].trim().replace(bulletPrefix, ''));
        i++;
      }
      blocks.push({ kind: 'ul', items });
      continue;
    }
    const paragraph: string[] = [];
    while (i < lines.length && lines[i].trim() !== '' && !bulletPrefix.test(lines[i].trim())) {
      paragraph.push(lines[i].trim());
      i++;
    }
    blocks.push({ kind: 'p', lines: paragraph });
  }

  return (
    <div id={id} className={className ? `addon-markdown ${className}` : 'addon-markdown'}>
      {blocks.map((block, bi) => {
        if (block.kind === 'ul') {
          return (
            <ul key={`b-${bi}`} className="addon-markdown-list">
              {block.items.map((item, ii) => (
                <li key={`b-${bi}-i-${ii}`}>{renderInline(item, `b-${bi}-i-${ii}`)}</li>
              ))}
            </ul>
          );
        }
        return (
          <p key={`b-${bi}`} className="addon-markdown-paragraph">
            {renderInline(block.lines.join(' '), `b-${bi}`)}
          </p>
        );
      })}
    </div>
  );
}
