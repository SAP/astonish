import { capMainText, type PageContext, type PageMap } from '../../lib/page-context';

const ISSUE_OR_PR = /\/(issues|pull)\//;

const ISSUE_BODY_SELECTORS = [
  'textarea[name="issue[body]"]',
  'textarea[name="pull_request[body]"]',
  'textarea#issue_body',
];

const COMMENT_SELECTORS = [
  'textarea[name="comment[body]"]',
  'textarea.comment-form-textarea',
  '#new_comment_field',
];

const COMMENT_CONTAINER_SELECTORS = [
  '#new_comment_form',
  '.js-new-comment-form',
  '[data-testid="comment-composer"]',
  '.timeline-new-comment',
  '#issue-comment-box',
].join(',');

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

function firstVisible(selectors: string[]): HTMLTextAreaElement | null {
  for (const selector of selectors) {
    const el = document.querySelector<HTMLTextAreaElement>(selector);
    if (el && isVisible(el)) {
      return el;
    }
  }
  return null;
}

function inCommentComposer(el: Element): boolean {
  return !!el.closest(COMMENT_CONTAINER_SELECTORS);
}

/** Best-effort HTML → Markdown so captured GitHub issue bodies keep structure. */
export function htmlToMarkdown(root: Element): string {
  const walk = (node: Node): string => {
    if (node.nodeType === Node.TEXT_NODE) {
      return (node.textContent ?? '').replace(/\s+/g, ' ');
    }
    if (!(node instanceof HTMLElement)) {
      return [...node.childNodes].map(walk).join('');
    }
    const tag = node.tagName.toLowerCase();
    if (tag === 'br') {
      return '\n';
    }
    if (tag === 'pre') {
      return `\n\`\`\`\n${(node.textContent ?? '').replace(/\n$/, '')}\n\`\`\`\n\n`;
    }
    const inner = [...node.childNodes].map(walk).join('').trim();
    switch (tag) {
      case 'h1':
        return `\n# ${inner}\n\n`;
      case 'h2':
        return `\n## ${inner}\n\n`;
      case 'h3':
        return `\n### ${inner}\n\n`;
      case 'h4':
        return `\n#### ${inner}\n\n`;
      case 'p':
        return `\n${inner}\n\n`;
      case 'li':
        return `- ${inner}\n`;
      case 'ul':
      case 'ol':
        return `\n${[...node.children].map(walk).join('')}\n`;
      case 'strong':
      case 'b':
        return `**${inner}**`;
      case 'em':
      case 'i':
        return `*${inner}*`;
      case 'code':
        return `\`${inner}\``;
      case 'a': {
        const href = node.getAttribute('href') || '';
        return href ? `[${inner}](${href})` : inner;
      }
      case 'blockquote':
        return `\n> ${inner}\n\n`;
      default:
        return [...node.childNodes].map(walk).join('');
    }
  };
  return walk(root).replace(/\n{3,}/g, '\n\n').trim();
}

function githubTitle(): string {
  const el =
    document.querySelector('.js-issue-title') ||
    document.querySelector('h1.gh-header-title');
  const text = el?.textContent?.replace(/\s+/g, ' ').trim();
  return text || document.title;
}

function controlName(el: HTMLElement): string {
  const labelled = el.getAttribute('aria-label');
  if (labelled?.trim()) {
    return labelled.trim().toLowerCase();
  }
  return (el.innerText || el.textContent || '').replace(/\s+/g, ' ').trim().toLowerCase();
}

export function isGitHubCommentField(el: Element): boolean {
  if (el instanceof HTMLTextAreaElement) {
    const name = el.getAttribute('name') || '';
    if (name === 'issue[body]' || name === 'pull_request[body]' || el.id === 'issue_body') {
      return false;
    }
    if (name === 'comment[body]' || el.id === 'new_comment_field') {
      return true;
    }
    if (el.classList.contains('comment-form-textarea')) {
      return true;
    }
  }
  if (inCommentComposer(el)) {
    return true;
  }
  if (!(el instanceof HTMLElement)) {
    return false;
  }
  const label = `${el.getAttribute('aria-label') || ''} ${el.getAttribute('placeholder') || ''}`.toLowerCase();
  return /\b(add a comment|leave a comment|write a comment)\b/.test(label);
}

