import { describe, expect, it } from 'vitest';
import { renderMarkdown } from '../markdown';

describe('renderMarkdown', () => {
  it('escapes raw HTML so assistant text cannot inject markup', () => {
    const html = renderMarkdown('<img src=x onerror=alert(1)>');
    expect(html).not.toContain('<img');
    expect(html).toContain('&lt;img');
  });

  it('renders headings, bold, italic, and inline code', () => {
    expect(renderMarkdown('## Title')).toBe('<h2>Title</h2>');
    expect(renderMarkdown('**bold**')).toContain('<strong>bold</strong>');
    expect(renderMarkdown('use `page_fill` here')).toContain('<code>page_fill</code>');
  });

  it('renders unordered and ordered lists', () => {
    expect(renderMarkdown('- one\n- two')).toBe('<ul>\n<li>one</li>\n<li>two</li>\n</ul>');
    expect(renderMarkdown('1. first\n2. second')).toBe(
      '<ol>\n<li>first</li>\n<li>second</li>\n</ol>',
    );
  });

  it('renders fenced code blocks verbatim and escaped', () => {
    const html = renderMarkdown('```\n<b>x</b>\n```');
    expect(html).toBe('<pre><code>&lt;b&gt;x&lt;/b&gt;</code></pre>');
  });

  it('allows safe links but leaves javascript: URLs untransformed', () => {
    expect(renderMarkdown('[docs](https://example.com)')).toContain(
      '<a href="https://example.com" target="_blank" rel="noopener noreferrer">docs</a>',
    );
    const unsafe = renderMarkdown('[x](javascript:alert(1))');
    expect(unsafe).not.toContain('<a ');
  });

  it('renders markdown tables with header and data rows', () => {
    const md = '| Name | Status |\n| --- | --- |\n| vm-1 | ACTIVE |\n| vm-2 | STOPPED |';
    const html = renderMarkdown(md);
    expect(html).toContain('<table>');
    expect(html).toContain('<thead>');
    expect(html).toContain('<th>Name</th>');
    expect(html).toContain('<th>Status</th>');
    expect(html).toContain('<tbody>');
    expect(html).toContain('<td>vm-1</td>');
    expect(html).toContain('<td>ACTIVE</td>');
    expect(html).toContain('<td>vm-2</td>');
    expect(html).toContain('<td>STOPPED</td>');
    expect(html).toContain('</table>');
  });

  it('closes a table when a non-table line follows', () => {
    const md = '| A | B |\n| - | - |\n| 1 | 2 |\n\nSome paragraph.';
    const html = renderMarkdown(md);
    expect(html).toContain('</table>');
    expect(html).toContain('<p>Some paragraph.</p>');
  });
});
