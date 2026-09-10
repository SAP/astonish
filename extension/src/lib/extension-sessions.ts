export type ExtensionSessionRef = {
  id: string;
  title: string;
};

export type ExtensionChatState = {
  currentSessionId: string;
  sessions: ExtensionSessionRef[];
};

// Per-tab map stored in session storage (in-memory, cleared on browser restart).
// Session storage is shared across all extension contexts, so we key by tabId.
// The tabId is embedded in the side panel URL by the service worker when it opens
// the panel, so it is always available synchronously from location.search.
const STORAGE_KEY = 'astonishExtensionChatByTab';
const LEGACY_STORAGE_KEY = 'astonishExtensionChat';

type TabStateMap = Record<number, ExtensionChatState>;

const emptyState = (): ExtensionChatState => ({
  currentSessionId: '',
  sessions: [],
});

function asState(value: unknown): ExtensionChatState {
  if (!value || typeof value !== 'object') {
    return emptyState();
  }
  const raw = value as Partial<ExtensionChatState>;
  const currentSessionId =
    typeof raw.currentSessionId === 'string' ? raw.currentSessionId : '';
  const sessions = Array.isArray(raw.sessions)
    ? raw.sessions
        .filter((item): item is ExtensionSessionRef => {
          return !!item && typeof item === 'object' && typeof item.id === 'string' && item.id !== '';
        })
        .map((item) => ({
          id: item.id,
          title: typeof item.title === 'string' && item.title.trim() ? item.title : 'New chat',
        }))
    : [];
  return { currentSessionId, sessions };
}

async function loadTabMap(): Promise<TabStateMap> {
  if (typeof chrome === 'undefined' || !chrome.storage?.session) {
    // Fall back to local storage if session is not available
    if (typeof chrome === 'undefined' || !chrome.storage?.local) return {};
    const result = await chrome.storage.local.get(STORAGE_KEY);
    const raw = result[STORAGE_KEY];
    return (!raw || typeof raw !== 'object') ? {} : raw as TabStateMap;
  }
  const result = await chrome.storage.session.get(STORAGE_KEY);
  const raw = result[STORAGE_KEY];
  return (!raw || typeof raw !== 'object') ? {} : raw as TabStateMap;
}

async function saveTabMap(map: TabStateMap): Promise<void> {
  if (typeof chrome === 'undefined') throw new Error('chrome is not available');
  if (chrome.storage?.session) {
    await chrome.storage.session.set({ [STORAGE_KEY]: map });
  } else if (chrome.storage?.local) {
    await chrome.storage.local.set({ [STORAGE_KEY]: map });
  }
}

async function saveTabState(tabId: number, state: ExtensionChatState): Promise<void> {
  const map = await loadTabMap();
  map[tabId] = state;
  await saveTabMap(map);
}

export async function loadExtensionChat(tabId: number): Promise<ExtensionChatState> {
  if (typeof chrome === 'undefined') return emptyState();

  const map = await loadTabMap();
  if (tabId in map) {
    return asState(map[tabId]);
  }

  // Migration: if legacy local key exists, move it to this tab and remove legacy.
  if (chrome.storage?.local) {
    const legacyResult = await chrome.storage.local.get(LEGACY_STORAGE_KEY);
    const legacy = legacyResult[LEGACY_STORAGE_KEY];
    if (legacy && typeof legacy === 'object') {
      const migrated = asState(legacy);
      const newMap: TabStateMap = { ...map, [tabId]: migrated };
      await saveTabMap(newMap);
      await chrome.storage.local.remove(LEGACY_STORAGE_KEY);
      return migrated;
    }
  }

  return emptyState();
}

