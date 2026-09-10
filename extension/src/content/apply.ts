import { unwrapEditorPayload } from '../lib/page-edit';
import type { ApplyNotEditableReason } from '../lib/messages';
import {
  findGitHubIssueEditor,
  isGitHubIssueOrPull,
} from './adapters/github';

function isVisible(el: Element): boolean {
  const html = el as HTMLElement;
  if (html.hidden) {
    return false;
  }
  const style = window.getComputedStyle?.(html);
  if (style && (style.display === 'none' || style.visibility === 'hidden')) {
    return false;
  }
  return true;
}

function dispatchEditorEvents(el: HTMLElement): void {
  el.dispatchEvent(
    new InputEvent('input', { bubbles: true, cancelable: true, inputType: 'insertText' }),
  );
  el.dispatchEvent(new Event('change', { bubbles: true }));
}

function setEditorValue(el: HTMLElement, text: string): void {
  if (el instanceof HTMLTextAreaElement || el instanceof HTMLInputElement) {
    el.focus();
    el.value = text;
    dispatchEditorEvents(el);
    return;
  }
  el.focus();
  el.textContent = text;
  dispatchEditorEvents(el);
}

export function findApplyTarget(): HTMLElement | null {
  if (isGitHubIssueOrPull(window.location)) {
    return findGitHubIssueEditor();
  }
  const active = document.activeElement;
  if (
    active instanceof HTMLElement &&
    isVisible(active) &&
    (active instanceof HTMLTextAreaElement || active.isContentEditable)
  ) {
    return active;
  }
  const nodes = document.querySelectorAll('textarea, [contenteditable="true"]');
  for (const node of nodes) {
    if (isVisible(node)) {
      return node as HTMLElement;
    }
  }
  return null;
}

const GITHUB_CLOSED_EDITOR_ERROR =
  'This field is not in edit mode. Click Edit on the page to open the editor, then press Apply again. Apply will not write the comment box.';

export async function applyToPage(
  text: string,
): Promise<{ ok: true } | { ok: false; error: string; reason?: ApplyNotEditableReason }> {
  const payload = unwrapEditorPayload(text);
  if (isGitHubIssueOrPull(window.location)) {
    const target = findGitHubIssueEditor();
    if (!target) {
      return { ok: false, reason: 'not-editable', error: GITHUB_CLOSED_EDITOR_ERROR };
    }
    setEditorValue(target, payload);
    return { ok: true };
  }
  const target = findApplyTarget();
  if (!target) {
    return {
      ok: false,
      reason: 'not-editable',
      error: 'No editor is open on this page. Put the field in edit mode, then press Apply again.',
    };
  }
  setEditorValue(target, payload);
  return { ok: true };
}
