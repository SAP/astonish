import { formatPageMap } from '../lib/page-context';
import { unwrapEditorPayload } from '../lib/page-edit';
import {
  findGitHubIssueEditor,
  isGitHubCommentField,
  isGitHubIssueBodyField,
  isGitHubIssueOrPull,
} from './adapters/github';
import { capturePage } from './capture';

const INTERACTIVE = [
  'a[href]',
  'button',
  'input',
  'textarea',
  'select',
  '[contenteditable="true"]',
  '[role="button"]',
  '[role="link"]',
  '[role="textbox"]',
  '[role="menuitem"]',
  '[role="option"]',
  '[role="gridcell"]',
  '[role="row"]',
  '[role="listitem"]',
  '[tabindex="0"]',
].join(',');

const MAX_REFS = 400;
const QUERY_MAX = 30;
const RESULT_CAP = 20_000;

type RefEntry = { ref: string; el: HTMLElement };

let refs: RefEntry[] = [];

function cap(text: string, limit = RESULT_CAP): string {
  if (text.length <= limit) {
    return text;
  }
  return `${text.slice(0, limit)}\n…(truncated)`;
}

/** Recursively collect the top document plus all same-origin iframe contentDocuments. */
function getAccessibleDocuments(root: Document = document): Document[] {
  const docs: Document[] = [root];
  try {
    const iframes = root.querySelectorAll('iframe');
    for (const iframe of iframes) {
      try {
        const iframeDoc = (iframe as HTMLIFrameElement).contentDocument;
        if (iframeDoc) {
          docs.push(...getAccessibleDocuments(iframeDoc));
        }
      } catch {
        // Cross-origin iframe — skip silently.
      }
    }
  } catch {
    // Defensive: root.querySelectorAll may fail on detached documents.
  }
  return docs;
}

/** If el lives inside an iframe, return a display label like ' (iframe: /path)'. */
function iframeSource(el: HTMLElement): string {
  const ownerDoc = el.ownerDocument;
  if (ownerDoc === document) return '';
  try {
    const frameEl = ownerDoc.defaultView?.frameElement as HTMLIFrameElement | null;
    if (frameEl) {
      const src = frameEl.getAttribute('src') || frameEl.id || '(embedded)';
      return ` (iframe: ${src})`;
    }
  } catch {
    // Cross-origin frameElement access.
  }
  return ' (iframe)';
}

/** Get an element's bounding rect in tab-absolute coordinates (accounting for iframe nesting). */
function getElementTabRect(el: HTMLElement): { x: number; y: number; width: number; height: number } {
  const r = el.getBoundingClientRect();
  let x = r.x, y = r.y;
  let doc = el.ownerDocument;
  while (doc !== document) {
    try {
      const frameEl = doc.defaultView?.frameElement as HTMLIFrameElement | null;
      if (!frameEl) break;
      const fr = frameEl.getBoundingClientRect();
      x += fr.x;
      y += fr.y;
      doc = frameEl.ownerDocument;
    } catch { break; }
  }
  return { x, y, width: r.width, height: r.height };
}

function isVisible(el: HTMLElement): boolean {
  const html = el;
  if (html.hidden) {
    return false;
  }
  if (html.getAttribute('aria-hidden') === 'true') {
    return false;
  }
  // Use the element's own window for getComputedStyle — cross-origin safe.
  const win = (html.ownerDocument?.defaultView ?? window) as Window & typeof globalThis;
  const style = win.getComputedStyle?.(html);
  if (style && (style.display === 'none' || style.visibility === 'hidden')) {
    return false;
  }
  return true;
}

function accessibleName(el: HTMLElement): string {
  const labelled = el.getAttribute('aria-label');
  if (labelled?.trim()) {
    return labelled.trim();
  }
  if (isInputOrTextarea(el)) {
    const input = el as HTMLInputElement | HTMLTextAreaElement;
    const placeholder = el.getAttribute('placeholder');
    if (input.value.trim()) {
      return input.value.trim().slice(0, 80);
    }
    if (placeholder?.trim()) {
      return placeholder.trim();
    }
  }
  const text = (el.innerText || el.textContent || '').replace(/\s+/g, ' ').trim();
  return text.slice(0, 80);
}

