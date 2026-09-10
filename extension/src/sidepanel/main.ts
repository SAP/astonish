import {
  connectChat,
  fetchSessionHistory,
  fetchSessions,
  historyMessageKind,
  sessionIdFromEvent,
  type HistoryMessage,
} from '../lib/astonish-client';
import {
  clearSession,
  listSSOProviders,
  loadSession,
  loginWithSSO,
  type SSOProvider,
  type SSOStatus,
} from '../lib/auth';
import {
  deleteExtensionSession,
  filterExtensionSessions,
  loadExtensionChat,
  mergeSessionTitles,
  pruneMissingSessionIds,
  rememberExtensionSession,
  replaceExtensionSessions,
  setCurrentSessionId,
  startNewExtensionSession,
  updateExtensionSessionTitle,
  type ExtensionChatState,
} from '../lib/extension-sessions';
import { renderMarkdown } from '../lib/markdown';
import {
  MSG_APPLY,
  MSG_GET_CONTEXT,
  MSG_PAGE_TOOL,
  type ApplyResult,
  type ContextResult,
  type PageToolResult,
} from '../lib/messages';
import { requestActiveTabHostPermission } from '../lib/page-access';
import {
  EXTENSION_PAGE_TOOLS_INSTRUCTIONS,
  buildSystemContext,
  type PageContext,
} from '../lib/page-context';
import { extractPageEdit } from '../lib/page-edit';
import {
  extractPageTools,
  formatPageToolResults,
  stripPageToolFences,
  type PageToolCall,
} from '../lib/page-tools';

const loginView = document.querySelector<HTMLElement>('#view-login');
const chatView = document.querySelector<HTMLElement>('#view-chat');
const loginForm = document.querySelector<HTMLFormElement>('#login-form');
const loginError = document.querySelector<HTMLElement>('#login-error');
const sendButton = document.querySelector<HTMLButtonElement>('#send');
const messageInput = document.querySelector<HTMLTextAreaElement>('#message');
const statusLine = document.querySelector<HTMLElement>('#status');
const transcript = document.querySelector<HTMLElement>('#transcript');
const serverUrlInput = document.querySelector<HTMLInputElement>('#server-url');
const connectButton = document.querySelector<HTMLButtonElement>('#connect');
const loginStatus = document.querySelector<HTMLElement>('#login-status');
const providerPicker = document.querySelector<HTMLElement>('#provider-picker');
const providerList = document.querySelector<HTMLElement>('#provider-list');
const pageMeta = document.querySelector<HTMLElement>('#page-meta');
const captureBanner = document.querySelector<HTMLElement>('#capture-banner');
const applyBar = document.querySelector<HTMLElement>('#apply-bar');
const applyPreview = document.querySelector<HTMLElement>('#apply-preview');
const applyMessage = document.querySelector<HTMLElement>('#apply-message');
const applyButton = document.querySelector<HTMLButtonElement>('#apply');
const dismissButton = document.querySelector<HTMLButtonElement>('#dismiss-apply');
const logoutButton = document.querySelector<HTMLButtonElement>('#logout');
const sessionPicker = document.querySelector<HTMLSelectElement>('#session-picker');
const deleteSessionButton = document.querySelector<HTMLButtonElement>('#delete-session');


let sessionId = '';
let studioUrl = '';
let streaming = false;
let pendingEdit: string | null = null;

const MAX_PAGE_TOOL_ROUNDS = 15;

function showError(message: string): void {
  if (!loginError) {
    return;
  }
  loginError.textContent = message;
  loginError.hidden = false;
}

function clearError(): void {
  if (!loginError) {
    return;
  }
  loginError.textContent = '';
  loginError.hidden = true;
}

function setLoggedIn(loggedIn: boolean): void {
  if (loginView) {
    loginView.hidden = loggedIn;
    loginView.classList.toggle('is-active', !loggedIn);
  }
  if (chatView) {
    chatView.hidden = !loggedIn;
    chatView.classList.toggle('is-active', loggedIn);
  }
  if (logoutButton) {
    logoutButton.hidden = !loggedIn;
  }
  if (sendButton) {
    sendButton.disabled = !loggedIn || streaming;
  }
  if (messageInput) {
    messageInput.disabled = !loggedIn;
  }
  if (statusLine && !streaming) {
    statusLine.textContent = loggedIn ? 'Connected' : 'Not connected';
  }
}

