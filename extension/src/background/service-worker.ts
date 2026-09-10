import {
  MSG_APPLY,
  MSG_GET_CONTEXT,
  MSG_PAGE_TOOL,
  MSG_PING,
  type ApplyResult,
  type ContextResult,
  type PageToolResult,
} from '../lib/messages';
import {
  clickImpliesNavigation,
  formatAfterNavigation,
  resolveHttpUrl,
  sameDocumentNavigation,
  waitForPossibleNavigation,
} from '../lib/page-navigate';

const CONTENT_SCRIPT = 'content-script.js';

chrome.runtime.onInstalled.addListener(() => {
  void chrome.sidePanel.setPanelBehavior({ openPanelOnActionClick: true });
});

void chrome.sidePanel.setPanelBehavior({ openPanelOnActionClick: true });

const UNREADABLE = 'This page cannot be read';

function isUnreadableUrl(url: string | undefined): boolean {
  if (!url) {
    return true;
  }
  try {
    const parsed = new URL(url);
    if (parsed.protocol === 'chrome:' || parsed.protocol === 'chrome-extension:') {
      return true;
    }
    if (parsed.hostname === 'chrome.google.com' && parsed.pathname.startsWith('/webstore')) {
      return true;
    }
    if (parsed.pathname.toLowerCase().endsWith('.pdf') || parsed.protocol === 'chrome-extension:') {
      return true;
    }
  } catch {
    return true;
  }
  return false;
}

async function activeTab(): Promise<chrome.tabs.Tab> {
  const [tab] = await chrome.tabs.query({ active: true, lastFocusedWindow: true });
  if (!tab?.id) {
    throw new Error(UNREADABLE);
  }
  return tab;
}

async function ensureContentScript(tabId: number): Promise<void> {
  try {
    await chrome.tabs.sendMessage(tabId, { type: MSG_PING });
    return;
  } catch {
    // Not injected yet.
  }
  await chrome.scripting.executeScript({
    target: { tabId },
    files: [CONTENT_SCRIPT],
  });
}

function sendToTab<T>(tabId: number, message: unknown): Promise<T> {
  return chrome.tabs.sendMessage(tabId, message) as Promise<T>;
}

async function snapshotTab(tabId: number): Promise<PageToolResult> {
  await ensureContentScript(tabId);
  return sendToTab<PageToolResult>(tabId, {
    type: MSG_PAGE_TOOL,
    name: 'page_snapshot',
    args: {},
  });
}

async function settleThenSnapshot(
  tabId: number,
  fromUrl: string | undefined,
  actionLine: string,
): Promise<PageToolResult> {
  await waitForPossibleNavigation(tabId, fromUrl);
  const snap = await snapshotTab(tabId);
  if (!snap.ok) {
    return {
      ok: true,
      name: 'page_click',
      result: formatAfterNavigation(
        actionLine,
        snap.error || 'Could not snapshot after navigation. Call page_snapshot in this tab.',
      ),
    };
  }
  return {
    ok: true,
    name: 'page_click',
    result: formatAfterNavigation(actionLine, snap.result ?? '(empty snapshot)'),
  };
}

async function runPageToolInTab(
  tab: chrome.tabs.Tab,
  name: string,
  args: Record<string, unknown>,
): Promise<PageToolResult> {
  const tabId = tab.id as number;
  if (name === 'page_navigate') {
    const href = resolveHttpUrl(args.url, tab.url);
    if (!href) {
      return { ok: false, name, error: 'page_navigate requires a http(s) url.' };
    }
    if (sameDocumentNavigation(tab.url, href)) {
      const snap = await snapshotTab(tabId);
      return {
        ok: true,
        name,
        result: formatAfterNavigation(
          `Already on ${href}. Did not reload this tab.`,
          snap.result || snap.error || '(empty snapshot)',
        ),
      };
    }
    await chrome.tabs.update(tabId, { url: href });
    const settled = await settleThenSnapshot(tabId, tab.url, `Navigated this tab to ${href}.`);
    return { ...settled, name: 'page_navigate' };
  }

  let result: PageToolResult;
  try {
    result = await sendToTab<PageToolResult>(tabId, {
      type: MSG_PAGE_TOOL,
      name,
      args,
    });
  } catch (err) {
    if (name === 'page_click') {
      const action = `Clicked in this tab; the page started navigating (${err instanceof Error ? err.message : 'frame gone'}).`;
      return settleThenSnapshot(tabId, tab.url, action);
    }
    throw err;
  }

  if (name === 'page_click' && result.ok && clickImpliesNavigation(result.href, tab.url)) {
    const href = resolveHttpUrl(result.href, tab.url);
    // Overlay / preventDefault on news cards often swallows element.click().
    // Load the href in this tab so the click still lands.
    if (href) {
      await chrome.tabs.update(tabId, { url: href });
    }
    const action = result.result || `Clicked a link (${result.href}).`;
    try {
      return await settleThenSnapshot(tabId, tab.url, action);
    } catch (err) {
      return {
        ok: true,
        name,
        result: formatAfterNavigation(
          action,
          err instanceof Error ? err.message : 'Navigation finished; call page_snapshot in this tab.',
        ),
      };
    }
  }

  return result;
}

chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
  const type = message && typeof message === 'object' ? (message as { type?: unknown }).type : undefined;
  if (type !== MSG_GET_CONTEXT && type !== MSG_APPLY && type !== MSG_PAGE_TOOL) {
    sendResponse({ ok: false, error: 'not implemented' });
    return false;
  }

  void (async () => {
    try {
      const tab = await activeTab();
      if (isUnreadableUrl(tab.url)) {
        sendResponse({ ok: false, error: UNREADABLE } satisfies ContextResult | ApplyResult | PageToolResult);
        return;
      }
      await ensureContentScript(tab.id as number);
      if (type === MSG_GET_CONTEXT) {
        const result = await sendToTab<ContextResult>(tab.id as number, { type: MSG_GET_CONTEXT });
        sendResponse(result);
        return;
      }
      if (type === MSG_PAGE_TOOL) {
        const name = typeof (message as { name?: unknown }).name === 'string' ? (message as { name: string }).name : '';
        const rawArgs = (message as { args?: unknown }).args;
        const args =
          rawArgs && typeof rawArgs === 'object' && !Array.isArray(rawArgs)
            ? (rawArgs as Record<string, unknown>)
            : {};
        const result = await runPageToolInTab(tab, name, args);
        sendResponse(result);
        return;
      }
      const text = typeof (message as { text?: unknown }).text === 'string' ? (message as { text: string }).text : '';
      const result = await sendToTab<ApplyResult>(tab.id as number, {
        type: MSG_APPLY,
        text,
        mode: 'replace',
      });
      sendResponse(result);
    } catch (err) {
      sendResponse({
        ok: false,
        error: err instanceof Error ? err.message : UNREADABLE,
      });
    }
  })();
  return true;
});