function tagLabel(el: HTMLElement): string {
  const role = el.getAttribute('role');
  if (role) {
    return role;
  }
  const tag = el.tagName.toLowerCase();
  if (tag === 'a') {
    return 'link';
  }
  if (tag === 'input') {
    return (el as HTMLInputElement).type || 'input';
  }
  return tag;
}

function resolveRef(ref: string): HTMLElement | null {
  const entry = refs.find((item) => item.ref === ref);
  return entry?.el ?? null;
}

function githubClickBlocked(el: HTMLElement): string | null {
  if (window.location.hostname !== 'github.com') {
    return null;
  }
  const name = accessibleName(el).toLowerCase();
  if (
    name === 'comment' ||
    name === 'close comment' ||
    name === 'comment on this issue' ||
    name === 'comment on this pull request' ||
    name === 'add comment' ||
    name === 'submit comment'
  ) {
    return 'Refusing to click GitHub Comment. That submits or opens a comment, not the issue description. Click Edit on the page to open the editor, then Apply again, or ask the user if they want a comment instead.';
  }
  // Do not let the model drive edit mode on the user's behalf. When the
  // description editor is not already open, refuse clicks on the Edit button
  // and the "Issue body actions" (…) kebab menu. Opening Edit is the user's job;
  // the model should emit an astonish-page-edit fence and stop. Once the editor
  // is open, these clicks are allowed (findGitHubIssueEditor() is truthy).
  if (isGitHubIssueOrPull(window.location) && !findGitHubIssueEditor()) {
    if (
      name === 'edit' ||
      name === 'edit issue' ||
      name === 'edit comment' ||
      name === 'issue body actions' ||
      name === 'edit issue description'
    ) {
      return 'Refusing to open GitHub edit mode for you. Do not click Edit or the "Issue body actions" (…) kebab menu. Emit the complete updated description in ONE astonish-page-edit fence and stop — opening Edit and pressing Apply is the user\'s job.';
    }
  }
  return null;
}

function dispatchEditorEvents(el: HTMLElement): void {
  el.dispatchEvent(
    new InputEvent('input', { bubbles: true, cancelable: true, inputType: 'insertText' }),
  );
  el.dispatchEvent(new Event('change', { bubbles: true }));
}

function inViewport(el: HTMLElement): boolean {
  const rect = el.getBoundingClientRect();
  return rect.bottom > 0 && rect.top < (window.innerHeight || 0) && rect.right > 0 && rect.left < (window.innerWidth || 0);
}

function preferViewport(els: HTMLElement[]): HTMLElement[] {
  const inView: HTMLElement[] = [];
  const rest: HTMLElement[] = [];
  for (const el of els) {
    if (inViewport(el)) {
      inView.push(el);
    } else {
      rest.push(el);
    }
  }
  return inView.concat(rest);
}

function isHTMLElement(el: Element): el is HTMLElement {
  // Cross-window safe: elements from iframe documents have a different HTMLElement
  // constructor than the top frame, so `instanceof HTMLElement` returns false for them.
  // Check using the element's own window context instead.
  const win = el.ownerDocument?.defaultView as (Window & { HTMLElement?: typeof HTMLElement }) | null;
  if (win?.HTMLElement) {
    return el instanceof win.HTMLElement;
  }
  return el instanceof HTMLElement;
}

function collectCandidates(args: Record<string, unknown>): HTMLElement[] | string {
  const selector = typeof args.selector === 'string' ? args.selector : '';
  const text = typeof args.text === 'string' ? args.text.trim().toLowerCase() : '';
  let candidates: HTMLElement[] = [];
  const docs = getAccessibleDocuments();
  if (selector) {
    try {
      for (const doc of docs) {
        candidates.push(
          ...[...doc.querySelectorAll(selector)].filter(
            (el): el is HTMLElement => isHTMLElement(el) && isVisible(el as HTMLElement),
          ),
        );
      }
    } catch {
      return `Invalid selector: ${selector}`;
    }
  } else {
    for (const doc of docs) {
      candidates.push(
        ...[...doc.querySelectorAll(INTERACTIVE)].filter(
          (el): el is HTMLElement => isHTMLElement(el) && isVisible(el as HTMLElement),
        ),
      );
    }
  }
  if (text) {
    candidates = candidates.filter((el) => {
      const name = accessibleName(el).toLowerCase();
      const href = el instanceof HTMLAnchorElement ? el.href.toLowerCase() : '';
      return name.includes(text) || href.includes(text);
    });
  }
  return candidates;
}

