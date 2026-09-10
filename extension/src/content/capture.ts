import { capMainText, type PageContext } from '../lib/page-context';
import { captureGitHub } from './adapters/github';

function selectionText(): string {
  return window.getSelection()?.toString() ?? '';
}

function editorPresent(): boolean {
  return !!document.querySelector('textarea, [contenteditable="true"]');
}

function genericMainText(): string {
  const root =
    document.querySelector('article') ||
    document.querySelector('[role="main"]') ||
    document.querySelector('main') ||
    document.body;
  return capMainText((root?.textContent ?? '').replace(/\s+/g, ' ').trim());
}

export function capturePage(): PageContext {
  const github = captureGitHub(window.location);
  if (github) {
    return github;
  }
  const hasEditor = editorPresent();
  return {
    url: window.location.href,
    title: document.title,
    hostname: window.location.hostname,
    selection: selectionText(),
    mainText: genericMainText(),
    editorPresent: hasEditor,
    editorKind: hasEditor ? 'plain' : 'none',
    adapter: 'generic',
    pageMap: {
      kind: 'document',
      regions: [
        { name: 'Main content', status: 'readable' },
        {
          name: 'Editor',
          status: hasEditor ? 'present' : 'none',
          notes: hasEditor
            ? 'Apply writes the focused or first text field.'
            : 'No editor on this page.',
        },
      ],
      actions: ['You are already on this page. Do not page_navigate unless you need a different URL.'],
    },
  };
}
