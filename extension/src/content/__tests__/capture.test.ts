import { afterEach, describe, expect, it } from 'vitest';
import { capturePage } from '../capture';
import { captureGitHub, htmlToMarkdown, isGitHubIssueOrPull } from '../adapters/github';

function setLocation(href: string): void {
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: new URL(href),
  });
}

describe('capture', () => {
  afterEach(() => {
    document.body.innerHTML = '';
    document.title = '';
  });

  it('generic capture uses article innerText, selection, and editorPresent', () => {
    setLocation('https://docs.example.com/guide');
    document.title = 'Service guide';
    document.body.innerHTML = `
      <article>Deploy the payments service to staging.</article>
      <textarea></textarea>
    `;
    const range = document.createRange();
    range.selectNodeContents(document.querySelector('article') as HTMLElement);
    const sel = window.getSelection();
    sel?.removeAllRanges();
    sel?.addRange(range);

    const ctx = capturePage();
    expect(ctx.adapter).toBe('generic');
    expect(ctx.hostname).toBe('docs.example.com');
    expect(ctx.title).toBe('Service guide');
    expect(ctx.mainText).toContain('payments service');
    expect(ctx.editorPresent).toBe(true);
    expect(ctx.editorKind).toBe('plain');
  });

  it('GitHub issue fixture returns adapter github plus title and markdown body, truncated to 50k', () => {
    const href = 'https://github.com/acme/app/issues/42';
    setLocation(href);
    document.body.innerHTML = `
      <h1 class="gh-header-title"><span class="js-issue-title">Crash on boot</span></h1>
      <article class="markdown-body">${'cluster-east '.repeat(6000)}</article>
    `;

    expect(isGitHubIssueOrPull(new URL(href))).toBe(true);
    const ctx = captureGitHub(new URL(href));
    expect(ctx).not.toBeNull();
    expect(ctx?.adapter).toBe('github');
    expect(ctx?.editorKind).toBe('markdown');
    expect(ctx?.title).toBe('Crash on boot');
    expect(ctx?.mainText.length).toBeLessThanOrEqual(50_000);
    expect(ctx?.mainText).toContain('cluster-east');
  });

  it('GitHub capture prefers a visible issue-body textarea over rendered markdown', () => {
    setLocation('https://github.com/acme/app/issues/7');
    document.body.innerHTML = `
      <span class="js-issue-title">Rename cluster</span>
      <article class="markdown-body">rendered body</article>
      <textarea name="issue[body]">edited body with cluster-west</textarea>
    `;
    const ctx = captureGitHub(new URL('https://github.com/acme/app/issues/7'));
    expect(ctx?.mainText).toBe('edited body with cluster-west');
    expect(ctx?.editorKind).toBe('markdown');
  });

  it('GitHub capture builds a page map that separates description from the comment box', () => {
    setLocation('https://github.com/acme/app/issues/7');
    document.body.innerHTML = `
      <span class="js-issue-title">Rename cluster</span>
      <article class="markdown-body">rendered body</article>
      <textarea name="issue[body]">edited body</textarea>
      <form id="new_comment_form">
        <textarea name="comment[body]" id="new_comment_field"></textarea>
      </form>
    `;
    const ctx = captureGitHub(new URL('https://github.com/acme/app/issues/7'));
    expect(ctx?.pageMap?.kind).toBe('github-issue');
    expect(ctx?.pageMap?.regions).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ name: 'Issue description', status: 'editor open' }),
        expect.objectContaining({ name: 'New comment box', status: 'always visible' }),
      ]),
    );
    expect(ctx?.pageMap?.actions.some((action) => /do not page_navigate/i.test(action))).toBe(true);
    expect(ctx?.editorPresent).toBe(true);
  });

  it('GitHub page map marks a closed description editor as read-only', () => {
    setLocation('https://github.com/acme/app/issues/7');
    document.body.innerHTML = `
      <article class="markdown-body">rendered body</article>
      <form id="new_comment_form">
        <textarea name="comment[body]" id="new_comment_field"></textarea>
      </form>
    `;
    const ctx = captureGitHub(new URL('https://github.com/acme/app/issues/7'));
    expect(ctx?.editorPresent).toBe(false);
    expect(ctx?.pageMap?.regions).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ name: 'Issue description', status: 'read-only' }),
        expect.objectContaining({ name: 'New comment box', status: 'always visible' }),
      ]),
    );
    // The read-only guidance must forcefully tell the model to STOP and emit
    // one page-edit fence, not hunt for the kebab/Edit affordance itself.
    expect(ctx?.pageMap?.actions.some((action) => /\bstop\b/i.test(action))).toBe(true);
    expect(
      ctx?.pageMap?.actions.some((action) => /one astonish-page-edit fence/i.test(action)),
    ).toBe(true);
    expect(ctx?.pageMap?.actions.some((action) => /the user'?s job/i.test(action))).toBe(true);
    // It must explicitly warn against page_click / page_query hunting for the kebab.
    expect(ctx?.pageMap?.actions.some((action) => /page_click/i.test(action))).toBe(true);
    expect(ctx?.pageMap?.actions.some((action) => /kebab/i.test(action))).toBe(true);
    expect(ctx?.pageMap?.actions.some((action) => /auto-enters/i.test(action))).toBe(false);
    expect(
      ctx?.pageMap?.regions.some(
        (region) =>
          region.name === 'Issue description' &&
          /the user opens edit/i.test(region.notes ?? '') &&
          /then presses apply/i.test(region.notes ?? ''),
      ),
    ).toBe(true);
  });

  it('GitHub capture converts rendered markdown-body HTML into markdown source', () => {
    setLocation('https://github.com/acme/app/issues/9');
    document.body.innerHTML = `
      <span class="js-issue-title">SCI credentials</span>
      <article class="markdown-body">
        <h3>Requirement Overview</h3>
        <p>We have the existing UI handler for the GCP Object Store Credentials.</p>
        <h3>Acceptance Criteria</h3>
        <ol>
          <li>Requirements Fulfillment</li>
        </ol>
      </article>
    `;
    const ctx = captureGitHub(new URL('https://github.com/acme/app/issues/9'));
    expect(ctx?.mainText).toContain('### Requirement Overview');
    expect(ctx?.mainText).toContain('GCP Object Store Credentials');
    expect(ctx?.mainText).toContain('- Requirements Fulfillment');
    expect(ctx?.mainText).not.toMatch(/<h3>/);
  });

  it('htmlToMarkdown keeps headings and lists', () => {
    document.body.innerHTML = `
      <article>
        <h2>Detailed Requirements</h2>
        <p>Enhance the UI.</p>
        <ul><li>Add fields</li><li>Mask secrets</li></ul>
      </article>
    `;
    const md = htmlToMarkdown(document.querySelector('article') as HTMLElement);
    expect(md).toContain('## Detailed Requirements');
    expect(md).toContain('Enhance the UI.');
    expect(md).toContain('- Add fields');
    expect(md).toContain('- Mask secrets');
  });
});
