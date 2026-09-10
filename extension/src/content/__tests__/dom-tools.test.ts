import { afterEach, describe, expect, it } from 'vitest';
import { accessibleDocumentCount, runPageTool, snapshotRefCount } from '../dom-tools';

describe('dom-tools', () => {
  afterEach(() => {
    document.body.innerHTML = '';
    document.title = '';
  });

  it('page_snapshot assigns refs to visible interactive elements', () => {
    document.title = 'CNN';
    document.body.innerHTML = `
      <h1>Maine polls</h1>
      <a href="https://cnn.com/us">US</a>
      <button type="button">Subscribe</button>
      <input placeholder="Search" />
      <button type="button" hidden>Hidden</button>
    `;
    const out = runPageTool('page_snapshot');
    expect(out.ok).toBe(true);
    expect(out.result).toContain('Title: CNN');
    expect(out.result).toContain('[ref1]');
    expect(out.result).toContain('Subscribe');
    expect(out.result).toContain('Headings:');
    expect(out.result).toContain('Maine polls');
    expect(out.result).not.toContain('Hidden');
    expect(snapshotRefCount()).toBeGreaterThanOrEqual(3);
  });

  it('page_click uses a snapshot ref', () => {
    document.body.innerHTML = '<button id="go" type="button">Go</button>';
    let clicks = 0;
    document.querySelector('#go')?.addEventListener('click', () => {
      clicks += 1;
    });
    runPageTool('page_snapshot');
    const clicked = runPageTool('page_click', { ref: 'ref1' });
    expect(clicked.ok).toBe(true);
    expect(clicks).toBeGreaterThanOrEqual(1);
  });

  it('page_click on a link reports href and forces _self', () => {
    document.body.innerHTML = '<a id="us" href="https://cnn.com/us" target="_blank">US</a>';
    runPageTool('page_snapshot');
    const clicked = runPageTool('page_click', { ref: 'ref1' });
    expect(clicked.ok).toBe(true);
    expect(clicked.href).toContain('cnn.com/us');
    expect(clicked.result).toContain('href=');
    expect(document.querySelector('#us')?.getAttribute('target')).toBe('_self');
  });

  it('page_navigate is not executed in the content script', () => {
    const out = runPageTool('page_navigate', { url: 'https://cnn.com/us' });
    expect(out.ok).toBe(false);
    expect(out.error).toContain('service worker');
  });

  it('page_fill types into an input by ref', () => {
    document.body.innerHTML = '<input id="q" />';
    const input = document.querySelector<HTMLInputElement>('#q');
    const events: string[] = [];
    input?.addEventListener('input', (event) => {
      events.push(event.type);
      expect(event.bubbles).toBe(true);
    });
    runPageTool('page_snapshot');
    const filled = runPageTool('page_fill', { ref: 'ref1', text: 'Maine polls' });
    expect(filled.ok).toBe(true);
    expect(input?.value).toBe('Maine polls');
    expect(events).toEqual(['input']);
  });

  it('page_fill unwraps a JSON envelope so a markdown textarea gets markdown', () => {
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: new URL('https://github.com/acme/app/issues/1'),
    });
    document.body.innerHTML = '<textarea name="issue[body]">old</textarea>';
    const textarea = document.querySelector<HTMLTextAreaElement>('textarea[name="issue[body]"]');
    runPageTool('page_snapshot');
    expect(runPageTool('page_snapshot').result).toContain('markdown');
    const markdown = '### Requirement Overview\n\nSupport SCI credentials.';
    const filled = runPageTool('page_fill', {
      ref: 'ref1',
      text: JSON.stringify({ ref: 'ref1', text: markdown }),
    });
    expect(filled.ok).toBe(true);
    expect(textarea?.value).toBe(markdown);
    expect(textarea?.value).not.toContain('"ref"');
  });

  it('page_query finds by visible text and assigns clickable refs', () => {
    document.body.innerHTML = `
      <a href="/us">US</a>
      <a href="/world">World</a>
    `;
    const out = runPageTool('page_query', { text: 'world' });
    expect(out.ok).toBe(true);
    expect(out.result).toContain('[ref1]');
    expect(out.result).toContain('World');
    expect(out.result).not.toContain('"US"');
    expect(snapshotRefCount()).toBe(1);
    const clicked = runPageTool('page_click', { ref: 'ref1' });
    expect(clicked.ok).toBe(true);
    expect(clicked.href).toContain('/world');
  });

  it('page_query honors selector and href text', () => {
    document.body.innerHTML = `
      <a href="https://cnn.com/topics/new-america">New America</a>
      <a href="/us">US</a>
      <button type="button">Subscribe</button>
    `;
    const byHref = runPageTool('page_query', { text: 'new-america' });
    expect(byHref.result).toContain('New America');
    expect(byHref.result).toContain('[ref1]');
    expect(byHref.result).not.toContain('Subscribe');
    const bySelector = runPageTool('page_query', { selector: 'a[href*="topics"]' });
    expect(bySelector.result).toContain('New America');
    expect(bySelector.result).not.toContain('"US"');
  });

  it('page_snapshot can filter by text so later links still get refs', () => {
    document.body.innerHTML = Array.from({ length: 12 }, (_, i) =>
      `<a href="/nav-${i}">Nav ${i}</a>`,
    ).join('') + '<a href="/topics/new-america">New America</a>';
    const filtered = runPageTool('page_snapshot', { text: 'new america' });
    expect(filtered.ok).toBe(true);
    expect(filtered.result).toContain('[ref1]');
    expect(filtered.result).toContain('New America');
    expect(filtered.result).not.toContain('Nav 0');
  });

  it('unknown click refs surface in error', () => {
    const clicked = runPageTool('page_click', { ref: 'ref9' });
    expect(clicked.ok).toBe(false);
    expect(clicked.error).toContain('Unknown ref ref9');
    expect(clicked.result).toBeUndefined();
  });

  it('unknown tool names return an error', () => {
    const out = runPageTool('browser_snapshot');
    expect(out.ok).toBe(false);
    expect(out.error).toContain('Unknown page tool');
  });

  it('refuses to click GitHub Edit when the description editor is closed', () => {
    Object.defineProperty(window, 'location', {
      value: new URL('https://github.com/acme/app/issues/1'),
      configurable: true,
    });
    document.body.innerHTML = '<button type="button">Edit</button>';
    let clicks = 0;
    document.querySelector('button')?.addEventListener('click', () => {
      clicks += 1;
    });
    runPageTool('page_query', { text: 'edit' });
    const clicked = runPageTool('page_click', { ref: 'ref1' });
    // No open issue[body] editor: opening edit mode is the user's job, so the
    // model must not click Edit — it should emit an astonish-page-edit fence.
    expect(clicked.ok).toBe(false);
    expect(clicked.error).toMatch(/edit mode|astonish-page-edit/i);
    expect(clicks).toBe(0);
  });

  it('still refuses GitHub Comment submit clicks', () => {
    Object.defineProperty(window, 'location', {
      value: new URL('https://github.com/acme/app/issues/1'),
      configurable: true,
    });
    document.body.innerHTML = '<button type="button">Comment</button>';
    runPageTool('page_snapshot');
    const clicked = runPageTool('page_click', { ref: 'ref1' });
    expect(clicked.ok).toBe(false);
    expect(clicked.error).toMatch(/comment/i);
  });

  it('refuses to click Edit or the kebab menu when the description editor is closed', () => {
    Object.defineProperty(window, 'location', {
      value: new URL('https://github.com/acme/app/issues/1'),
      configurable: true,
    });
    // Read-only description: an Edit button and the "Issue body actions" kebab,
    // but no open issue[body] editor. The model must not drive edit mode.
    document.body.innerHTML = `
      <article class="markdown-body">old description</article>
      <button type="button" aria-label="Edit">Edit</button>
      <summary aria-label="Issue body actions" role="button">…</summary>
    `;
    runPageTool('page_snapshot');
    const editClick = runPageTool('page_click', { ref: 'ref1' });
    expect(editClick.ok).toBe(false);
    expect(editClick.error).toMatch(/edit mode/i);
    expect(editClick.error).toMatch(/astonish-page-edit/i);
    const kebabClick = runPageTool('page_click', { ref: 'ref2' });
    expect(kebabClick.ok).toBe(false);
    expect(kebabClick.error).toMatch(/kebab|issue body actions/i);
  });

  it('allows clicks inside an already-open description editor', () => {
    Object.defineProperty(window, 'location', {
      value: new URL('https://github.com/acme/app/issues/1'),
      configurable: true,
    });
    // Editor is open (issue[body] present): an Edit-labeled control is no longer blocked.
    document.body.innerHTML = `
      <textarea name="issue[body]" aria-label="Markdown value">desc</textarea>
      <button type="button" aria-label="Edit">Edit</button>
    `;
    // page_query filters to the Edit button and reassigns it as the first ref.
    const snap = runPageTool('page_query', { text: 'Edit' });
    expect(snap.ok).toBe(true);
    const clicked = runPageTool('page_click', { ref: 'ref1' });
    expect(clicked.ok).toBe(true);
  });

  it('labels GitHub description vs comment-box fields in the snapshot', () => {
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: new URL('https://github.com/acme/app/issues/1'),
    });
    document.body.innerHTML = `
      <textarea name="issue[body]" aria-label="Markdown value">desc</textarea>
      <form id="new_comment_form">
        <textarea name="comment[body]" id="new_comment_field" aria-label="Comment">c</textarea>
      </form>
    `;
    const out = runPageTool('page_snapshot');
    expect(out.result).toContain('issue-description');
    expect(out.result).toContain('comment-box');
    expect(out.result).toContain('## Page map');
    expect(out.result).toContain('Issue description');
  });

  it('refuses page_fill on the GitHub comment box', () => {
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: new URL('https://github.com/acme/app/issues/1'),
    });
    document.body.innerHTML = `
      <form id="new_comment_form">
        <textarea name="comment[body]" id="new_comment_field">draft comment</textarea>
      </form>
    `;
    const comment = document.querySelector<HTMLTextAreaElement>('textarea[name="comment[body]"]');
    runPageTool('page_snapshot');
    const filled = runPageTool('page_fill', { ref: 'ref1', text: 'should not land here' });
    expect(filled.ok).toBe(false);
    expect(filled.error).toContain('comment box');
    expect(comment?.value).toBe('draft comment');
  });

  it('refuses page_fill on a contenteditable GitHub comment composer', () => {
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: new URL('https://github.com/acme/app/issues/1'),
    });
    document.body.innerHTML = `
      <article class="markdown-body">issue body</article>
      <div role="textbox" contenteditable="true" aria-label="Add a comment">leave a comment</div>
    `;
    const composer = document.querySelector<HTMLElement>('[role="textbox"]');
    runPageTool('page_snapshot');
    const filled = runPageTool('page_fill', { ref: 'ref1', text: 'should not land here' });
    expect(filled.ok).toBe(false);
    expect(filled.error).toMatch(/comment/i);
    expect(filled.error).toMatch(/edit/i);
    expect(composer?.textContent).toBe('leave a comment');
  });

  describe('iframe support', () => {
    function createSameOriginIframe(html: string): HTMLIFrameElement {
      const iframe = document.createElement('iframe');
      document.body.appendChild(iframe);
      const doc = iframe.contentDocument!;
      doc.open();
      doc.write(`<html><body>${html}</body></html>`);
      doc.close();
      return iframe;
    }

    it('page_snapshot includes interactive elements from same-origin iframes', () => {
      document.body.innerHTML = '<button type="button">Top Button</button>';
      createSameOriginIframe('<button type="button">Iframe Button</button>');
      const out = runPageTool('page_snapshot');
      expect(out.ok).toBe(true);
      // If jsdom supports iframe.contentDocument, both buttons appear.
      // If not (contentDocument null), only Top Button appears — still ok.
      expect(out.result).toContain('Top Button');
      const iframeDoc = document.querySelector('iframe')?.contentDocument;
      if (iframeDoc) {
        expect(out.result).toContain('Iframe Button');
        expect(out.result).toContain('(iframe');
        expect(snapshotRefCount()).toBeGreaterThanOrEqual(2);
      }
    });

    it('page_click works on a ref inside an iframe', () => {
      let iframeClicks = 0;
      createSameOriginIframe('<button id="inner" type="button">Inner</button>');
      const iframeDoc = document.querySelector('iframe')?.contentDocument;
      if (!iframeDoc) return; // skip if jsdom doesn't support iframe.contentDocument
      const innerBtn = iframeDoc.querySelector('#inner')!;
      innerBtn.addEventListener('click', () => { iframeClicks += 1; });
      runPageTool('page_snapshot');
      const snap = runPageTool('page_query', { text: 'Inner' });
      expect(snap.ok).toBe(true);
      const clicked = runPageTool('page_click', { ref: 'ref1' });
      expect(clicked.ok).toBe(true);
      expect(iframeClicks).toBeGreaterThanOrEqual(1);
    });

    it('page_fill works on an input inside an iframe', () => {
      createSameOriginIframe('<input id="inner-input" placeholder="Search" />');
      const iframeDoc = document.querySelector('iframe')?.contentDocument;
      if (!iframeDoc) return; // skip if jsdom doesn't support iframe.contentDocument
      runPageTool('page_snapshot');
      const snap = runPageTool('page_query', { text: 'Search' });
      expect(snap.ok).toBe(true);
      const filled = runPageTool('page_fill', { ref: 'ref1', text: 'hello iframe' });
      expect(filled.ok).toBe(true);
      const input = iframeDoc.querySelector<HTMLInputElement>('#inner-input');
      expect(input?.value).toBe('hello iframe');
    });

    it('page_query with selector works across iframes', () => {
      document.body.innerHTML = '<a href="/top">Top Link</a>';
      createSameOriginIframe('<a href="/inner">Inner Link</a>');
      const iframeDoc = document.querySelector('iframe')?.contentDocument;
      const out = runPageTool('page_query', { selector: 'a[href]' });
      expect(out.ok).toBe(true);
      expect(out.result).toContain('Top Link');
      if (iframeDoc) {
        expect(out.result).toContain('Inner Link');
      }
    });

    it('snapshot headings include iframe headings', () => {
      document.body.innerHTML = '<h1>Top Heading</h1>';
      createSameOriginIframe('<h2>Iframe Heading</h2>');
      const iframeDoc = document.querySelector('iframe')?.contentDocument;
      const out = runPageTool('page_snapshot');
      expect(out.ok).toBe(true);
      expect(out.result).toContain('Top Heading');
      if (iframeDoc) {
        expect(out.result).toContain('Iframe Heading');
      }
    });

    it('accessibleDocumentCount reflects iframe presence', () => {
      // Without any iframes, only the top document.
      expect(accessibleDocumentCount()).toBe(1);
      createSameOriginIframe('<p>hi</p>');
      const iframeDoc = document.querySelector('iframe')?.contentDocument;
      if (iframeDoc) {
        // jsdom supports contentDocument: should count 2.
        expect(accessibleDocumentCount()).toBe(2);
      } else {
        // jsdom returns null contentDocument: still 1.
        expect(accessibleDocumentCount()).toBe(1);
      }
    });
  });
});
