import {
  connectChat,
  deleteSession,
  fetchSessionHistory,
  fetchSessions,
  historyMessageKind,
  sessionIdFromEvent,
  stopChat,
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
const stopButton = document.querySelector<HTMLButtonElement>('#stop');
const sessionPicker = document.querySelector<HTMLSelectElement>('#session-picker');
const deleteSessionButton = document.querySelector<HTMLButtonElement>('#delete-session');


let sessionId = '';
let studioUrl = '';
let streaming = false;
let pendingEdit: string | null = null;
let currentAbort: AbortController | null = null;
let currentTabId = 0;

const MAX_PAGE_TOOL_ROUNDS = 15;

// Read the tab ID directly from the URL query parameter set by the service worker
// when it opens this side panel. This is synchronous and 100% reliable.
function resolveTabId(): number {
  if (typeof location === 'undefined') return 0;
  const params = new URLSearchParams(location.search);
  return Number(params.get('tabId') ?? '0') || 0;
}

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

/**
 * Append a grouped tool-use section to the transcript. Renders a compact
 * collapsible block showing how many tools were called, with a togglable
 * list of individual tool names — matching the Studio chat's tool fold.
 */
function appendToolGroup(tools: string[]): HTMLElement {
  const wrap = document.createElement('div');
  wrap.className = 'msg msg-tool-group';
  const summary = document.createElement('div');
  summary.className = 'tool-group-summary';
  const count = tools.length;
  summary.textContent = count === 1
    ? `Used 1 tool`
    : `Used ${count} tools`;
  summary.addEventListener('click', () => {
    wrap.classList.toggle('is-expanded');
  });
  wrap.appendChild(summary);
  const details = document.createElement('div');
  details.className = 'tool-group-details';
  for (const name of tools) {
    const item = document.createElement('div');
    item.className = 'tool-group-item';
    item.textContent = name;
    details.appendChild(item);
  }
  wrap.appendChild(details);
  transcript?.appendChild(wrap);
  scrollTranscript();
  return wrap;
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
  let currentToolGroup: HTMLElement | null = null;
  for (const message of messages) {
    const kind = historyMessageKind(message);
    const content = typeof message.content === 'string' ? message.content : '';
    if (kind === 'user') {
      currentToolGroup = null;
      if (content) {
        appendUser(content);
      }
      continue;
    }
    if (kind === 'agent' || kind === 'assistant') {
      currentToolGroup = null;
      if (content) {
        const { body } = appendAssistant();
        setAssistantMarkdown(body, stripPageToolFences(content));
      }
      continue;
    }
    if (kind === 'tool_call') {
      const toolName = message.toolName || 'tool';
      const args = (message as { toolArgs?: unknown }).toolArgs;
      currentToolGroup = appendOrUpdateToolGroup(currentToolGroup, toolName, args);
      continue;
    }
    if (kind === 'tool_result') {
      // Mark the last tool in the current group as done.
      if (currentToolGroup) {
        const details = currentToolGroup.querySelector('.tool-group-details');
        if (details && details.children.length > 1) {
          const lastItem = details.children[details.children.length - 1];
          const statusEl = lastItem.querySelector('.tool-status');
          if (statusEl) {
            statusEl.textContent = '✓';
            statusEl.className = 'tool-status success';
          }
        }
      }
      continue;
    }
    // Any other kind resets tool group context.
    currentToolGroup = null;
  }
  scrollTranscript();
}

async function refreshExtensionSessions(): Promise<ExtensionChatState> {
  const stored = await loadExtensionChat(currentTabId);
  try {
    const listed = await fetchSessions();
    const pruned = pruneMissingSessionIds(stored.sessions, listed);
    const merged = mergeSessionTitles(pruned, listed);
    // Add server sessions not yet in local storage (e.g. sessions created in another tab)
    const localIds = new Set(merged.map((s) => s.id));
    const newSessions = listed
      .filter((s) => !localIds.has(s.id))
      .map((s) => ({ id: s.id, title: s.title || 'New chat' }));
    const allSessions = [...merged, ...newSessions];
    const currentStillThere =
      !stored.currentSessionId || allSessions.some((session) => session.id === stored.currentSessionId);
    const saved = await replaceExtensionSessions(
      currentTabId,
      allSessions,
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
  currentTabId = resolveTabId();
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
      const next = await updateExtensionSessionTitle(currentTabId, sessionId, history.title);
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
  const state = await startNewExtensionSession(currentTabId);
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
  await setCurrentSessionId(currentTabId, id);
  try {
    const history = await fetchSessionHistory(id);
    renderHistory(history.messages);
    const title = history.title || id;
    const state = await rememberExtensionSession(currentTabId, id, title);
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
  void rememberExtensionSession(currentTabId, id, title).then(renderSessionPicker);
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
  return chrome.runtime.sendMessage({ type: MSG_GET_CONTEXT, tabId: currentTabId }) as Promise<ContextResult>;
}

async function runPageToolCalls(calls: PageToolCall[]): Promise<PageToolResult[]> {
  const results: PageToolResult[] = [];
  for (const call of calls) {
    try {
      const result = (await chrome.runtime.sendMessage({
        type: MSG_PAGE_TOOL,
        name: call.name,
        args: call.args,
        tabId: currentTabId,
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

function appendOrUpdateToolGroup(
  existingGroup: HTMLElement | null,
  toolName: string,
  args?: unknown,
): HTMLElement {
  let wrap = existingGroup;
  let details: HTMLElement;
  let summary: HTMLElement;

  if (!wrap) {
    wrap = document.createElement('div');
    wrap.className = 'msg msg-tool-group';

    const detailsEl = document.createElement('details');
    detailsEl.className = 'tool-group-details';

    summary = document.createElement('summary');
    summary.className = 'tool-group-summary';
    summary.textContent = 'Used 1 tool';

    detailsEl.appendChild(summary);
    wrap.appendChild(detailsEl);
    transcript?.appendChild(wrap);
    details = detailsEl;
  } else {
    details = wrap.querySelector('.tool-group-details') as HTMLElement;
    summary = details.querySelector('.tool-group-summary') as HTMLElement;
  }

  // Add the tool item
  const item = document.createElement('div');
  item.className = 'tool-group-item';

  const nameEl = document.createElement('span');
  nameEl.className = 'tool-call-name';
  nameEl.textContent = toolName;
  item.appendChild(nameEl);

  if (args != null) {
    const argsStr = typeof args === 'string' ? args : JSON.stringify(args);
    if (argsStr && argsStr !== '{}' && argsStr !== 'null') {
      const preview = document.createElement('span');
      preview.className = 'tool-args-preview';
      preview.textContent = argsStr.length > 120 ? argsStr.slice(0, 120) + '…' : argsStr;
      item.appendChild(preview);
    }
  }

  const statusEl = document.createElement('span');
  statusEl.className = 'tool-status running';
  statusEl.textContent = '⏳';
  item.appendChild(statusEl);

  details.appendChild(item);

  // Update the summary count (children minus the <summary> element)
  const count = details.children.length - 1;
  summary.textContent = count === 1 ? 'Used 1 tool' : `Used ${count} tools`;

  scrollTranscript();
  return wrap;
}

function streamOnce(params: {
  message: string;
  systemContext?: string;
  onText: (segmentText: string, isNewSegment: boolean) => void;
}): Promise<{ text: string; error?: string }> {
  return new Promise((resolve) => {
    let assistantText = '';
    // Offset into assistantText where the current visual segment started.
    let segmentStart = 0;
    // The current live tool-group element (null when not in a tool batch).
    let currentToolGroup: HTMLElement | null = null;
    // True when at least one tool_call has fired since the last text chunk.
    let hadToolsSinceText = false;

    const controller = connectChat({
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
          if (chunk) {
            assistantText += chunk;
            // If tools fired since the last text, start a new visual segment.
            const isNewSegment = hadToolsSinceText;
            if (isNewSegment) {
              segmentStart = assistantText.length - chunk.length;
            }
            hadToolsSinceText = false;
            currentToolGroup = null;
            // Pass only this segment's text so each bubble shows only its portion.
            params.onText(assistantText.slice(segmentStart), isNewSegment);
          }
        }
        if (type === 'tool_call') {
          const name = typeof data.name === 'string' ? data.name : 'tool';
          const args = data.args;
          hadToolsSinceText = true;
          currentToolGroup = appendOrUpdateToolGroup(currentToolGroup, name, args);
        }
        if (type === 'tool_result') {
          // Mark the last tool item in the current group as done.
          if (currentToolGroup) {
            const details = currentToolGroup.querySelector('.tool-group-details');
            if (details && details.children.length > 1) {
              const lastItem = details.children[details.children.length - 1];
              const statusEl = lastItem.querySelector('.tool-status');
              if (statusEl) {
                statusEl.textContent = '✓';
                statusEl.className = 'tool-status success';
              }
            }
          }
        }
        if (type === 'approval') {
          currentToolGroup = null;
          const notice = document.createElement('a');
          notice.href = studioUrl || '#';
          notice.target = '_blank';
          notice.rel = 'noreferrer';
          notice.textContent = 'Finish this in Studio';
          appendNotice('approval', 'Approval needed. ', notice);
        }
        if (type === 'error' || type === 'error_info') {
          currentToolGroup = null;
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
    currentAbort = controller;
    return controller;
  });
}

function finishStreaming(status?: string): void {
  streaming = false;
  currentAbort = null;
  if (stopButton) {
    stopButton.hidden = true;
  }
  if (messageInput) {
    messageInput.disabled = false;
    messageInput.placeholder = 'Say hi, or drop a task in here…';
  }
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
  if (messageInput) {
    messageInput.disabled = true;
    messageInput.placeholder = 'Agent is responding…';
  }
  if (stopButton) {
    stopButton.hidden = false;
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
    // When the page is a read-only GitHub issue/PR (no open description editor),
    // page-tool calls are pointless — the model should just emit a fence. Skip
    // the tool loop entirely to prevent the model from kebab-hunting in circles.
    let skipPageTools = false;
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

    for (let round = 0; round < MAX_PAGE_TOOL_ROUNDS; round += 1) {
      let assistantBody: HTMLElement | null = null;
      const { text: assistantText, error } = await streamOnce({
        message,
        systemContext: context,
        onText: (segmentText, isNewSegment) => {
          lastAssistant = segmentText;
          // When text arrives after a tool group, start a fresh assistant bubble.
          if (isNewSegment && assistantBody) {
            assistantBody = null;
          }
          if (!assistantBody) {
            assistantBody = appendAssistant().body;
          }
          const visible = stripPageToolFences(segmentText);
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
      context = formatPageToolResults(results);
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

stopButton?.addEventListener('click', () => {
  // Abort the SSE connection
  if (currentAbort) {
    currentAbort.abort();
    currentAbort = null;
  }
  // Stop the backend runner
  if (sessionId) {
    void stopChat(sessionId);
  }
  appendNotice('error', 'Stopped by user.');
  finishStreaming('Stopped.');
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
        tabId: currentTabId,
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
  // Delete from server first; fall through to local cleanup even on failure
  // (the session will be pruned on next sync if the server call fails).
  try {
    await deleteSession(sessionId);
  } catch {
    // Best-effort — remove from local state regardless.
  }
  const state = await deleteExtensionSession(currentTabId, sessionId);
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
