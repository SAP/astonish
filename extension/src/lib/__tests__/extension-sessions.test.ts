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
}

describe('extension-sessions', () => {
  beforeEach(() => {
    installChromeMock();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('persists the current session and extension-created list', async () => {
    await rememberExtensionSession('ext-1', 'GitHub issue');
    await rememberExtensionSession('ext-2', 'Docs page');

    const state = await loadExtensionChat();
    expect(state.currentSessionId).toBe('ext-2');
    expect(state.sessions.map((s) => s.id)).toEqual(['ext-2', 'ext-1']);
    expect(state.sessions[0].title).toBe('Docs page');
  });

  it('startNewExtensionSession clears current id but keeps the list', async () => {
    await rememberExtensionSession('ext-1', 'First');
    const next = await startNewExtensionSession();
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
    await rememberExtensionSession('ext-1');
    await updateExtensionSessionTitle('ext-1', 'Renamed');
    const state = await loadExtensionChat();
    expect(state.sessions[0].title).toBe('Renamed');
  });

  it('clearExtensionChat removes storage', async () => {
    await rememberExtensionSession('ext-1', 'First');
    await clearExtensionChat();
    const state = await loadExtensionChat();
    expect(state).toEqual({ currentSessionId: '', sessions: [] });
  });
});
