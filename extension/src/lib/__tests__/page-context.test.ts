import { describe, expect, it } from 'vitest';
import {
  buildSystemContext,
  buildToolRoundContext,
  capMainText,
  formatPageMap,
  retainPageDocument,
  type PageContext,
} from '../page-context';

const fixture: PageContext = {
  url: 'https://github.com/acme/app/issues/12',
  title: 'Crash on boot',
  hostname: 'github.com',
  selection: 'cluster names',
  mainText: 'The service fails when the cluster is unnamed.',
  editorPresent: true,
  editorKind: 'markdown',
  adapter: 'github',
};

describe('page-context', () => {
  it('buildSystemContext includes URL, title, site, adapter, editor, selection, and main content', () => {
    const markdown = buildSystemContext(fixture);
    expect(markdown).toContain('## Browser page context');
    expect(markdown).toContain('URL: https://github.com/acme/app/issues/12');
    expect(markdown).toContain('Title: Crash on boot');
    expect(markdown).toContain('Site: github.com');
    expect(markdown).toContain('Adapter: github');
    expect(markdown).toContain('Editor present: yes');
    expect(markdown).toContain('Editor kind: markdown');
    expect(markdown).toContain('### Selection');
    expect(markdown).toContain('cluster names');
    expect(markdown).toContain('### Main content (markdown)');
    expect(markdown).toContain('write Markdown');
    expect(markdown).toContain('The service fails when the cluster is unnamed.');
    expect(markdown).toContain('## Chrome extension page tools');
    expect(markdown).toContain('Do NOT call');
    expect(markdown).toContain('page_snapshot');
    expect(markdown).toContain('page_navigate');
    expect(markdown).toContain('browser_snapshot');
    expect(markdown).toContain('web_fetch');
    expect(markdown).toContain('Never write a page-tool envelope');
    expect(markdown).toContain('astonish-page-edit');
    expect(markdown).toMatch(/enter edit mode/i);
    expect(markdown).toMatch(/cannot be edited/i);
    expect(markdown).not.toContain('Never auto-click GitHub Edit');
  });

  it('omits the selection section when selection is empty', () => {
    const markdown = buildSystemContext({ ...fixture, selection: '  ' });
    expect(markdown).not.toContain('### Selection');
  });

  it('caps main text at 50_000 characters', () => {
    const long = 'x'.repeat(50_100);
    expect(capMainText(long)).toHaveLength(50_000);
  });

  it('includes the GitHub page map in system context', () => {
    const markdown = buildSystemContext({
      ...fixture,
      pageMap: {
        kind: 'github-issue',
        regions: [
          { name: 'Issue description', status: 'editor open', notes: 'Apply writes here' },
          { name: 'New comment box', status: 'always visible', notes: 'Do not Apply here' },
        ],
        actions: ['You are already on this issue. Do not page_navigate.'],
      },
    });
    expect(markdown).toContain('## Page map');
    expect(markdown).toContain('Kind: github-issue');
    expect(markdown).toContain('Issue description: editor open');
    expect(markdown).toContain('New comment box: always visible');
    expect(markdown).toContain('Do not page_navigate');
  });

  it('retainPageDocument keeps the longer original issue body', () => {
    const previous = { ...fixture, mainText: '### Overview\n\nKeep this whole description.' };
    const next = { ...fixture, mainText: 'short snapshot', editorPresent: false };
    expect(retainPageDocument(previous, next).mainText).toBe(previous.mainText);
    expect(retainPageDocument(undefined, next).mainText).toBe('short snapshot');
    const longer = { ...fixture, mainText: previous.mainText + '\n\n### Extra' };
    expect(retainPageDocument(previous, longer).mainText).toBe(longer.mainText);
  });

  it('buildToolRoundContext keeps the page document above tool results', () => {
    const combined = buildToolRoundContext(
      buildSystemContext(fixture),
      '## Chrome extension page-tool results\n\n### page_snapshot (ok)\nURL: https://github.com/acme/app/issues/12',
    );
    expect(combined.indexOf('### Main content (markdown)')).toBeLessThan(
      combined.indexOf('## Chrome extension page-tool results'),
    );
    expect(combined).toContain('The service fails when the cluster is unnamed.');
  });

  it('formatPageMap is empty when no map is present', () => {
    expect(formatPageMap()).toBe('');
  });
});
