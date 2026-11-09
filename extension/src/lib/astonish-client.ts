import { studioFetch } from './auth';

export type SSEEventCallback = (eventType: string, data: Record<string, unknown>) => void;
export type ErrorCallback = (error: Error) => void;
export type DoneCallback = () => void;

export const EXTENSION_APP_NAME = 'astonish-extension';

const HANDLED_EVENTS = new Set([
  'session',
  'session_title',
  'text',
  'done',
  'error',
  'error_info',
  'tool_call',
  'tool_result',
  'approval',
  // Bot streams carry persisted events (including metadata/state) verbatim.
  // Never unwrap these into executable chat or approval events.
  'transcript',
  'replay_complete',
]);

/** Studio emits `sessionId`; fixtures and some clients use `id`. */
export function sessionIdFromEvent(data: Record<string, unknown>): string {
  if (typeof data.sessionId === 'string' && data.sessionId.trim()) {
    return data.sessionId.trim();
  }
  if (typeof data.id === 'string' && data.id.trim()) {
    return data.id.trim();
  }
  return '';
}

export type StudioSession = {
  id: string;
  title: string;
  createdAt?: string;
  updatedAt?: string;
  messageCount?: number;
};

export type HistoryMessage = {
  type?: string;
  role?: string;
  content?: string;
  toolName?: string;
};

export function historyMessageKind(message: HistoryMessage): string {
  if (typeof message.type === 'string' && message.type.trim()) {
    return message.type.trim();
  }
  if (message.role === 'assistant' || message.role === 'model') {
    return 'agent';
  }
  if (typeof message.role === 'string' && message.role.trim()) {
    return message.role.trim();
  }
  return '';
}

export type SessionHistory = {
  id: string;
  title: string;
  messages: HistoryMessage[];
};

export async function fetchSessions(): Promise<StudioSession[]> {
  const response = await studioFetch(`/api/studio/sessions?app=${encodeURIComponent(EXTENSION_APP_NAME)}`);
  if (!response.ok) {
    throw new Error(`Failed to fetch sessions: ${response.status}`);
  }
  const data = (await response.json().catch(() => [])) as unknown;
  if (!Array.isArray(data)) {
    return [];
  }
  return data.filter((item): item is StudioSession => {
    return !!item && typeof item === 'object' && typeof (item as StudioSession).id === 'string';
  });
}

export async function fetchSessionHistory(id: string): Promise<SessionHistory> {
  const response = await studioFetch(
    `/api/studio/sessions/${encodeURIComponent(id)}?app=${encodeURIComponent(EXTENSION_APP_NAME)}`,
  );
  if (!response.ok) {
    throw new Error(`Failed to fetch session: ${response.status}`);
  }
  const data = (await response.json().catch(() => ({}))) as Partial<SessionHistory>;
  const messages = Array.isArray(data.messages)
    ? data.messages.filter((item): item is HistoryMessage => {
        if (!item || typeof item !== 'object') {
          return false;
        }
        return historyMessageKind(item as HistoryMessage) !== '';
      })
    : [];
  return {
    id: typeof data.id === 'string' ? data.id : id,
    title: typeof data.title === 'string' ? data.title : '',
    messages,
  };
}

export interface ConnectChatParams {
  sessionId?: string;
  message?: string;
  systemContext?: string;
  autoApprove?: boolean;
  onEvent: SSEEventCallback;
  onError?: ErrorCallback;
  onDone?: DoneCallback;
}

function dispatchEvent(eventType: string, data: Record<string, unknown>, onEvent: SSEEventCallback): void {
  if (!HANDLED_EVENTS.has(eventType)) {
    return;
  }
  onEvent(eventType, data);
}

export function connectChat({
  sessionId,
  message,
  systemContext,
  autoApprove,
  onEvent,
  onError,
  onDone,
}: ConnectChatParams): AbortController {
  const controller = new AbortController();

  const run = async () => {
    try {
      const body: Record<string, unknown> = {
        sessionId: sessionId || '',
        message: message || '',
        autoApprove: !!autoApprove,
        appName: EXTENSION_APP_NAME,
      };
      if (systemContext) {
        body.systemContext = systemContext;
      }

      const response = await studioFetch('/api/studio/chat', {
        method: 'POST',
        signal: controller.signal,
        body: JSON.stringify(body),
      });

      if (!response.ok) {
        const text = await response.text();
        throw new Error(text || `HTTP ${response.status}`);
      }

      const reader = response.body?.getReader();
      if (!reader) {
        throw new Error('chat response was not a stream');
      }
      const decoder = new TextDecoder();
      let buffer = '';

      while (true) {
        const { value, done } = await reader.read();
        if (done) {
          break;
        }

        buffer += decoder.decode(value, { stream: true });
        let separator: RegExpExecArray | null;
        while ((separator = /\r?\n\r?\n/.exec(buffer))) {
          const block = buffer.slice(0, separator.index);
          buffer = buffer.slice(separator.index + separator[0].length);
          const lines = block.split(/\r?\n/);
          let eventType = 'message';
          const dataLines: string[] = [];

          for (const line of lines) {
            const colon = line.indexOf(':');
            if (colon <= 0) {
              continue; // Comments and fields without values.
            }
            const field = line.slice(0, colon);
            const value = line.slice(colon + 1).replace(/^ /, '');
            if (field === 'event') {
              eventType = value.trim();
            } else if (field === 'data') {
              dataLines.push(value);
            }
          }

          if (!dataLines.length) {
            continue;
          }
          try {
            const data: unknown = JSON.parse(dataLines.join('\n'));
            if (data && typeof data === 'object' && !Array.isArray(data)) {
              dispatchEvent(eventType, data as Record<string, unknown>, onEvent);
            }
          } catch {
            // Ignore malformed SSE payloads, matching Studio's connectChat.
          }
        }
      }

      onDone?.();
    } catch (err) {
      if (err instanceof Error && err.name === 'AbortError') {
        onDone?.();
      } else {
        onError?.(err instanceof Error ? err : new Error(String(err)));
      }
    }
  };

  void run();
  return controller;
}

/**
 * Delete an extension session on the server.
 * Sends DELETE /api/studio/sessions/{id}?app=astonish-extension.
 * Throws if the server returns a non-OK status.
 */
export async function deleteSession(sid: string): Promise<void> {
  const response = await studioFetch(
    `/api/studio/sessions/${encodeURIComponent(sid)}?app=${encodeURIComponent(EXTENSION_APP_NAME)}`,
    { method: 'DELETE' },
  );
  if (!response.ok) {
    throw new Error(`Failed to delete session: ${response.status}`);
  }
}

/**
 * Stop a running chat session via the Studio API.
 * Sends POST /api/studio/sessions/{id}/stop.
 */
export async function stopChat(sid: string): Promise<void> {
  try {
    await studioFetch(`/api/studio/sessions/${encodeURIComponent(sid)}/stop`, {
      method: 'POST',
    });
  } catch {
    // Best-effort — the session may already be stopped.
  }
}