function githubFieldRole(el: HTMLElement): string {
  if (!isGitHubIssueOrPull(window.location)) {
    return '';
  }
  if (isGitHubIssueBodyField(el)) {
    return ' issue-description';
  }
  if (isGitHubCommentField(el)) {
    return ' comment-box';
  }
  return '';
}

function editorKindHint(el: HTMLElement): string {
  if (!(el instanceof HTMLTextAreaElement) && !el.isContentEditable) {
    return '';
  }
  let kind = ' text';
  if (isGitHubIssueOrPull(window.location)) {
    kind = ' markdown';
  } else {
    const hint = `${el.getAttribute('name') || ''} ${el.id} ${el.getAttribute('aria-label') || ''}`.toLowerCase();
    if (/markdown|issue\[body\]|comment\[body\]|pull_request\[body\]/.test(hint)) {
      kind = ' markdown';
    }
  }
  return `${kind}${githubFieldRole(el)}`;
}

function formatInteractive(els: HTMLElement[], max: number): string {
  refs = [];
  const lines: string[] = [];
  const shown = els.slice(0, max);
  for (const [i, el] of shown.entries()) {
    const ref = `ref${i + 1}`;
    refs.push({ ref, el });
    const name = accessibleName(el) || '(unnamed)';
    const href = el instanceof HTMLAnchorElement ? el.href : '';
    const extra = href ? ` href=${href}` : '';
    const kind = editorKindHint(el);
    lines.push(`[${ref}] ${tagLabel(el)}${kind} "${name}"${extra}${iframeSource(el)}`);
  }
  if (!shown.length) {
    lines.push('(none)');
  } else if (els.length > max) {
    lines.push(`… ${els.length - max} more omitted`);
  }
  return lines.join('\n');
}

/**
 * Collect interactive elements from the current document only (no iframe traversal).
 * Used by child-frame executions via MSG_FRAME_TOOL.
 * The refOffset ensures refs from this frame don't collide with the top frame's refs.
 */
function collectCandidatesFrameLocal(
  args: Record<string, unknown>,
  refOffset: number,
): HTMLElement[] | string {
  const selector = typeof args.selector === 'string' ? args.selector : '';
  const text = typeof args.text === 'string' ? args.text.trim().toLowerCase() : '';
  let candidates: HTMLElement[] = [];
  if (selector) {
    try {
      candidates = [...document.querySelectorAll(selector)].filter(
        (el): el is HTMLElement => isHTMLElement(el) && isVisible(el as HTMLElement),
      );
    } catch {
      return `Invalid selector: ${selector}`;
    }
  } else {
    candidates = [...document.querySelectorAll(INTERACTIVE)].filter(
      (el): el is HTMLElement => isHTMLElement(el) && isVisible(el as HTMLElement),
    );
  }
  if (text) {
    candidates = candidates.filter((el) => {
      const name = accessibleName(el).toLowerCase();
      const href = el instanceof HTMLAnchorElement ? el.href.toLowerCase() : '';
      return name.includes(text) || href.includes(text);
    });
  }
  // Populate refs with offset so resolveRef works for click/fill calls.
  refs = [];
  const max = MAX_REFS;
  const shown = candidates.slice(0, max);
  for (const [i, el] of shown.entries()) {
    refs.push({ ref: `ref${refOffset + i + 1}`, el });
  }
  return candidates;
}

/** Render interactive elements for frame-local execution, returning a string. */
function snapshotFrameInteractive(args: Record<string, unknown>): string {
  const refOffset = typeof args._refOffset === 'number' ? args._refOffset : 0;
  const candidates = collectCandidatesFrameLocal(args, refOffset);
  if (typeof candidates === 'string') return candidates;
  const ordered = args.selector || args.text ? candidates : preferViewport(candidates);
  return formatInteractiveWithOffset(ordered, MAX_REFS, refOffset);
}

