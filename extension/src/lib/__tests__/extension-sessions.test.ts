import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  clearExtensionChat,
  filterExtensionSessions,
  loadExtensionChat,
  mergeSessionTitles,
  pruneMissingSessionIds,
  rememberExtensionSession,
  startNewExtensionSession,
  updateExtensionSessionTitle,
} from '../extension-sessions';

type Store = Record<string, unknown>;

function installChromeMock() {
  const local: Store = {};
  const session: Store = {};

  const area = (store: Store) => ({
    get: async (key: string | string[] | Record<string, unknown>) => {
      if (typeof key === 'string') {
        return { [key]: store[key] };
      }
      if (Array.isArray(key)) {
        const out: Store = {};
        for (const k of key) {
          out[k] = store[k];
        }
        return out;
      }
      const out: Store = { ...key };
      for (const k of Object.keys(key)) {
        if (k in store) {
          out[k] = store[k];
        }
      }
      return out;
    },
    set: async (value: Record<string, unknown>) => {
      Object.assign(store, value);
    },
    remove: async (key: string | string[]) => {
      for (const k of Array.isArray(key) ? key : [key]) {
        delete store[k];
      }
    },
  });

  (globalThis as unknown as { chrome: unknown }).chrome = {
    storage: {
      local: area(local),
      session: area(session),
    },
  };

  // Return the local store so tests can write legacy data directly.
  return { local };
}

describe('extension-sessions', () => {
  let chromeMock: ReturnType<typeof installChromeMock>;

  beforeEach(() => {
    chromeMock = installChromeMock();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('persists the current session and extension-created list', async () => {
    await rememberExtensionSession(42, 'ext-1', 'GitHub issue');
    await rememberExtensionSession(42, 'ext-2', 'Docs page');

    const state = await loadExtensionChat(42);
    expect(state.currentSessionId).toBe('ext-2');
    expect(state.sessions.map((s) => s.id)).toEqual(['ext-2', 'ext-1']);
    expect(state.sessions[0].title).toBe('Docs page');
  });

  it('startNewExtensionSession clears current id but keeps the list', async () => {
    await rememberExtensionSession(42, 'ext-1', 'First');
    const next = await startNewExtensionSession(42);
    expect(next.currentSessionId).toBe('');
    expect(next.sessions).toEqual([{ id: 'ext-1', title: 'First' }]);
  });

  it('filterExtensionSessions hides Studio-only chats', () => {
    const listed = [
      { id: 'studio-1', title: 'Studio chat' },
      { id: 'ext-1', title: 'Extension chat' },
      { id: 'ext-2', title: 'Another extension chat' },
    ];
    expect(filterExtensionSessions(listed, ['ext-2', 'ext-1'])).toEqual([
      { id: 'ext-1', title: 'Extension chat' },
      { id: 'ext-2', title: 'Another extension chat' },
    ]);
  });

  it('prunes deleted Studio sessions and merges titles', () => {
    const stored = [
      { id: 'ext-1', title: 'New chat' },
      { id: 'gone', title: 'Deleted' },
    ];
    const listed = [{ id: 'ext-1', title: 'GitHub issue #12' }];
    const pruned = pruneMissingSessionIds(stored, listed);
    expect(pruned).toEqual([{ id: 'ext-1', title: 'New chat' }]);
    expect(mergeSessionTitles(pruned, listed)).toEqual([{ id: 'ext-1', title: 'GitHub issue #12' }]);
  });

  it('updateExtensionSessionTitle rewrites a stored title', async () => {
    await rememberExtensionSession(42, 'ext-1');
    await updateExtensionSessionTitle(42, 'ext-1', 'Renamed');
    const state = await loadExtensionChat(42);
    expect(state.sessions[0].title).toBe('Renamed');
  });

  it('session storage is cleared per-tab automatically', async () => {
    // When using chrome.storage.session, each tab gets its own isolated storage
    // and it's auto-cleared when the tab closes. There's no explicit cleanup needed.
    await rememberExtensionSession(42, 'ext-1', 'First');
    let state = await loadExtensionChat(42);
    expect(state.currentSessionId).toBe('ext-1');
    
    // clearExtensionChat is now a no-op for session storage (auto-cleared per tab).
    // It only removes legacy local storage if present.
    await clearExtensionChat();
    // Session storage still has the data (it's per-tab and auto-cleared on tab close)
    state = await loadExtensionChat(42);
    expect(state.currentSessionId).toBe('ext-1');
  });

  it('migrates legacy global state to the session', async () => {
    // Manually write legacy format into the mock local store
    await (chrome.storage.local as any).set({
      astonishExtensionChat: {
        currentSessionId: 'old-1',
        sessions: [{ id: 'old-1', title: 'Legacy session' }],
      },
    });
    const state = await loadExtensionChat(55);
    expect(state.currentSessionId).toBe('old-1');
    expect(state.sessions[0].title).toBe('Legacy session');
    // Legacy key should be removed after migration
    const legacy = await (chrome.storage.local as any).get('astonishExtensionChat');
    expect(legacy.astonishExtensionChat).toBeUndefined();
  });
});