function scrollTranscript(): void {
  const scroller = transcript?.closest('.chat-scroll') ?? transcript;
  if (scroller) {
    scroller.scrollTop = scroller.scrollHeight;
  }
}

function appendMessage(kind: string): HTMLElement {
  const el = document.createElement('div');
  el.className = `msg msg-${kind}`;
  transcript?.appendChild(el);
  scrollTranscript();
  return el;
}

function appendUser(text: string): HTMLElement {
  const wrap = appendMessage('user');
  const label = document.createElement('div');
  label.className = 'msg-label';
  label.textContent = 'You';
  const bubble = document.createElement('div');
  bubble.className = 'chat-bubble-user';
  const body = document.createElement('p');
  body.textContent = text;
  bubble.appendChild(body);
  wrap.append(label, bubble);
  return wrap;
}

function appendAssistant(): { wrap: HTMLElement; body: HTMLElement } {
  const wrap = appendMessage('assistant');
  const label = document.createElement('div');
  label.className = 'msg-label';
  label.textContent = 'Agent';
  const bubble = document.createElement('div');
  bubble.className = 'chat-bubble-agent';
  const body = document.createElement('div');
  body.className = 'markdown-body';
  bubble.appendChild(body);
  wrap.append(label, bubble);
  return { wrap, body };
}

function setAssistantMarkdown(body: HTMLElement, markdown: string): void {
  body.innerHTML = renderMarkdown(markdown);
  scrollTranscript();
}

function appendNotice(kind: 'tool' | 'approval' | 'error', text: string, extra?: HTMLElement): HTMLElement {
  const el = appendMessage(kind);
  el.append(document.createTextNode(text));
  if (extra) {
    el.appendChild(extra);
  }
  return el;
}

function setPageMeta(ctx: PageContext | null, error?: string): void {
  if (!pageMeta) {
    return;
  }
  if (ctx) {
    pageMeta.hidden = false;
    pageMeta.textContent = `${ctx.title} — ${ctx.url}`;
    return;
  }
  if (error) {
    pageMeta.hidden = false;
    pageMeta.textContent = error;
    return;
  }
  pageMeta.hidden = true;
  pageMeta.textContent = '';
}

function showCaptureBanner(message: string | null): void {
  if (!captureBanner) {
    return;
  }
  if (!message) {
    captureBanner.hidden = true;
    captureBanner.textContent = '';
    return;
  }
  captureBanner.hidden = false;
  captureBanner.textContent = message;
}

function hideApplyBar(): void {
  pendingEdit = null;
  if (applyBar) {
    applyBar.hidden = true;
    applyBar.classList.remove('is-blocked');
  }
  if (applyPreview) {
    applyPreview.textContent = '';
  }
  if (applyMessage) {
    applyMessage.textContent = '';
    applyMessage.hidden = true;
  }
  if (applyButton) {
    applyButton.textContent = 'Apply';
  }
}

function renderSessionPicker(state: ExtensionChatState): void {
  if (!sessionPicker) {
    return;
  }
  sessionPicker.replaceChildren();
  const blank = document.createElement('option');
  blank.value = '';
  blank.textContent = 'New chat';
  sessionPicker.appendChild(blank);
  for (const session of state.sessions) {
    const option = document.createElement('option');
    option.value = session.id;
    option.textContent = session.title || 'New chat';
    sessionPicker.appendChild(option);
  }
  sessionPicker.value = state.currentSessionId;
}

function renderHistory(messages: HistoryMessage[]): void {
  transcript?.replaceChildren();
  hideApplyBar();
  for (const message of messages) {
    const kind = historyMessageKind(message);
    const content = typeof message.content === 'string' ? message.content : '';
    if (kind === 'user') {
      if (content) {
        appendUser(content);
      }
      continue;
    }
    if (kind === 'agent' || kind === 'assistant') {
      if (content) {
        const { body } = appendAssistant();
        setAssistantMarkdown(body, stripPageToolFences(content));
      }
      continue;
    }
    if (kind === 'tool_call') {
      appendNotice('tool', `Studio tool: ${message.toolName || 'tool'}`);
      continue;
    }
    if (kind === 'tool_result') {
      appendNotice('tool', 'Studio tool result');
    }
  }
  scrollTranscript();
}

