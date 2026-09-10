export type ExtensionSessionRef = {
  id: string;
  title: string;
};

export type ExtensionChatState = {
  currentSessionId: string;
  sessions: ExtensionSessionRef[];
};

const STORAGE_KEY = 'astonishExtensionChat';

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

async function saveState(state: ExtensionChatState): Promise<void> {
  if (typeof chrome === 'undefined' || !chrome.storage?.local) {
    throw new Error('chrome.storage is not available');
  }
  await chrome.storage.local.set({ [STORAGE_KEY]: state });
}

export async function loadExtensionChat(): Promise<ExtensionChatState> {
  if (typeof chrome === 'undefined' || !chrome.storage?.local) {
    return emptyState();
  }
  const result = await chrome.storage.local.get(STORAGE_KEY);
  return asState(result[STORAGE_KEY]);
}

export async function rememberExtensionSession(id: string, title?: string): Promise<ExtensionChatState> {
  const trimmed = id.trim();
  if (!trimmed) {
    return loadExtensionChat();
  }
  const state = await loadExtensionChat();
  const existing = state.sessions.find((session) => session.id === trimmed);
  const next: ExtensionSessionRef = {
    id: trimmed,
    title: (title && title.trim()) || existing?.title || 'New chat',
  };
  const sessions = [next, ...state.sessions.filter((session) => session.id !== trimmed)];
  const saved: ExtensionChatState = { currentSessionId: trimmed, sessions };
  await saveState(saved);
  return saved;
}

export async function setCurrentSessionId(id: string): Promise<ExtensionChatState> {
  const state = await loadExtensionChat();
  const saved: ExtensionChatState = { ...state, currentSessionId: id.trim() };
  await saveState(saved);
  return saved;
}

export async function updateExtensionSessionTitle(id: string, title: string): Promise<ExtensionChatState> {
  const trimmedTitle = title.trim();
  if (!id || !trimmedTitle) {
    return loadExtensionChat();
  }
  const state = await loadExtensionChat();
  const sessions = state.sessions.map((session) =>
    session.id === id ? { ...session, title: trimmedTitle } : session,
  );
  const saved: ExtensionChatState = { ...state, sessions };
  await saveState(saved);
  return saved;
}

export async function startNewExtensionSession(): Promise<ExtensionChatState> {
  return setCurrentSessionId('');
}

export async function replaceExtensionSessions(
  sessions: ExtensionSessionRef[],
  currentSessionId: string,
): Promise<ExtensionChatState> {
  const saved: ExtensionChatState = { currentSessionId, sessions };
  await saveState(saved);
  return saved;
}

export async function deleteExtensionSession(id: string): Promise<ExtensionChatState> {
  const trimmed = id.trim();
  if (!trimmed) {
    return loadExtensionChat();
  }
  const state = await loadExtensionChat();
  const sessions = state.sessions.filter((session) => session.id !== trimmed);
  let currentSessionId = state.currentSessionId;
  // If we deleted the current session, switch to the first remaining one or empty string
  if (currentSessionId === trimmed) {
    currentSessionId = sessions.length > 0 ? sessions[0].id : '';
  }
  const saved: ExtensionChatState = { currentSessionId, sessions };
  await saveState(saved);
  return saved;
}

export async function clearExtensionChat(): Promise<void> {
  if (typeof chrome === 'undefined' || !chrome.storage?.local) {
    return;
  }
  await chrome.storage.local.remove(STORAGE_KEY);
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