/** Collect headings from the current document only. */
function snapshotFrameHeadings(): string {
  return [...document.querySelectorAll('h1, h2, h3')]
    .map((el) => (el.textContent ?? '').replace(/\s+/g, ' ').trim())
    .filter(Boolean)
    .slice(0, 20)
    .join('\n');
}

/** Run page_query scoped to the current frame only. */
function queryFrame(args: Record<string, unknown>): string {
  const refOffset = typeof args._refOffset === 'number' ? args._refOffset : 0;
  const candidates = collectCandidatesFrameLocal(args, refOffset);
  if (typeof candidates === 'string') return candidates;
  if (!candidates.length) return 'No matching elements.';
  return cap(formatInteractiveWithOffset(candidates, QUERY_MAX, refOffset));
}

/** Like formatInteractive but applies a ref offset (for child-frame refs). */
function formatInteractiveWithOffset(els: HTMLElement[], max: number, refOffset: number): string {
  const lines: string[] = [];
  const shown = els.slice(0, max);
  for (const [i, el] of shown.entries()) {
    const ref = `ref${refOffset + i + 1}`;
    const name = accessibleName(el) || '(unnamed)';
    const href = el instanceof HTMLAnchorElement ? el.href : '';
    const extra = href ? ` href=${href}` : '';
    const kind = editorKindHint(el);
    lines.push(`[${ref}] ${tagLabel(el)}${kind} "${name}"${extra}`);
  }
  if (!shown.length) {
    lines.push('(none)');
  } else if (els.length > max) {
    lines.push(`… ${els.length - max} more omitted`);
  }
  return lines.join('\n');
}

function snapshot(args: Record<string, unknown> = {}): string {
  const filtered = collectCandidates(args);
  if (typeof filtered === 'string') {
    return filtered;
  }
  const ordered = args.selector || args.text ? filtered : preferViewport(filtered);
  const map = formatPageMap(capturePage().pageMap);
  const lines: string[] = [`URL: ${window.location.href}`, `Title: ${document.title}`];
  if (map) {
    lines.push('', map);
  }
  lines.push('', 'Interactive:', formatInteractive(ordered, MAX_REFS));
  const allDocs = getAccessibleDocuments();
  const headings = allDocs
    .flatMap((doc) => [...doc.querySelectorAll('h1, h2, h3')])
    .map((el) => (el.textContent ?? '').replace(/\s+/g, ' ').trim())
    .filter(Boolean)
    .slice(0, 20);
  if (headings.length) {
    lines.push('', 'Headings:', ...headings.map((h) => `- ${h}`));
  }
  return cap(lines.join('\n'));
}

function query(args: Record<string, unknown>): string {
  const filtered = collectCandidates(args);
  if (typeof filtered === 'string') {
    return filtered;
  }
  if (!filtered.length) {
    return 'No matching elements.';
  }
  return cap(formatInteractive(filtered, QUERY_MAX));
}

type ClickOutcome = { ok: boolean; result: string; href?: string; rect?: { x: number; y: number; width: number; height: number } };

function click(args: Record<string, unknown>): ClickOutcome {
  const ref = typeof args.ref === 'string' ? args.ref : '';
  const el = resolveRef(ref);
  if (!el) {
    return { ok: false, result: `Unknown ref ${ref || '(missing)'}. Run page_snapshot or page_query first.` };
  }
  const blocked = githubClickBlocked(el);
  if (blocked) {
    return { ok: false, result: blocked };
  }
  const anchor = el instanceof HTMLAnchorElement ? el : el.closest('a[href]');
  if (anchor instanceof HTMLAnchorElement && (anchor.target === '_blank' || anchor.getAttribute('target') === '_blank')) {
    anchor.setAttribute('target', '_self');
  }
  const href = anchor instanceof HTMLAnchorElement ? anchor.href : undefined;
  const clickable = anchor instanceof HTMLAnchorElement ? anchor : el;
  try {
    clickable.scrollIntoView({ block: 'center', inline: 'nearest' });
  } catch {
    // jsdom and some frames omit scrollIntoView.
  }
  const rect = getElementTabRect(clickable);
  // If the element has a real bounding rect, return it for CDP dispatch by the service worker.
  // If rect is zero (hidden, off-screen, or jsdom), fall back to DOM synthetic events.
  if (!rect.width && !rect.height) {
    for (const type of ['pointerdown', 'mousedown', 'mouseup', 'click'] as const) {
      try {
        clickable.dispatchEvent(
          new MouseEvent(type, { bubbles: true, cancelable: true, buttons: 1 }),
        );
      } catch {
        // Pointer/mouse events are best-effort; HTMLElement.click still runs.
      }
    }
    clickable.click();
  }
  return {
    ok: true,
    result: `Clicked ${ref} (${tagLabel(el)} "${accessibleName(el) || '(unnamed)'}"${href ? ` href=${href}` : ''}).`,
    href,
    rect,
  };
}

