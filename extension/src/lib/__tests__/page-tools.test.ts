import { describe, expect, it } from 'vitest';
import {
  PAGE_TOOL_FENCE,
  extractPageTools,
  formatPageToolResults,
  stripPageToolFences,
} from '../page-tools';

describe('page-tools', () => {
  it('extracts a single astonish-page-tool fence', () => {
    const text = [
      'I will snapshot the tab.',
      '```' + PAGE_TOOL_FENCE,
      '{"name":"page_snapshot"}',
      '```',
    ].join('\n');
    expect(extractPageTools(text)).toEqual([{ name: 'page_snapshot', args: {} }]);
  });

  it('extracts several fences and arguments', () => {
    const text = [
      '```' + PAGE_TOOL_FENCE,
      '{"name":"page_click","args":{"ref":"ref3"}}',
      '```',
      '```' + PAGE_TOOL_FENCE,
      '{"name":"page_fill","arguments":{"ref":"ref4","text":"hi"}}',
      '```',
    ].join('\n');
    expect(extractPageTools(text)).toEqual([
      { name: 'page_click', args: { ref: 'ref3' } },
      { name: 'page_fill', args: { ref: 'ref4', text: 'hi' } },
    ]);
  });

  it('merges top-level selector/text/url/ref into args', () => {
    const text = [
      '```' + PAGE_TOOL_FENCE,
      '{"name":"page_query","text":"New America","selector":"a"}',
      '```',
      '```' + PAGE_TOOL_FENCE,
      '{"name":"page_click","ref":"ref1"}',
      '```',
      '```' + PAGE_TOOL_FENCE,
      '{"name":"page_navigate","url":"/topics/new-america"}',
      '```',
    ].join('\n');
    expect(extractPageTools(text)).toEqual([
      { name: 'page_query', args: { text: 'New America', selector: 'a' } },
      { name: 'page_click', args: { ref: 'ref1' } },
      { name: 'page_navigate', args: { url: '/topics/new-america' } },
    ]);
  });

  it('nested args win over duplicate top-level keys', () => {
    const text = [
      '```' + PAGE_TOOL_FENCE,
      '{"name":"page_click","ref":"wrong","args":{"ref":"ref3"}}',
      '```',
    ].join('\n');
    expect(extractPageTools(text)).toEqual([{ name: 'page_click', args: { ref: 'ref3' } }]);
  });

  it('ignores astonish-page-edit fences and invalid JSON', () => {
    const text = [
      '```astonish-page-edit',
      'rewrite',
      '```',
      '```' + PAGE_TOOL_FENCE,
      'not-json',
      '```',
    ].join('\n');
    expect(extractPageTools(text)).toEqual([]);
  });

  it('strips tool fences from displayed markdown', () => {
    const text = ['Hello', '```' + PAGE_TOOL_FENCE, '{"name":"page_snapshot"}', '```', 'World'].join(
      '\n',
    );
    expect(stripPageToolFences(text)).toBe('Hello\n\nWorld');
  });

  it('formats tool results for the next turn', () => {
    const markdown = formatPageToolResults([
      { name: 'page_snapshot', ok: true, result: 'URL: https://cnn.com\nTitle: CNN' },
      { name: 'page_click', ok: false, error: 'Unknown ref ref9' },
    ]);
    expect(markdown).toContain('## Chrome extension page-tool results');
    expect(markdown).toContain('page_snapshot (ok)');
    expect(markdown).toContain('URL: https://cnn.com');
    expect(markdown).toContain('page_click (error)');
    expect(markdown).toContain('Unknown ref ref9');
    expect(markdown).toContain('THIS browser tab');
    expect(markdown).toContain('Do not call web_fetch');
    expect(markdown).toContain('page_navigate');
    expect(markdown).toContain('original page document remains in ### Main content');
    expect(markdown).toContain('Do not page_navigate to the current URL');
  });
});
