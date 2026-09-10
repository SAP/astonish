/** Resolve a user/model URL to http(s). Relative paths use the current tab as base. */
export function resolveHttpUrl(raw: unknown, base?: string): string | null {
  if (typeof raw !== 'string' || !raw.trim()) {
    return null;
  }
  try {
    const parsed = new URL(raw.trim(), base);
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
      return null;
    }
    return parsed.href;
  } catch {
    return null;
  }
}

/** True when page_navigate would reload the tab the user is already on. */
export function sameDocumentNavigation(from: string | undefined, to: string): boolean {
  if (!from) {
    return false;
  }
  return urlsLooselyMatch(from, to);
}

/** Compare URLs ignoring trailing slashes and empty hashes. */
export function urlsLooselyMatch(a: string, b: string): boolean {
  try {
    const left = new URL(a);
    const right = new URL(b);
    const path = (p: string) => (p.endsWith('/') && p.length > 1 ? p.slice(0, -1) : p);
    return (
      left.origin === right.origin &&
      path(left.pathname) === path(right.pathname) &&
      left.search === right.search
    );
  } catch {
    return a === b;
  }
}

/**
 * True when a click target has an http(s) href that leaves this document.
 * Hash-only / same-path links stay on the page — no full navigation wait.
 */
export function clickImpliesNavigation(href: string | undefined, currentUrl?: string): boolean {
  const resolved = resolveHttpUrl(href, currentUrl);
  if (!resolved) {
    return false;
  }
  if (!currentUrl) {
    return true;
  }
  try {
    const next = new URL(resolved);
    const cur = new URL(currentUrl);
    const path = (p: string) => (p.endsWith('/') && p.length > 1 ? p.slice(0, -1) : p);
    if (
      next.origin === cur.origin &&
      path(next.pathname) === path(cur.pathname) &&
      next.search === cur.search
    ) {
      return false;
    }
  } catch {
    return true;
  }
  return true;
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => {
    setTimeout(resolve, ms);
  });
}

export type TabWaiter = {
  get: (id: number) => Promise<{ url?: string; status?: string }>;
  onUpdated: {
    addListener: (listener: (id: number, info: { status?: string; url?: string }) => void) => void;
    removeListener: (listener: (id: number, info: { status?: string; url?: string }) => void) => void;
  };
};

export type NavigationWaitOptions = {
  probeMs?: number;
  spaMs?: number;
  loadTimeoutMs?: number;
  settleMs?: number;
};

function defaultTabWaiter(): TabWaiter | undefined {
  if (typeof chrome === 'undefined' || !chrome.tabs?.get || !chrome.tabs.onUpdated) {
    return undefined;
  }
  return chrome.tabs;
}

/**
 * After page_click / page_navigate, wait until this tab finishes loading
 * (or a short SPA beat if it never left complete).
 * No-ops in unit tests when chrome.tabs is undefined.
 */
export async function waitForPossibleNavigation(
  tabId: number,
  fromUrl: string | undefined,
  tabs: TabWaiter | undefined = defaultTabWaiter(),
  options: NavigationWaitOptions = {},
): Promise<void> {
  if (!tabs) {
    return;
  }
  const probeMs = options.probeMs ?? 800;
  const spaMs = options.spaMs ?? 200;
  const loadTimeoutMs = options.loadTimeoutMs ?? 12_000;
  const settleMs = options.settleMs ?? 300;
  let loading = false;
  let completed = false;
  const onUpdated = (id: number, info: { status?: string; url?: string }) => {
    if (id !== tabId) {
      return;
    }
    if (info.status === 'loading') {
      loading = true;
    }
    if (info.status === 'complete') {
      completed = true;
    }
  };
  tabs.onUpdated.addListener(onUpdated);
  try {
    const probeUntil = Date.now() + probeMs;
    while (Date.now() < probeUntil) {
      const tab = await tabs.get(tabId).catch(() => undefined);
      const urlChanged = !!(fromUrl && tab?.url && !urlsLooselyMatch(tab.url, fromUrl));
      if (loading || urlChanged) {
        break;
      }
      await delay(Math.min(50, probeMs));
    }
    const tab = await tabs.get(tabId).catch(() => undefined);
    const urlChanged = !!(fromUrl && tab?.url && !urlsLooselyMatch(tab.url, fromUrl));
    if (!loading && !urlChanged) {
      await delay(spaMs);
      return;
    }
    const deadline = Date.now() + loadTimeoutMs;
    while (Date.now() < deadline) {
      const current = await tabs.get(tabId).catch(() => undefined);
      if (current?.status === 'complete' && (completed || loading || urlChanged)) {
        await delay(settleMs);
        return;
      }
      await delay(Math.min(100, loadTimeoutMs));
    }
  } finally {
    tabs.onUpdated.removeListener(onUpdated);
  }
}

/** Combine a click/navigate action with a fresh in-tab snapshot. */
export function formatAfterNavigation(actionLine: string, snapshot: string): string {
  return [
    actionLine,
    '',
    'The tab is still THIS Chrome tab (same side panel session). Read the snapshot below. Do not call web_fetch, http_request, or sandbox browser tools for this URL unless the user explicitly asked to fetch it on the backend.',
    '',
    '--- page_snapshot ---',
    snapshot,
  ].join('\n');
}
