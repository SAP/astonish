import {
  MSG_APPLY,
  MSG_FRAME_TOOL,
  MSG_GET_CONTEXT,
  MSG_PAGE_TOOL,
  MSG_PING,
  type ApplyResult,
  type ContextResult,
  type FrameToolResult,
  type PageToolResult,
} from '../lib/messages';
import {
  clickImpliesNavigation,
  formatAfterNavigation,
  resolveHttpUrl,
  sameDocumentNavigation,
  waitForPossibleNavigation,
} from '../lib/page-navigate';
import { cdpClick, cdpFill } from './cdp-input';
import { removeTabState } from '../lib/extension-sessions';

const CONTENT_SCRIPT = 'content-script.js';

const FRAME_TOOL_TIMEOUT_MS = 5000;

/**
 * Send MSG_FRAME_TOOL to a specific frame (by frameId) and wait for its FrameToolResult.
 * Returns null if the frame times out or is unreachable.
 */
async function sendToFrame(
  tabId: number,
  frameId: number,
  name: string,
  args: Record<string, unknown>,
  refOffset: number,
): Promise<FrameToolResult | null> {
  // Retry up to 3 times — child frame content scripts may not have finished
  // registering their chrome.runtime.onMessage listener yet after injection.
  for (let attempt = 0; attempt < 3; attempt++) {
    const result = await new Promise<FrameToolResult | null>((resolve) => {
      const timer = setTimeout(() => resolve(null), FRAME_TOOL_TIMEOUT_MS);
      try {
        chrome.tabs.sendMessage(
          tabId,
          { type: MSG_FRAME_TOOL, name, args, refOffset },
          { frameId },
          (response: FrameToolResult | undefined) => {
            clearTimeout(timer);
            if (chrome.runtime.lastError || !response) {
              resolve(null);
            } else {
              resolve(response);
            }
          },
        );
      } catch {
        clearTimeout(timer);
        resolve(null);
      }
    });
    if (result) return result;
    // Brief delay before retry to let the content script finish loading.
    if (attempt < 2) {
      await new Promise((r) => setTimeout(r, 300));
    }
  }
  return null;
}

/**
 * Collect all frame IDs in the tab that have the content script loaded.
 * Returns the top frame (frameId 0) plus any child frames.
 */
async function getAllFrameIds(tabId: number): Promise<number[]> {
  try {
    const frames = await chrome.webNavigation.getAllFrames({ tabId });
    return (frames ?? []).map((f) => f.frameId);
  } catch {
    // webNavigation not available or tab not ready — fall back to top frame only.
    return [0];
  }
}

/**
 * Merge partial FrameToolResults from multiple frames into a single PageToolResult.
 * For snapshot/query: concatenate interactive lines + headings from all frames.
 * For click/fill/scroll: return the first successful result.
 */
function mergeFrameResults(
  name: string,
  frameResults: Array<FrameToolResult | null>,
  frameIds: number[],
): PageToolResult {
  const validResults = frameResults.map((r, i) => ({ r, frameId: frameIds[i] ?? 0 })).filter((x): x is { r: FrameToolResult; frameId: number } => x.r !== null);
  if (!validResults.length) {
    return { ok: false, name, error: 'No frames responded.' };
  }

  if (name === 'page_snapshot' || name === 'page_query') {
    const interactiveParts = validResults
      .map((x) => x.r.interactive)
      .filter(Boolean);
    const headingParts = validResults
      .map((x) => x.r.headings)
      .filter(Boolean);

    let result = interactiveParts.join('\n') || '(none)';

    // For snapshot, include URL/title/headings from the first (top) frame.
    if (name === 'page_snapshot') {
      const headingsText = headingParts.join('\n');
      if (headingsText) {
        result += '\n\nHeadings:\n' + headingParts.map((h) => h.split('\n').map((l) => `- ${l}`).join('\n')).join('\n');
      }
    }

    return { ok: true, name, result };
  }

  // For click/fill/scroll: first success wins; if none, return last error.
  for (const { r, frameId } of validResults) {
    if (!r.error) {
      return { ok: true, name, result: r.result, href: r.href, rect: r.rect, frameId };
    }
  }
  return { ok: false, name, error: validResults[validResults.length - 1].r.error };
}