async function refreshExtensionSessions(): Promise<ExtensionChatState> {
  const stored = await loadExtensionChat();
  try {
    const listed = await fetchSessions();
    const extensionOnly = filterExtensionSessions(
      listed,
      stored.sessions.map((session) => session.id),
    );
    const pruned = pruneMissingSessionIds(stored.sessions, extensionOnly);
    const merged = mergeSessionTitles(pruned, extensionOnly);
    const currentStillThere =
      !stored.currentSessionId || merged.some((session) => session.id === stored.currentSessionId);
    const saved = await replaceExtensionSessions(
      merged,
      currentStillThere ? stored.currentSessionId : '',
    );
    renderSessionPicker(saved);
    return saved;
  } catch {
    renderSessionPicker(stored);
    return stored;
  }
}

async function restoreExtensionChat(): Promise<void> {
  const state = await refreshExtensionSessions();
  sessionId = state.currentSessionId;
  if (!sessionId) {
    transcript?.replaceChildren();
    hideApplyBar();
    return;
  }
  try {
    const history = await fetchSessionHistory(sessionId);
    renderHistory(history.messages);
    if (history.title) {
      const next = await updateExtensionSessionTitle(sessionId, history.title);
      renderSessionPicker(next);
    }
  } catch (err) {
    appendNotice('error', err instanceof Error ? err.message : 'Failed to load session');
  }
}

async function beginNewChat(): Promise<void> {
  if (streaming) {
    return;
  }
  const state = await startNewExtensionSession();
  sessionId = '';
  transcript?.replaceChildren();
  hideApplyBar();
  renderSessionPicker(state);
}

async function selectStoredSession(id: string): Promise<void> {
  if (streaming) {
    return;
  }
  if (!id) {
    await beginNewChat();
    return;
  }
  sessionId = id;
  await setCurrentSessionId(id);
  try {
    const history = await fetchSessionHistory(id);
    renderHistory(history.messages);
    const title = history.title || id;
    const state = await rememberExtensionSession(id, title);
    renderSessionPicker(state);
  } catch (err) {
    transcript?.replaceChildren();
    appendNotice('error', err instanceof Error ? err.message : 'Failed to load session');
  }
}

function persistSessionId(id: string, title?: string): void {
  if (!id) {
    return;
  }
  sessionId = id;
  void rememberExtensionSession(id, title).then(renderSessionPicker);
}

function showApplyBar(text: string): void {
  pendingEdit = text;
  if (applyPreview) {
    applyPreview.textContent = text.length > 400 ? `${text.slice(0, 400)}…` : text;
  }
  if (applyMessage) {
    applyMessage.textContent = '';
    applyMessage.hidden = true;
  }
  if (applyButton) {
    applyButton.textContent = 'Apply';
  }
  if (applyBar) {
    applyBar.classList.remove('is-blocked');
    applyBar.hidden = false;
  }
}

function showApplyBlocked(message: string): void {
  // Keep pendingEdit unchanged so the proposed content stays pinned.
  if (applyMessage) {
    applyMessage.textContent = message;
    applyMessage.hidden = false;
  } else if (statusLine) {
    // Defensive fallback when the message element is missing.
    statusLine.textContent = message;
  }
  if (applyButton) {
    applyButton.textContent = 'Apply again';
  }
  if (applyBar) {
    applyBar.classList.add('is-blocked');
    applyBar.hidden = false;
  }
  applyButton?.focus();
}

async function requestContext(): Promise<ContextResult> {
  return chrome.runtime.sendMessage({ type: MSG_GET_CONTEXT }) as Promise<ContextResult>;
}

