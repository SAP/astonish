export type TabAccess =
  | { ok: true; tabId: number; url: string }
  | { ok: false; error: string };

const UNREADABLE = 'This page cannot be read';

/** Origins declared in manifest optional_host_permissions. */
export const OPTIONAL_TAB_ORIGINS = [
  'https://*/*',
  'http://localhost/*',
  'http://127.0.0.1/*',
  'https://localhost/*',
];

function originPattern(url: string): string | null {
  try {
    const parsed = new URL(url);
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
      return null;
    }
    return `${parsed.origin}/*`;
  } catch {
    return null;
  }
}

async function queryActiveTab(): Promise<chrome.tabs.Tab | undefined> {
  const [tab] = await chrome.tabs.query({ active: true, lastFocusedWindow: true });
  return tab;
}

/**
 * Request optional host permission from a user-gesture context (side panel
 * Send). Ask for the declared optional hosts first so Chrome still has the
 * gesture; tab query is async and would otherwise drop the prompt.
 */
export async function requestActiveTabHostPermission(): Promise<TabAccess> {
  if (typeof chrome === 'undefined' || !chrome.tabs?.query) {
    return { ok: false, error: UNREADABLE };
  }
  if (chrome.permissions?.request) {
    const granted = await chrome.permissions.request({ origins: OPTIONAL_TAB_ORIGINS });
    if (!granted) {
      const tab = await queryActiveTab();
      let host = 'this page';
      if (tab?.url) {
        try {
          host = new URL(tab.url).hostname;
        } catch {
          // keep fallback
        }
      }
      return { ok: false, error: `Allow access to ${host} to read this page` };
    }
  }
  const tab = await queryActiveTab();
  if (!tab?.id || !tab.url) {
    return { ok: false, error: UNREADABLE };
  }
  if (!originPattern(tab.url)) {
    return { ok: false, error: UNREADABLE };
  }
  return { ok: true, tabId: tab.id, url: tab.url };
}
