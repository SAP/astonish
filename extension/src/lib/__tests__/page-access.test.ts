import { afterEach, describe, expect, it, vi } from 'vitest';
import { OPTIONAL_TAB_ORIGINS, requestActiveTabHostPermission } from '../page-access';

describe('page-access', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('requests optional hosts then returns the active tab', async () => {
    const request = vi.fn(async () => true);
    vi.stubGlobal('chrome', {
      tabs: {
        query: vi.fn(async () => [{ id: 7, url: 'https://www.cnn.com/us' }]),
      },
      permissions: { request },
    });

    const access = await requestActiveTabHostPermission();
    expect(request).toHaveBeenCalledWith({ origins: OPTIONAL_TAB_ORIGINS });
    expect(access).toEqual({ ok: true, tabId: 7, url: 'https://www.cnn.com/us' });
  });

  it('explains when the user denies host access', async () => {
    vi.stubGlobal('chrome', {
      tabs: {
        query: vi.fn(async () => [{ id: 7, url: 'https://www.cnn.com/us' }]),
      },
      permissions: {
        request: vi.fn(async () => false),
      },
    });

    const access = await requestActiveTabHostPermission();
    expect(access.ok).toBe(false);
    if (!access.ok) {
      expect(access.error).toContain('www.cnn.com');
    }
  });
});
