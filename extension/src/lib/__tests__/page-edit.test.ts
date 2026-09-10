import { describe, expect, it } from 'vitest';
import { PAGE_EDIT_FENCE, extractPageEdit, unwrapEditorPayload } from '../page-edit';

describe('page-edit', () => {
  it('extracts the body of an astonish-page-edit fence', () => {
    const text = [
      'Here is a suggested rewrite:',
      '```' + PAGE_EDIT_FENCE,
      'Add cluster-east to the runbook.',
      '```',
      'Let me know if you want a shorter version.',
    ].join('\n');

    expect(extractPageEdit(text)).toBe('Add cluster-east to the runbook.');
  });

  it('returns the last fence when several are present', () => {
    const text = [
      '```' + PAGE_EDIT_FENCE,
      'first',
      '```',
      '```' + PAGE_EDIT_FENCE,
      'second',
      '```',
    ].join('\n');

    expect(extractPageEdit(text)).toBe('second');
  });

  it('returns null when no page-edit fence is present', () => {
    expect(extractPageEdit('```markdown\nhello\n```')).toBeNull();
    expect(extractPageEdit('no fences at all')).toBeNull();
  });

  it('unwraps a page-tool envelope so Apply writes markdown, not JSON', () => {
    const markdown = '### Requirement Overview\n\nSupport SCI Object Store Credentials.';
    const text = [
      '```' + PAGE_EDIT_FENCE,
      JSON.stringify({ ref: 'ref1', text: markdown }),
      '```',
    ].join('\n');
    expect(extractPageEdit(text)).toBe(markdown);
    expect(unwrapEditorPayload('{"ref":"ref1","text":"## Summary\\n\\nBody"}')).toBe('## Summary\n\nBody');
    expect(unwrapEditorPayload({ name: 'page_fill', args: { ref: 'ref4', text: markdown } })).toBe(markdown);
    expect(unwrapEditorPayload(markdown)).toBe(markdown);
    expect(
      unwrapEditorPayload(JSON.stringify({ ref: 'ref1', text: JSON.stringify({ ref: 'ref1', text: markdown }) })),
    ).toBe(markdown);
  });
});