export function isGitHubIssueBodyField(el: Element): boolean {
  if (!(el instanceof HTMLTextAreaElement)) {
    return false;
  }
  const name = el.getAttribute('name') || '';
  if (name === 'issue[body]' || name === 'pull_request[body]' || el.id === 'issue_body') {
    return true;
  }
  if (isGitHubCommentField(el)) {
    return false;
  }
  return isVisible(el) && !inCommentComposer(el);
}

/** Visible issue / PR description editor. Never the always-visible comment box. */
export function findGitHubIssueEditor(): HTMLTextAreaElement | null {
  const named = firstVisible(ISSUE_BODY_SELECTORS);
  if (named) {
    return named;
  }
  const areas = [...document.querySelectorAll('textarea')].filter(
    (el): el is HTMLTextAreaElement =>
      el instanceof HTMLTextAreaElement && isVisible(el) && isGitHubIssueBodyField(el),
  );
  return (
    areas.find((el) => /markdown/i.test(`${el.getAttribute('aria-label') || ''} ${el.placeholder || ''}`)) ||
    areas[0] ||
    null
  );
}

export function findGitHubCommentEditor(): HTMLTextAreaElement | null {
  return firstVisible(COMMENT_SELECTORS);
}

/** Edit control for the issue/PR description — never a comment-composer button. */
export function findGitHubIssueEditControl(): HTMLElement | null {
  const nodes = document.querySelectorAll('button, a[href], [role="button"], [role="menuitem"], summary');
  for (const node of nodes) {
    if (!(node instanceof HTMLElement) || !isVisible(node) || inCommentComposer(node)) {
      continue;
    }
    const name = controlName(node);
    if (name === 'edit' || name === 'edit issue' || /^edit (issue|description|body)\b/.test(name)) {
      return node;
    }
  }
  return null;
}

/**
 * The kebab "Issue body actions" menu trigger. On modern GitHub the description
 * Edit affordance is hidden inside this menu, so it must be opened before the
 * Edit item exists in the DOM. Never a comment-composer control.
 */
export function findGitHubIssueActionsMenu(): HTMLElement | null {
  const nodes = document.querySelectorAll('button, [role="button"], summary');
  for (const node of nodes) {
    if (!(node instanceof HTMLElement) || !isVisible(node) || inCommentComposer(node)) {
      continue;
    }
    const name = controlName(node);
    if (
      name === 'issue body actions' ||
      name === 'issue actions' ||
      name === 'description actions' ||
      /\bbody actions\b/.test(name) ||
      (/\bactions\b/.test(name) && /\b(issue|description|body)\b/.test(name))
    ) {
      return node;
    }
  }
  return null;
}