export async function rememberExtensionSession(
  tabId: number,
  id: string,
  title?: string,
): Promise<ExtensionChatState> {
  const trimmed = id.trim();
  if (!trimmed) {
    return loadExtensionChat(tabId);
  }
  const state = await loadExtensionChat(tabId);
  const existing = state.sessions.find((session) => session.id === trimmed);
  const next: ExtensionSessionRef = {
    id: trimmed,
    title: (title && title.trim()) || existing?.title || 'New chat',
  };
  const sessions = [next, ...state.sessions.filter((session) => session.id !== trimmed)];
  const saved: ExtensionChatState = { currentSessionId: trimmed, sessions };
  await saveTabState(tabId, saved);
  return saved;
}

export async function setCurrentSessionId(
  tabId: number,
  id: string,
): Promise<ExtensionChatState> {
  const state = await loadExtensionChat(tabId);
  const saved: ExtensionChatState = { ...state, currentSessionId: id.trim() };
  await saveTabState(tabId, saved);
  return saved;
}

export async function updateExtensionSessionTitle(
  tabId: number,
  id: string,
  title: string,
): Promise<ExtensionChatState> {
  const trimmedTitle = title.trim();
  if (!id || !trimmedTitle) {
    return loadExtensionChat(tabId);
  }
  const state = await loadExtensionChat(tabId);
  const sessions = state.sessions.map((session) =>
    session.id === id ? { ...session, title: trimmedTitle } : session,
  );
  const saved: ExtensionChatState = { ...state, sessions };
  await saveTabState(tabId, saved);
  return saved;
}

export async function startNewExtensionSession(tabId: number): Promise<ExtensionChatState> {
  return setCurrentSessionId(tabId, '');
}

export async function replaceExtensionSessions(
  tabId: number,
  sessions: ExtensionSessionRef[],
  currentSessionId: string,
): Promise<ExtensionChatState> {
  const saved: ExtensionChatState = { currentSessionId, sessions };
  await saveTabState(tabId, saved);
  return saved;
}

export async function deleteExtensionSession(
  tabId: number,
  id: string,
): Promise<ExtensionChatState> {
  const trimmed = id.trim();
  if (!trimmed) {
    return loadExtensionChat(tabId);
  }
  const state = await loadExtensionChat(tabId);
  const sessions = state.sessions.filter((session) => session.id !== trimmed);
  let currentSessionId = state.currentSessionId;
  if (currentSessionId === trimmed) {
    currentSessionId = sessions.length > 0 ? sessions[0].id : '';
  }
  const saved: ExtensionChatState = { currentSessionId, sessions };
  await saveTabState(tabId, saved);
  return saved;
}

export async function clearExtensionChat(): Promise<void> {
  if (typeof chrome === 'undefined') return;
  if (chrome.storage?.session) {
    await chrome.storage.session.remove(STORAGE_KEY);
  }
  if (chrome.storage?.local) {
    await chrome.storage.local.remove([STORAGE_KEY, LEGACY_STORAGE_KEY]);
  }
}

export async function removeTabState(tabId: number): Promise<void> {
  if (typeof chrome === 'undefined') return;
  const map = await loadTabMap();
  if (!(tabId in map)) return;
  delete map[tabId];
  await saveTabMap(map);
}

export type ListedSession = {
  id: string;
  title?: string;
};

/** Keep only Studio sessions the extension created. Studio itself still lists all. */
export function filterExtensionSessions<T extends ListedSession>(
  all: T[],
  extensionIds: string[],
): T[] {
  const allowed = new Set(extensionIds);
  return all.filter((session) => allowed.has(session.id));
}

export function pruneMissingSessionIds(
  stored: ExtensionSessionRef[],
  listed: ListedSession[],
): ExtensionSessionRef[] {
  const present = new Set(listed.map((session) => session.id));
  return stored.filter((session) => present.has(session.id));
}

export function mergeSessionTitles(
  stored: ExtensionSessionRef[],
  listed: ListedSession[],
): ExtensionSessionRef[] {
  const titles = new Map(
    listed
      .filter((session) => typeof session.title === 'string' && session.title.trim())
      .map((session) => [session.id, session.title!.trim()]),
  );
  return stored.map((session) => ({
    id: session.id,
    title: titles.get(session.id) || session.title || 'New chat',
  }));
}