function isInputOrTextarea(el: HTMLElement): el is HTMLInputElement | HTMLTextAreaElement {
  // Cross-window safe check — el may come from an iframe document.
  const tag = el.tagName?.toLowerCase();
  return tag === 'input' || tag === 'textarea';
}

type FillOutcome = { ok: boolean; result: string; rect?: { x: number; y: number; width: number; height: number } };

function fill(args: Record<string, unknown>): FillOutcome {
  const ref = typeof args.ref === 'string' ? args.ref : '';
  const text = unwrapEditorPayload(args.text);
  const el = resolveRef(ref);
  if (!el) {
    return { ok: false, result: `Unknown ref ${ref || '(missing)'}. Run page_snapshot or page_query first.` };
  }
  if (
    isGitHubIssueOrPull(window.location) &&
    isGitHubCommentField(el) &&
    !isGitHubIssueBodyField(el)
  ) {
    return { ok: false, result: 'Refusing to fill the GitHub comment box. The issue description is not in edit mode. Click Edit on the description first, then page_fill that editor. Only fill this field if the user asked to add a comment.' };
  }
  const rect = getElementTabRect(el);
  if (isInputOrTextarea(el)) {
    el.focus();
    (el as HTMLInputElement | HTMLTextAreaElement).value = text;
    dispatchEditorEvents(el);
    return { ok: true, result: `Filled ${ref}.`, rect };
  }
  if (el.isContentEditable) {
    el.focus();
    el.textContent = text;
    dispatchEditorEvents(el);
    return { ok: true, result: `Filled ${ref}.`, rect };
  }
  return { ok: false, result: `${ref} is not an editable field.` };
}

function selectOption(args: Record<string, unknown>): string {
  const ref = typeof args.ref === 'string' ? args.ref : '';
  const el = resolveRef(ref);
  if (!(el instanceof HTMLSelectElement)) {
    return `Unknown select ref ${ref || '(missing)'}. Run page_snapshot or page_query first.`;
  }
  const values = Array.isArray(args.values)
    ? args.values.map((v) => String(v))
    : typeof args.values === 'string'
      ? [args.values]
      : typeof args.value === 'string'
        ? [args.value]
        : [];
  if (!values.length) {
    return 'values is required.';
  }
  for (const option of el.options) {
    option.selected = values.includes(option.value) || values.includes(option.text);
  }
  dispatchEditorEvents(el);
  return `Selected ${values.join(', ')} on ${ref}.`;
}

function scroll(args: Record<string, unknown>): string {
  const ref = typeof args.ref === 'string' ? args.ref : '';
  if (ref) {
    const el = resolveRef(ref);
    if (!el) {
      return `Unknown ref ${ref}. Run page_snapshot or page_query first.`;
    }
    el.scrollIntoView({ block: 'center', inline: 'nearest' });
    return `Scrolled ${ref} into view.`;
  }
  const y = typeof args.y === 'number' ? args.y : Number(args.y);
  if (Number.isFinite(y)) {
    window.scrollTo({ top: y, behavior: 'instant' in window ? 'instant' : 'auto' } as ScrollToOptions);
    return `Scrolled to y=${y}.`;
  }
  window.scrollBy({ top: window.innerHeight * 0.8, behavior: 'instant' as ScrollBehavior });
  return 'Scrolled down one viewport.';
}

export type PageToolRun = { ok: boolean; name: string; result?: string; error?: string; href?: string; rect?: { x: number; y: number; width: number; height: number } };