function nextTick(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

/** Poll for the description editor for up to ~1s while React mounts it. */
export async function waitForGitHubIssueEditor(
  attempts = 20,
): Promise<HTMLTextAreaElement | null> {
  for (let i = 0; i < attempts; i += 1) {
    const editor = findGitHubIssueEditor();
    if (editor) {
      return editor;
    }
    await nextTick();
  }
  return findGitHubIssueEditor();
}

/**
 * Click Edit so the description enters edit mode. Handles both the direct
 * "Edit" button and GitHub's kebab "Issue body actions" menu (open it, then
 * click the revealed Edit item). Returns true if a control was clicked.
 */
export async function enterGitHubIssueEditMode(): Promise<boolean> {
  const direct = findGitHubIssueEditControl();
  if (direct) {
    direct.click();
    return true;
  }
  const menu = findGitHubIssueActionsMenu();
  if (!menu) {
    return false;
  }
  menu.click();
  // The menu items render after the trigger opens; wait a tick before looking.
  for (let i = 0; i < 5; i += 1) {
    const edit = findGitHubIssueEditControl();
    if (edit) {
      edit.click();
      return true;
    }
    await nextTick();
  }
  return false;
}

/** @deprecated Use findGitHubIssueEditor — Apply must not target the comment box. */
export function findGitHubEditor(): HTMLTextAreaElement | null {
  return findGitHubIssueEditor();
}

function githubIssueDescriptionMarkdown(): string {
  const editor = findGitHubIssueEditor();
  if (editor?.value.trim()) {
    return editor.value;
  }
  for (const selector of ISSUE_BODY_SELECTORS) {
    const el = document.querySelector<HTMLTextAreaElement>(selector);
    if (el?.value.trim()) {
      return el.value;
    }
  }
  const markdown =
    document.querySelector('.js-comment-body.markdown-body') ||
    document.querySelector('[data-testid="issue-body"] .markdown-body') ||
    document.querySelector('.timeline-comment .markdown-body') ||
    document.querySelector('article.markdown-body') ||
    document.querySelector('.markdown-body');
  if (markdown instanceof HTMLTextAreaElement && markdown.value.trim()) {
    return markdown.value;
  }
  if (markdown instanceof HTMLElement) {
    return htmlToMarkdown(markdown);
  }
  return '';
}

export function buildGitHubPageMap(location: Location | URL = window.location): PageMap {
  const issueOpen = !!findGitHubIssueEditor();
  const commentOpen = !!findGitHubCommentEditor();
  const kind = /\/pull\//.test(location.pathname) ? 'github-pr' : 'github-issue';
  return {
    kind,
    regions: [
      {
        name: 'Issue description',
        status: issueOpen ? 'editor open' : 'read-only',
        notes: issueOpen
          ? 'This is the field Apply writes. Use an astonish-page-edit fence with the full markdown document.'
          : 'Read-only right now. To update it, the user opens Edit on the page, then presses Apply. Emit the full updated description in an astonish-page-edit fence. Apply will not write the comment box.',
      },
      {
        name: 'New comment box',
        status: commentOpen ? 'always visible' : 'absent',
        notes: 'NOT the issue description. Do not page_fill or Apply here unless the user asked to add a comment.',
      },
    ],
    actions: issueOpen
      ? [
          'You are already on this issue. Do not page_navigate.',
          'The document is in Main content below. Do not page_query or page_snapshot to re-read it.',
          'Emit astonish-page-edit with the complete updated description (preserve existing sections). Never write the comment box unless the user asked to comment.',
        ]
      : [
          'STOP. Do NOT page_query, page_snapshot, or page_click to hunt for an Edit button, the "Issue body actions" kebab (…) menu, or any edit affordance. You cannot and must not open edit mode yourself.',
          'Emit the complete updated description in ONE astonish-page-edit fence, then STOP and end your turn. Do not ask permission to click anything and do not try again.',
          'The Apply bar will appear in the side panel. Opening Edit on the page and pressing Apply is the USER\'s job, not yours — the extension guides them through it.',
          'You are already on this issue. Do not page_navigate. The document is in Main content below; do not page_query or page_snapshot to re-read it.',
          'Do not write the comment box as a substitute for editing the description.',
        ],
  };
}

export function isGitHubIssueOrPull(location: Location | URL): boolean {
  return location.hostname === 'github.com' && ISSUE_OR_PR.test(location.pathname);
}

export function captureGitHub(location: Location | URL = window.location): PageContext | null {
  if (!isGitHubIssueOrPull(location)) {
    return null;
  }
  const issueEditor = findGitHubIssueEditor();
  return {
    url: location.href,
    title: githubTitle(),
    hostname: location.hostname,
    selection: window.getSelection()?.toString() ?? '',
    mainText: capMainText(githubIssueDescriptionMarkdown()),
    editorPresent: !!issueEditor,
    editorKind: 'markdown',
    adapter: 'github',
    pageMap: buildGitHubPageMap(location),
  };
}