/**
 * Run a page tool by injecting into all frames and aggregating results.
 * For snapshot/query: fans out to all frames.
 * For action tools (click/fill/scroll): fans out to all frames, uses first success.
 */
async function runPageToolAllFrames(
  tab: chrome.tabs.Tab,
  name: string,
  args: Record<string, unknown>,
): Promise<PageToolResult> {
  const tabId = tab.id as number;
  const frameIds = await getAllFrameIds(tabId);

  // Fan out to each frame sequentially so ref offsets accumulate correctly.
  const frameResults: Array<FrameToolResult | null> = [];
  let refOffset = 0;
  for (const frameId of frameIds) {
    const result = await sendToFrame(tabId, frameId, name, args, refOffset);
    frameResults.push(result);
    refOffset += result?.refCount ?? 0;
  }

  const merged = mergeFrameResults(name, frameResults, frameIds);

  // For page_snapshot, prepend the URL/title line from the tab.
  if (name === 'page_snapshot' && merged.ok) {
    const urlLine = `URL: ${tab.url ?? '(unknown)'}\nTitle: ${tab.title ?? '(unknown)'}`;
    merged.result = `${urlLine}\n\nInteractive:\n${merged.result}`;
  }

  return merged;
}

chrome.runtime.onInstalled.addListener(() => {
  // Do NOT use openPanelOnActionClick — it prevents us from embedding the tabId
  // in the panel URL. Instead we open it explicitly via action.onClicked.
  void chrome.sidePanel.setPanelBehavior({ openPanelOnActionClick: false });
});

void chrome.sidePanel.setPanelBehavior({ openPanelOnActionClick: false });

// Open the side panel for the clicked tab, embedding the tabId in the URL so the
// panel page can read it synchronously from location.search without any message passing.
chrome.action.onClicked.addListener((tab) => {
  if (!tab.id) return;
  const tabId = tab.id;
  void chrome.sidePanel.setOptions({
    tabId,
    path: `sidepanel.html?tabId=${tabId}`,
    enabled: true,
  });
  void chrome.sidePanel.open({ tabId });
});

// Clean up per-tab session state when a tab is closed.
chrome.tabs.onRemoved.addListener((tabId) => {
  void removeTabState(tabId);
});

const UNREADABLE = 'This page cannot be read';
const MSG_GET_TAB_ID = 'MSG_GET_TAB_ID';

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
  // Always inject into all frames. The top frame may already have the script
  // (which is fine — module-level state resets on re-inject), but child frames
  // (especially cross-origin iframes that loaded after the initial injection)
  // may not have it yet. Chrome silently handles injection into frames where the
  // script is already present.
  try {
    await chrome.scripting.executeScript({
      target: { tabId, allFrames: true },
      files: [CONTENT_SCRIPT],
    });
  } catch {
    // Some frames may refuse injection (e.g. chrome:// iframes). Try top-frame only.
    await chrome.scripting.executeScript({
      target: { tabId },
      files: [CONTENT_SCRIPT],
    });
  }
}

function sendToTab<T>(tabId: number, message: unknown): Promise<T> {
  return chrome.tabs.sendMessage(tabId, message) as Promise<T>;
}