async function runPageToolCalls(calls: PageToolCall[]): Promise<PageToolResult[]> {
  const results: PageToolResult[] = [];
  for (const call of calls) {
    try {
      const result = (await chrome.runtime.sendMessage({
        type: MSG_PAGE_TOOL,
        name: call.name,
        args: call.args,
      })) as PageToolResult;
      const entry: PageToolResult = {
        ok: !!result?.ok,
        name: result?.name || call.name,
        result: result?.result,
        error: result?.error || (!result?.ok ? result?.result : undefined),
      };
      results.push(entry);
      appendNotice(
        'tool',
        entry.ok ? `Page tool: ${entry.name}` : `Page tool failed: ${entry.name}${entry.error ? ` — ${entry.error}` : ''}`,
      );
    } catch (err) {
      const error = err instanceof Error ? err.message : String(err);
      results.push({ ok: false, name: call.name, error });
      appendNotice('tool', `Page tool failed: ${call.name} — ${error}`);
    }
  }
  return results;
}

function streamOnce(params: {
  message: string;
  systemContext?: string;
  onText: (full: string) => void;
}): Promise<{ text: string; error?: string }> {
  return new Promise((resolve) => {
    let assistantText = '';
    connectChat({
      sessionId,
      message: params.message,
      systemContext: params.systemContext,
      autoApprove: false,
      onEvent: (type, data) => {
        if (type === 'session') {
          const id = sessionIdFromEvent(data);
          if (id) {
            persistSessionId(id);
          }
        }
        if (type === 'session_title') {
          const title = typeof data.title === 'string' ? data.title.trim() : '';
          const id = sessionIdFromEvent(data) || sessionId;
          if (id) {
            persistSessionId(id, title || undefined);
          }
        }
        if (type === 'text') {
          const chunk = typeof data.text === 'string' ? data.text : '';
          assistantText += chunk;
          params.onText(assistantText);
        }
        if (type === 'tool_call') {
          const name = typeof data.name === 'string' ? data.name : 'tool';
          appendNotice('tool', `Studio tool: ${name}`);
        }
        if (type === 'tool_result') {
          appendNotice('tool', 'Studio tool result');
        }
        if (type === 'approval') {
          const notice = document.createElement('a');
          notice.href = studioUrl || '#';
          notice.target = '_blank';
          notice.rel = 'noreferrer';
          notice.textContent = 'Finish this in Studio';
          appendNotice('approval', 'Approval needed. ', notice);
        }
        if (type === 'error' || type === 'error_info') {
          const message =
            (typeof data.message === 'string' && data.message) ||
            (typeof data.error === 'string' && data.error) ||
            'Chat error';
          appendNotice('error', message);
        }
      },
      onError: (err) => {
        appendNotice('error', err.message);
        resolve({ text: assistantText, error: err.message });
      },
      onDone: () => {
        resolve({ text: assistantText });
      },
    });
  });
}

function finishStreaming(status?: string): void {
  streaming = false;
  setLoggedIn(true);
  if (sessionPicker) {
    sessionPicker.disabled = false;
  }
  if (deleteSessionButton) {
    deleteSessionButton.disabled = false;
  }
  if (statusLine && status) {
    statusLine.textContent = status;
  }
}

function setLoginStatus(message: string | null): void {
  if (!loginStatus) {
    return;
  }
  if (!message) {
    loginStatus.hidden = true;
    loginStatus.textContent = '';
    return;
  }
  loginStatus.hidden = false;
  loginStatus.textContent = message;
}

function hideProviderPicker(): void {
  if (providerPicker) {
    providerPicker.hidden = true;
  }
  if (providerList) {
    providerList.replaceChildren();
  }
}

function ssoStatusMessage(status: SSOStatus, verifyUrl?: string): string {
  switch (status) {
    case 'opening_browser':
      return 'Opening browser for authentication…';
    case 'browser_failed':
      return verifyUrl
        ? `Could not open the browser. Open ${verifyUrl}`
        : 'Could not open the browser. Use the verify URL from Studio.';
    case 'polling':
      return 'Waiting for authentication to complete in the browser…';
    default:
      return 'Signing in…';
  }
}

async function completeSSO(serverUrl: string, providerID = ''): Promise<void> {
  if (connectButton) {
    connectButton.disabled = true;
  }
  const session = await loginWithSSO(serverUrl, providerID, (status, verifyUrl) => {
    setLoginStatus(ssoStatusMessage(status, verifyUrl));
  });
  studioUrl = session.serverUrl;
  setLoggedIn(true);
  setLoginStatus(null);
  hideProviderPicker();
  if (connectButton) {
    connectButton.disabled = false;
  }
  await restoreExtensionChat();
}

