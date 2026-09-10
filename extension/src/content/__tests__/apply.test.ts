import { afterEach, describe, expect, it } from 'vitest';
import { applyToPage, findApplyTarget } from '../apply';

describe('apply', () => {
  afterEach(() => {
    document.body.innerHTML = '';
  });

  it('sets textarea.value and records a bubbling input event', async () => {
    document.body.innerHTML = '<textarea id="editor">old</textarea>';
    const textarea = document.querySelector<HTMLTextAreaElement>('#editor');
    if (!textarea) {
      throw new Error('missing textarea');
    }
    const events: string[] = [];
    textarea.addEventListener('input', (event) => {
      events.push(event.type);
      expect(event).toBeInstanceOf(InputEvent);
      expect(event.bubbles).toBe(true);
    });
    textarea.addEventListener('change', (event) => {
      events.push(event.type);
    });

    const result = await applyToPage('new body with cluster-east');
    expect(result).toEqual({ ok: true });
    expect(textarea.value).toBe('new body with cluster-east');
    expect(events).toEqual(['input', 'change']);
  });

  it('prefers the focused textarea over the first visible one', async () => {
    document.body.innerHTML = `
      <textarea id="first">one</textarea>
      <textarea id="second">two</textarea>
    `;
    const second = document.querySelector<HTMLTextAreaElement>('#second');
    second?.focus();
    expect(findApplyTarget()).toBe(second);
    expect(await applyToPage('focused')).toEqual({ ok: true });
    expect(second?.value).toBe('focused');
  });

  it('returns a typed not-editable error when no editor is present', async () => {
    document.body.innerHTML = '<p>read only</p>';
    const result = await applyToPage('x');
    expect(result.ok).toBe(false);
    if (result.ok) {
      throw new Error('expected apply to fail');
    }
    expect(result.reason).toBe('not-editable');
    expect(result.error).toMatch(/edit mode/i);
  });

  it('writes into a GitHub issue-body textarea when present', async () => {
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: new URL('https://github.com/acme/app/issues/12'),
    });
    document.body.innerHTML = '<textarea name="issue[body]">old issue</textarea>';
    const textarea = document.querySelector<HTMLTextAreaElement>('textarea[name="issue[body]"]');
    expect(await applyToPage('cluster-east')).toEqual({ ok: true });
    expect(textarea?.value).toBe('cluster-east');
  });

  it('unwraps a page-tool JSON envelope before writing markdown', async () => {
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: new URL('https://github.com/acme/app/issues/12'),
    });
    document.body.innerHTML = '<textarea name="issue[body]">old</textarea>';
    const textarea = document.querySelector<HTMLTextAreaElement>('textarea[name="issue[body]"]');
    const markdown = '### Requirement Overview\n\nSupport SCI credentials.';
    expect(await applyToPage(JSON.stringify({ ref: 'ref1', text: markdown }))).toEqual({ ok: true });
    expect(textarea?.value).toBe(markdown);
    expect(textarea?.value).not.toContain('"ref"');
  });

  it('prefers the GitHub issue description over the always-visible comment box', async () => {
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: new URL('https://github.com/acme/app/issues/12'),
    });
    document.body.innerHTML = `
      <textarea name="issue[body]">old description</textarea>
      <form id="new_comment_form">
        <textarea name="comment[body]" id="new_comment_field">draft comment</textarea>
      </form>
    `;
    const description = document.querySelector<HTMLTextAreaElement>('textarea[name="issue[body]"]');
    const comment = document.querySelector<HTMLTextAreaElement>('textarea[name="comment[body]"]');
    comment?.focus();
    expect(findApplyTarget()).toBe(description);
    expect(await applyToPage('updated description')).toEqual({ ok: true });
    expect(description?.value).toBe('updated description');
    expect(comment?.value).toBe('draft comment');
  });

  it('refuses to write the GitHub comment box when the description editor is closed', async () => {
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: new URL('https://github.com/acme/app/issues/12'),
    });
    document.body.innerHTML = `
      <form id="new_comment_form">
        <textarea name="comment[body]" id="new_comment_field">draft comment</textarea>
      </form>
    `;
    const comment = document.querySelector<HTMLTextAreaElement>('textarea[name="comment[body]"]');
    expect(findApplyTarget()).toBeNull();
    const result = await applyToPage('should not land here');
    expect(result.ok).toBe(false);
    if (result.ok) {
      throw new Error('expected apply to fail');
    }
    expect(result.reason).toBe('not-editable');
    expect(result.error).toMatch(/edit/i);
    expect(result.error).toMatch(/comment/i);
    expect(comment?.value).toBe('draft comment');
  });

  it('does NOT auto-open edit mode: read-only description returns not-editable and leaves the comment box untouched', async () => {
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: new URL('https://github.com/acme/app/issues/12'),
    });
    // A read-only description with an Edit button present, but no open editor.
    // The extension must NOT click Edit for the user.
    let editClicked = false;
    document.body.innerHTML = `
      <article class="markdown-body">old description</article>
      <button type="button" aria-label="Edit">Edit</button>
      <form id="new_comment_form">
        <textarea name="comment[body]" id="new_comment_field">draft comment</textarea>
      </form>
    `;
    document.querySelector('button')?.addEventListener('click', () => {
      editClicked = true;
      const editor = document.createElement('textarea');
      editor.setAttribute('name', 'issue[body]');
      document.body.appendChild(editor);
    });
    const comment = document.querySelector<HTMLTextAreaElement>('textarea[name="comment[body]"]');
    expect(findApplyTarget()).toBeNull();
    const result = await applyToPage('updated description');
    expect(result.ok).toBe(false);
    if (result.ok) {
      throw new Error('expected apply to fail');
    }
    expect(result.reason).toBe('not-editable');
    // The extension never clicked Edit and never mounted the editor.
    expect(editClicked).toBe(false);
    expect(document.querySelector('textarea[name="issue[body]"]')).toBeNull();
    // And it never wrote the comment box.
    expect(comment?.value).toBe('draft comment');
  });

  it('does NOT open a kebab "Issue body actions" menu on the user\'s behalf', async () => {
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: new URL('https://github.com/acme/app/issues/12'),
    });
    let menuOpened = false;
    document.body.innerHTML = `
      <article class="markdown-body">old description</article>
      <details class="js-issue-body-actions">
        <summary aria-label="Issue body actions" role="button">…</summary>
        <div class="dropdown-menu" hidden>
          <button type="button" class="dropdown-item" role="menuitem">Edit</button>
          <button type="button" class="dropdown-item" role="menuitem">Delete</button>
        </div>
      </details>
      <form id="new_comment_form">
        <textarea name="comment[body]" id="new_comment_field">draft comment</textarea>
      </form>
    `;
    document.querySelector('summary')?.addEventListener('click', () => {
      menuOpened = true;
    });
    const comment = document.querySelector<HTMLTextAreaElement>('textarea[name="comment[body]"]');
    expect(findApplyTarget()).toBeNull();
    const result = await applyToPage('updated via kebab');
    expect(result.ok).toBe(false);
    if (result.ok) {
      throw new Error('expected apply to fail');
    }
    expect(result.reason).toBe('not-editable');
    expect(menuOpened).toBe(false);
    expect(document.querySelector('textarea[name="issue[body]"]')).toBeNull();
    expect(comment?.value).toBe('draft comment');
  });
});
