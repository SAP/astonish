import { describe, expect, it, vi } from 'vitest';
import {
  clickImpliesNavigation,
  formatAfterNavigation,
  resolveHttpUrl,
  sameDocumentNavigation,
  urlsLooselyMatch,
  waitForPossibleNavigation,
  type TabWaiter,
} from '../page-navigate';

describe('page-navigate', () => {
  it('resolveHttpUrl accepts http(s) and relative paths', () => {
    expect(resolveHttpUrl('https://cnn.com/us')).toBe('https://cnn.com/us');
    expect(resolveHttpUrl('/world', 'https://cnn.com/us')).toBe('https://cnn.com/world');
    expect(resolveHttpUrl('chrome://extensions')).toBeNull();
    expect(resolveHttpUrl('')).toBeNull();
    expect(resolveHttpUrl(12)).toBeNull();
  });

  it('urlsLooselyMatch ignores trailing slashes', () => {
    expect(urlsLooselyMatch('https://cnn.com/us', 'https://cnn.com/us/')).toBe(true);
    expect(urlsLooselyMatch('https://cnn.com/us', 'https://cnn.com/world')).toBe(false);
  });

  it('sameDocumentNavigation is true when page_navigate would reload this tab', () => {
    expect(
      sameDocumentNavigation(
        'https://github.com/acme/app/issues/12',
        'https://github.com/acme/app/issues/12',
      ),
    ).toBe(true);
    expect(
      sameDocumentNavigation(
        'https://github.com/acme/app/issues/12',
        'https://github.com/acme/app/issues/12/',
      ),
    ).toBe(true);
    expect(
      sameDocumentNavigation(
        'https://github.com/acme/app/issues/12#top',
        'https://github.com/acme/app/issues/12',
      ),
    ).toBe(true);
    expect(
      sameDocumentNavigation(
        'https://github.com/acme/app/issues/12',
        'https://github.com/acme/app/issues/13',
      ),
    ).toBe(false);
    expect(sameDocumentNavigation(undefined, 'https://github.com/acme/app/issues/12')).toBe(false);
  });

  it('clickImpliesNavigation is true only for http(s) hrefs that leave the document', () => {
    expect(clickImpliesNavigation('https://cnn.com/us')).toBe(true);
    expect(clickImpliesNavigation('/us', 'https://cnn.com/')).toBe(true);
    expect(clickImpliesNavigation('/world', 'https://cnn.com/us')).toBe(true);
    expect(clickImpliesNavigation(undefined)).toBe(false);
    expect(clickImpliesNavigation('javascript:void(0)')).toBe(false);
    expect(clickImpliesNavigation('#section', 'https://cnn.com/us')).toBe(false);
    expect(clickImpliesNavigation('https://cnn.com/us#top', 'https://cnn.com/us')).toBe(false);
    expect(clickImpliesNavigation('https://cnn.com/us/', 'https://cnn.com/us')).toBe(false);
  });

  it('formatAfterNavigation tells the model to stay in this tab', () => {
    const text = formatAfterNavigation('Clicked ref1 href=https://cnn.com/us.', 'URL: https://cnn.com/us\nTitle: US');
    expect(text).toContain('Clicked ref1');
    expect(text).toContain('THIS Chrome tab');
    expect(text).toContain('Do not call web_fetch');
    expect(text).toContain('--- page_snapshot ---');
    expect(text).toContain('URL: https://cnn.com/us');
  });

  it('waitForPossibleNavigation no-ops when chrome.tabs is missing', async () => {
    await expect(waitForPossibleNavigation(1, 'https://cnn.com/us', undefined)).resolves.toBeUndefined();
  });

  it('waitForPossibleNavigation returns after a SPA beat when the URL never changes', async () => {
    const listeners: Array<(id: number, info: { status?: string }) => void> = [];
    const tabs: TabWaiter = {
      get: vi.fn(async () => ({ url: 'https://cnn.com/us', status: 'complete' })),
      onUpdated: {
        addListener: (listener) => {
          listeners.push(listener);
        },
        removeListener: (listener) => {
          const idx = listeners.indexOf(listener);
          if (idx >= 0) {
            listeners.splice(idx, 1);
          }
        },
      },
    };
    await waitForPossibleNavigation(7, 'https://cnn.com/us', tabs, {
      probeMs: 20,
      spaMs: 5,
      loadTimeoutMs: 50,
      settleMs: 5,
    });
    expect(tabs.get).toHaveBeenCalled();
    expect(listeners).toHaveLength(0);
  });

  it('waitForPossibleNavigation waits for complete after a URL change', async () => {
    const listeners: Array<(id: number, info: { status?: string }) => void> = [];
    let url = 'https://cnn.com/us';
    let status = 'loading';
    const tabs: TabWaiter = {
      get: vi.fn(async () => ({ url, status })),
      onUpdated: {
        addListener: (listener) => {
          listeners.push(listener);
        },
        removeListener: (listener) => {
          const idx = listeners.indexOf(listener);
          if (idx >= 0) {
            listeners.splice(idx, 1);
          }
        },
      },
    };
    const pending = waitForPossibleNavigation(7, 'https://cnn.com/us', tabs, {
      probeMs: 200,
      spaMs: 5,
      loadTimeoutMs: 400,
      settleMs: 5,
    });
    url = 'https://cnn.com/world';
    listeners[0]?.(7, { status: 'loading' });
    await new Promise((resolve) => setTimeout(resolve, 30));
    status = 'complete';
    listeners[0]?.(7, { status: 'complete' });
    await pending;
    expect(listeners).toHaveLength(0);
  });
});