function showProviderPicker(serverUrl: string, providers: SSOProvider[]): void {
  if (!providerPicker || !providerList) {
    return;
  }
  providerList.replaceChildren();
  for (const provider of providers) {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'secondary';
    button.textContent = provider.name || provider.id;
    button.addEventListener('click', () => {
      clearError();
      void completeSSO(serverUrl, provider.id).catch((err) => {
        showError(err instanceof Error ? err.message : String(err));
        if (connectButton) {
          connectButton.disabled = false;
        }
      });
    });
    providerList.appendChild(button);
  }
  providerPicker.hidden = false;
}

function resizeComposer(): void {
  if (!messageInput) {
    return;
  }
  messageInput.style.height = 'auto';
  messageInput.style.height = `${Math.min(messageInput.scrollHeight, 128)}px`;
}

function sendCurrentMessage(): void {
  const text = messageInput?.value.trim() || '';
  if (!text || streaming) {
    return;
  }
  if (messageInput) {
    messageInput.value = '';
    resizeComposer();
  }
  appendUser(text);
  streaming = true;
  hideApplyBar();
  if (sendButton) {
    sendButton.disabled = true;
  }
  if (sessionPicker) {
    sessionPicker.disabled = true;
  }
  if (deleteSessionButton) {
    deleteSessionButton.disabled = true;
  }
  if (statusLine) {
    statusLine.textContent = 'Thinking…';
  }

  void (async () => {
    const access = await requestActiveTabHostPermission();
    let systemContext = EXTENSION_PAGE_TOOLS_INSTRUCTIONS;
    if (!access.ok) {
      setPageMeta(null, access.error);
      showCaptureBanner(`${access.error}. Sending without page context.`);
    } else {
      try {
        const captured = await requestContext();
        if (captured.ok && captured.context) {
          systemContext = buildSystemContext(captured.context);
          setPageMeta(captured.context);
          showCaptureBanner(null);
          if (captured.context.adapter === 'github' && !captured.context.editorPresent) {
            skipPageTools = true;
          }
        } else {
          const error = captured.error || 'This page cannot be read';
          setPageMeta(null, error);
          showCaptureBanner('Sending without page context.');
        }
      } catch (err) {
        setPageMeta(null, err instanceof Error ? err.message : 'This page cannot be read');
        showCaptureBanner('Sending without page context.');
      }
    }

    let message = text;
    let context: string | undefined = systemContext;
    let lastAssistant = '';
    // When the page is a read-only GitHub issue/PR (no open description editor),
    // page-tool calls are pointless — the model should just emit a fence. Skip
    // the tool loop entirely to prevent the model from kebab-hunting in circles.
    let skipPageTools = false;

    for (let round = 0; round < MAX_PAGE_TOOL_ROUNDS; round += 1) {
      let assistantBody: HTMLElement | null = null;
      const { text: assistantText, error } = await streamOnce({
        message,
        systemContext: context,
        onText: (full) => {
          lastAssistant = full;
          if (!assistantBody) {
            assistantBody = appendAssistant().body;
          }
          const visible = stripPageToolFences(full);
          setAssistantMarkdown(assistantBody, visible || '_Using page tools…_');
        },
      });
      lastAssistant = assistantText;
      if (error) {
        finishStreaming(error);
        return;
      }

      const calls = extractPageTools(assistantText);
      if (!calls.length || skipPageTools) {
        const edit = extractPageEdit(assistantText);
        if (edit !== null) {
          showApplyBar(edit);
        }
        finishStreaming();
        return;
      }

      if (statusLine) {
        statusLine.textContent = 'Using page tools…';
      }
      const results = await runPageToolCalls(calls);
      message =
        'The Chrome extension ran those page tools in THIS browser tab. Use the results below. Stay in this tab: page_snapshot / page_click / page_navigate. Do not call web_fetch, http_request, browser_snapshot, browser_navigate, or search_tools unless the user explicitly asked for a backend fetch.';
      context = `${formatPageToolResults(results)}\n\n${EXTENSION_PAGE_TOOLS_INSTRUCTIONS}`;
    }

    appendNotice('error', 'Stopped after too many page-tool rounds.');
    const edit = extractPageEdit(lastAssistant);
    if (edit !== null) {
      showApplyBar(edit);
    }
    finishStreaming('Stopped after too many page-tool rounds.');
  })();
}

