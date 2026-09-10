export type ExtensionSessionRef = {
  id: string;
  title: string;
};

export type ExtensionChatState = {
  currentSessionId: string;
  sessions: ExtensionSessionRef[];
};

// Use a single key in session storage (automatically per-tab-isolated by Chrome).
// chrome.storage.session is cleared when the tab closes, so each tab starts fresh.
const SESSION_STORAGE_KEY = 'astonishExtensionChatState';
const LEGACY_STORAGE_KEY = 'astonishExtensionChat';

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

async function loadSessionState(): Promise<ExtensionChatState> {
  if (typeof chrome === 'undefined' || !chrome.storage?.session) {
    return emptyState();
  }
  const result = await chrome.storage.session.get(SESSION_STORAGE_KEY);
  const raw = result[SESSION_STORAGE_KEY];
  if (raw) {
    return asState(raw);
  }
  // No session state yet. Check for legacy local storage (one-time migration).
  if (!chrome.storage?.local) {
    return emptyState();
  }
  const legacyResult = await chrome.storage.local.get(LEGACY_STORAGE_KEY);
  const legacy = legacyResult[LEGACY_STORAGE_KEY];
  if (legacy) {
    const migrated = asState(legacy);
    // Save to session storage (per-tab) and remove legacy
    await chrome.storage.session.set({ [SESSION_STORAGE_KEY]: migrated });
    await chrome.storage.local.remove(LEGACY_STORAGE_KEY);
    return migrated;
  }
  return emptyState();
}

async function saveSessionState(state: ExtensionChatState): Promise<void> {
  if (typeof chrome === 'undefined' || !chrome.storage?.session) {
    throw new Error('chrome.storage.session is not available');
  }
  await chrome.storage.session.set({ [SESSION_STORAGE_KEY]: state });
}

export async function loadExtensionChat(tabId: number): Promise<ExtensionChatState> {
  // Note: tabId parameter kept for API compatibility, but chrome.storage.session
  // is automatically per-tab-isolated by Chrome, so we don't need to manually key by tabId.
  return loadSessionState();
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
  await saveSessionState(saved);
  return saved;
}

export async function setCurrentSessionId(
  tabId: number,
  id: string,
): Promise<ExtensionChatState> {
  const state = await loadExtensionChat(tabId);
  const saved: ExtensionChatState = { ...state, currentSessionId: id.trim() };
  await saveSessionState(saved);
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
  await saveSessionState(saved);
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
  await saveSessionState(saved);
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
  await saveSessionState(saved);
  return saved;
}

export async function clearExtensionChat(): Promise<void> {
  if (typeof chrome === 'undefined' || !chrome.storage?.local) {
    return;
  }
  // Clear legacy local storage (session storage is auto-cleared per tab)
  await chrome.storage.local.remove(LEGACY_STORAGE_KEY);
}

export async function removeTabState(tabId: number): Promise<void> {
  // With session storage, no cleanup needed — it auto-clears when the tab closes.
  // This function kept for API compatibility but is a no-op.
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