/**
 * Execute a page tool scoped to only this frame's own document (no same-origin iframe traversal).
 * Used when this content script is running in a child frame and the top-frame coordinator has
 * delegated the query here via MSG_FRAME_TOOL.
 *
 * @param name   The page tool name (page_snapshot, page_query, page_click, etc.)
 * @param args   The tool arguments.
 * @param refOffset  Starting ref number so ref IDs don't collide across frames
 *                   (e.g. top frame used ref1–ref10, so pass 10 here to get ref11–refN).
 */
export function runPageToolInFrame(
  name: string,
  args: Record<string, unknown>,
  refOffset: number,
): { interactive: string; headings: string; result?: string; error?: string; href?: string; rect?: { x: number; y: number; width: number; height: number }; refCount: number } {
  try {
    // Temporarily override document querying to be frame-local only.
    // We do this by passing a flag through the args.
    const frameArgs = { ...args, _frameLocal: true, _refOffset: refOffset };

    switch (name) {
      case 'page_snapshot': {
        const interactiveText = snapshotFrameInteractive(frameArgs);
        const headingText = snapshotFrameHeadings();
        return { interactive: interactiveText, headings: headingText, refCount: refs.length };
      }
      case 'page_query': {
        const interactiveText = queryFrame(frameArgs);
        return { interactive: interactiveText, headings: '', refCount: refs.length };
      }
      case 'page_click': {
        // First populate refs for this frame so resolveRef works.
        snapshotFrameInteractive({ ...args, _frameLocal: true, _refOffset: refOffset });
        const outcome = click(args);
        return {
          interactive: '',
          headings: '',
          result: outcome.result,
          href: outcome.href,
          rect: outcome.rect,
          refCount: refs.length,
          ...(outcome.ok ? {} : { error: outcome.result }),
        };
      }
      case 'page_fill': {
        snapshotFrameInteractive({ ...args, _frameLocal: true, _refOffset: refOffset });
        const outcome = fill(args);
        return {
          interactive: '',
          headings: '',
          result: outcome.result,
          rect: outcome.rect,
          refCount: refs.length,
          ...(outcome.ok ? {} : { error: outcome.result }),
        };
      }
      case 'page_scroll': {
        snapshotFrameInteractive({ ...args, _frameLocal: true, _refOffset: refOffset });
        const result = scroll(args);
        return { interactive: '', headings: '', result, refCount: refs.length };
      }
      default:
        return { interactive: '', headings: '', error: `Unknown tool: ${name}`, refCount: 0 };
    }
  } catch (err) {
    return {
      interactive: '',
      headings: '',
      error: err instanceof Error ? err.message : String(err),
      refCount: 0,
    };
  }
}

/** How many refs are currently registered (test helper + frame relay). */
export function frameRefCount(): number {
  return refs.length;
}

export function runPageTool(name: string, args: Record<string, unknown> = {}): PageToolRun {
  try {
    switch (name) {
      case 'page_snapshot':
        return { ok: true, name, result: snapshot(args) };
      case 'page_query':
        return { ok: true, name, result: query(args) };
      case 'page_click': {
        const outcome = click(args);
        return outcome.ok
          ? { ok: true, name, result: outcome.result, href: outcome.href, rect: outcome.rect }
          : { ok: false, name, error: outcome.result };
      }
      case 'page_navigate':
        return {
          ok: false,
          name,
          error: 'page_navigate is handled by the extension service worker so this tab can stay in the user session.',
        };
      case 'page_fill': {
        const outcome = fill(args);
        return outcome.ok
          ? { ok: true, name, result: outcome.result, rect: outcome.rect }
          : { ok: false, name, error: outcome.result };
      }
      case 'page_select': {
        const result = selectOption(args);
        return result.startsWith('Selected')
          ? { ok: true, name, result }
          : { ok: false, name, error: result };
      }
      case 'page_scroll':
        return { ok: true, name, result: scroll(args) };
      default:
        return {
          ok: false,
          name,
          error: `Unknown page tool "${name}". Use page_snapshot, page_query, page_click, page_navigate, page_fill, page_select, or page_scroll.`,
        };
    }
  } catch (err) {
    return { ok: false, name, error: err instanceof Error ? err.message : String(err) };
  }
}

/** Test helper: current snapshot refs. */
export function snapshotRefCount(): number {
  return refs.length;
}

/** Test helper: accessible document count (top frame + same-origin iframes). */
export function accessibleDocumentCount(): number {
  return getAccessibleDocuments().length;
}