loginForm?.addEventListener('submit', async (event) => {
  event.preventDefault();
  clearError();
  hideProviderPicker();
  const serverUrl = serverUrlInput?.value.trim() || '';
  if (!serverUrl) {
    showError('Studio URL is required.');
    return;
  }
  try {
    setLoginStatus('Looking up SSO providers…');
    if (connectButton) {
      connectButton.disabled = true;
    }
    const providers = await listSSOProviders(serverUrl);
    if (providers.length === 0) {
      throw new Error('no SSO providers configured on this server');
    }
    if (providers.length === 1) {
      setLoginStatus(`Using SSO provider: ${providers[0].name}`);
      await completeSSO(serverUrl, providers[0].id);
      return;
    }
    setLoginStatus('Select an SSO provider.');
    showProviderPicker(serverUrl, providers);
    if (connectButton) {
      connectButton.disabled = false;
    }
  } catch (err) {
    showError(err instanceof Error ? err.message : String(err));
    setLoginStatus(null);
    if (connectButton) {
      connectButton.disabled = false;
    }
  }
});

document.querySelector('#composer')?.addEventListener('submit', (event) => {
  event.preventDefault();
  sendCurrentMessage();
});

messageInput?.addEventListener('input', () => {
  resizeComposer();
});

messageInput?.addEventListener('keydown', (event) => {
  if (event.key === 'Enter' && !event.shiftKey) {
    event.preventDefault();
    sendCurrentMessage();
  }
});

logoutButton?.addEventListener('click', () => {
  void (async () => {
    await clearSession();
    sessionId = '';
    studioUrl = '';
    streaming = false;
    hideApplyBar();
    hideProviderPicker();
    setLoginStatus(null);
    clearError();
    transcript?.replaceChildren();
    showCaptureBanner(null);
    setPageMeta(null);
    setLoggedIn(false);
  })();
});

applyButton?.addEventListener('click', () => {
  if (pendingEdit === null) {
    return;
  }
  const text = pendingEdit;
  void (async () => {
    try {
      const result = (await chrome.runtime.sendMessage({
        type: MSG_APPLY,
        text,
        mode: 'replace',
      })) as ApplyResult;
      if (result?.ok) {
        if (statusLine) {
          statusLine.textContent = 'Applied to page';
        }
        hideApplyBar();
        return;
      }
      const error = result?.error || 'Apply failed';
      if (result?.reason === 'not-editable') {
        // Loud, iterative handshake: keep pendingEdit pinned, prompt the user
        // to open the editor, and require a second Apply. No clipboard copy.
        showApplyBlocked(error);
        return;
      }
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(text);
      }
      if (statusLine) {
        statusLine.textContent = error;
      }
    } catch (err) {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(text);
      }
      if (statusLine) {
        statusLine.textContent = err instanceof Error ? err.message : 'Apply failed';
      }
    }
  })();
});

dismissButton?.addEventListener('click', () => {
  hideApplyBar();
});

sessionPicker?.addEventListener('change', () => {
  void selectStoredSession(sessionPicker.value);
});

deleteSessionButton?.addEventListener('click', async () => {
  if (!sessionId) {
    // Can't delete "New chat" (empty session)
    return;
  }
  const confirmed = confirm(`Delete this session?`);
  if (!confirmed) {
    return;
  }
  const state = await deleteExtensionSession(sessionId);
  sessionId = state.currentSessionId;
  renderSessionPicker(state);
  if (state.currentSessionId) {
    void selectStoredSession(state.currentSessionId);
  } else {
    // Switched to "New chat"
    void beginNewChat();
  }
});

void loadSession().then(async (session) => {
  if (!session) {
    setLoggedIn(false);
    return;
  }
  studioUrl = session.serverUrl;
  if (serverUrlInput) {
    serverUrlInput.value = session.serverUrl;
  }
  setLoggedIn(true);
  await restoreExtensionChat();
});
