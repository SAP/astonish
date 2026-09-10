/**
 * Minimal, dependency-free Markdown → HTML renderer for the side-panel
 * transcript. Assistant text is untrusted, so everything is HTML-escaped first
 * and only a small, safe subset of Markdown is re-introduced. The output is
 * assigned to innerHTML by the caller, so this module must never emit raw
 * user HTML, event handlers, or javascript: / data: URLs.
 */

function escapeHtml(text: string): string {
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

function safeHref(raw: string): string | null {
  const url = raw.trim();
  if (/^(https?:|mailto:)/i.test(url)) {
    return url;
  }
  if (url.startsWith('/') || url.startsWith('#')) {
    return url;
  }
  return null;
}

/** Inline spans: code, bold, italic, links. Operates on already-escaped text. */
function renderInline(escaped: string): string {
  let out = escaped;
  // Inline code first so its contents are not further transformed.
  out = out.replace(/`([^`]+)`/g, (_m, code: string) => `<code>${code}</code>`);
  // Links: [label](href) — label already escaped; validate the href.
  out = out.replace(/\[([^\]]+)\]\(([^)\s]+)\)/g, (match, label: string, href: string) => {
    const cleaned = href.replace(/&amp;/g, '&');
    const safe = safeHref(cleaned);
    if (!safe) {
      return match;
    }
    return `<a href="${escapeHtml(safe)}" target="_blank" rel="noopener noreferrer">${label}</a>`;
  });
  // Bold then italic.
  out = out.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
  out = out.replace(/(^|[^*])\*([^*]+)\*/g, '$1<em>$2</em>');
  out = out.replace(/(^|[^_])_([^_]+)_/g, '$1<em>$2</em>');
  return out;
}

/** Render a small, safe subset of Markdown to HTML. */
export function renderMarkdown(markdown: string): string {
  const source = (markdown ?? '').replace(/\r\n/g, '\n');
  const lines = source.split('\n');
  const html: string[] = [];

  let inCode = false;
  let codeBuffer: string[] = [];
  let listType: 'ul' | 'ol' | null = null;
  let paragraph: string[] = [];
  let inTable = false;
  let tableHasHeader = false;

  const flushParagraph = (): void => {
    if (paragraph.length) {
      html.push(`<p>${renderInline(escapeHtml(paragraph.join(' ')))}</p>`);
      paragraph = [];
    }
  };
  const closeList = (): void => {
    if (listType) {
      html.push(`</${listType}>`);
      listType = null;
    }
  };
  const closeTable = (): void => {
    if (inTable) {
      html.push('</tbody></table>');
      inTable = false;
      tableHasHeader = false;
    }
  };

  const isTableRow = (line: string): boolean => {
    const trimmed = line.trim();
    return trimmed.startsWith('|') && trimmed.endsWith('|') && trimmed.length > 1;
  };

  const isSeparatorRow = (line: string): boolean =>
    /^\|[\s:]*-{2,}[\s:]*(\|[\s:]*-{2,}[\s:]*)*\|$/.test(line.trim());

  const parseTableCells = (line: string): string[] => {
    const trimmed = line.trim();
    // Strip leading and trailing pipes, then split on pipes.
    return trimmed
      .slice(1, -1)
      .split('|')
      .map((c) => c.trim());
  };

  for (const line of lines) {
    const fence = line.match(/^```/);
    if (fence) {
      if (inCode) {
        html.push(`<pre><code>${escapeHtml(codeBuffer.join('\n'))}</code></pre>`);
        codeBuffer = [];
        inCode = false;
      } else {
        flushParagraph();
        closeList();
        closeTable();
        inCode = true;
      }
      continue;
    }
    if (inCode) {
      codeBuffer.push(line);
      continue;
    }

    // --- Table handling ---
    if (isTableRow(line)) {
      if (isSeparatorRow(line)) {
        // Separator row after header — mark that the previous row was a header.
        if (inTable && !tableHasHeader) {
          tableHasHeader = true;
        }
        continue;
      }
      if (!inTable) {
        flushParagraph();
        closeList();
        inTable = true;
        tableHasHeader = false;
        const cells = parseTableCells(line);
        // Emit the header row; we'll convert to <thead> if a separator follows.
        html.push('<table><thead><tr>');
        for (const cell of cells) {
          html.push(`<th>${renderInline(escapeHtml(cell))}</th>`);
        }
        html.push('</tr></thead><tbody>');
        continue;
      }
      // Subsequent data rows.
      const cells = parseTableCells(line);
      html.push('<tr>');
      for (const cell of cells) {
        html.push(`<td>${renderInline(escapeHtml(cell))}</td>`);
      }
      html.push('</tr>');
      continue;
    }
    // Non-table line while in a table — close the table.
    closeTable();

    if (line.trim() === '') {
      flushParagraph();
      closeList();
      continue;
    }

    const heading = line.match(/^(#{1,6})\s+(.*)$/);
    if (heading) {
      flushParagraph();
      closeList();
      const level = heading[1].length;
      html.push(`<h${level}>${renderInline(escapeHtml(heading[2].trim()))}</h${level}>`);
      continue;
    }

    const ordered = line.match(/^\s*\d+\.\s+(.*)$/);
    const unordered = line.match(/^\s*[-*+]\s+(.*)$/);
    if (ordered || unordered) {
      flushParagraph();
      const want: 'ul' | 'ol' = ordered ? 'ol' : 'ul';
      if (listType !== want) {
        closeList();
        listType = want;
        html.push(`<${want}>`);
      }
      const item = (ordered ? ordered[1] : (unordered as RegExpMatchArray)[1]).trim();
      html.push(`<li>${renderInline(escapeHtml(item))}</li>`);
      continue;
    }

    closeList();
    paragraph.push(line.trim());
  }

  if (inCode) {
    html.push(`<pre><code>${escapeHtml(codeBuffer.join('\n'))}</code></pre>`);
  }
  flushParagraph();
  closeList();
  closeTable();

  return html.join('\n');
}