async function snapshotTab(tabId: number): Promise<PageToolResult> {
  await ensureContentScript(tabId);
  const tab = await chrome.tabs.get(tabId);
  return runPageToolAllFrames(tab, 'page_snapshot', {});
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

  // For all other page tools, fan out to all frames (including cross-origin iframes).
  let result: PageToolResult;
  try {
    result = await runPageToolAllFrames(tab, name, args);
  } catch (err) {
    if (name === 'page_click') {
      const action = `Clicked in this tab; the page started navigating (${err instanceof Error ? err.message : 'frame gone'}).`;
      return settleThenSnapshot(tabId, tab.url, action);
    }
    throw err;
  }

  // CDP dispatch: for click/fill with a valid rect, use chrome.debugger for real input events.
  // This goes through Chrome's full input pipeline (pointer events, framework handlers, focus).
  if (
    (name === 'page_click' || name === 'page_fill') &&
    result.ok &&
    result.rect &&
    result.rect.width > 0 &&
    result.rect.height > 0
  ) {
    try {
      let cdpRect = result.rect;

      // Cross-origin iframe coordinate fix:
      // The content script's getElementTabRect() cannot traverse frameElement for cross-origin
      // iframes (SecurityError), so it returns only iframe-local coords. We must add the
      // iframe's offset (as seen from the top frame) to get tab-absolute coordinates.
      const winningFrameId = result.frameId ?? 0;
      if (winningFrameId !== 0) {
        try {
          // Find the iframe element in the top frame whose src URL matches the winning frame.
          const frames = await chrome.webNavigation.getAllFrames({ tabId });
          const winningFrame = (frames ?? []).find((f) => f.frameId === winningFrameId);
          const frameUrl = winningFrame?.url ?? '';

          if (frameUrl) {
            const iframeOffsets = await chrome.scripting.executeScript({
              target: { tabId, frameIds: [0] },
              func: (targetUrl: string) => {
                // Find the iframe in the top document whose src matches the target frame URL.
                // We compare by origin+pathname to handle query-param differences.
                const iframes = Array.from(document.querySelectorAll('iframe'));
                let bestMatch: { x: number; y: number } | null = null;
                let bestScore = 0;
                for (const iframe of iframes) {
                  const src = (iframe as HTMLIFrameElement).src;
                  if (!src) continue;
                  const r = (iframe as HTMLIFrameElement).getBoundingClientRect();
                  if (r.width === 0 && r.height === 0) continue;
                  // Score the match: exact > origin+path > origin only
                  let score = 0;
                  try {
                    const srcU = new URL(src);
                    const tgtU = new URL(targetUrl);
                    if (src === targetUrl) {
                      score = 3;
                    } else if (srcU.origin === tgtU.origin && srcU.pathname === tgtU.pathname) {
                      score = 2;
                    } else if (srcU.origin === tgtU.origin) {
                      score = 1;
                    }
                  } catch {
                    if (targetUrl.startsWith(src.replace(/\/$/, ''))) score = 1;
                  }
                  if (score > bestScore) {
                    bestScore = score;
                    bestMatch = { x: r.x, y: r.y };
                  }
                }
                return bestMatch;
              },
              args: [frameUrl],
            });
            const offset = iframeOffsets?.[0]?.result as { x: number; y: number } | null;
            if (offset) {
              cdpRect = {
                x: cdpRect.x + offset.x,
                y: cdpRect.y + offset.y,
                width: cdpRect.width,
                height: cdpRect.height,
              };
            }
          }
        } catch {
          // If we can't get the iframe offset, proceed with existing coords (best effort).
        }
      }

      if (name === 'page_click') {
        await cdpClick(tabId, cdpRect);
      } else {
        const text = typeof args.text === 'string' ? args.text : '';
        await cdpFill(tabId, cdpRect, text);
      }
    } catch {
      // CDP unavailable (policy-blocked, DevTools open, etc.).
      // For page_click: the content script did NOT call .click() (non-zero rect path),
      // so no DOM fallback ran — the click silently failed via CDP only.
      // For page_fill: the DOM .value= and dispatchEditorEvents already ran in the
      // content script (belt-and-suspenders), so the fill is not lost.
    }
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

// Handle tab ID requests from the side panel.
// sender.tab is undefined for side panels (they are extension pages, not content scripts),
// so we query the active tab in the last focused window — same as activeTab() used for
// page tools. This is reliable because the user must be looking at a tab to have its panel open.
chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
  if (message?.type === MSG_GET_TAB_ID) {
    void chrome.tabs.query({ active: true, lastFocusedWindow: true }).then(([tab]) => {
      sendResponse({ tabId: tab?.id ?? 0 });
    });
    return true;
  }
  return false;
});

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
