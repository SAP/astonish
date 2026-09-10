import { studioFetch } from './auth';

export type SSEEventCallback = (eventType: string, data: Record<string, unknown>) => void;
export type ErrorCallback = (error: Error) => void;
export type DoneCallback = () => void;

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
  toolArgs?: unknown;
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
  const response = await studioFetch('/api/studio/sessions');
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
  const response = await studioFetch(`/api/studio/sessions/${encodeURIComponent(id)}`);
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
        const blocks = buffer.split('\n\n');
        buffer = blocks.pop() ?? '';

        for (const block of blocks) {
          if (!block.trim()) {
            continue;
          }
          const lines = block.split('\n');
          let eventType = 'message';
          let dataStr = '';

          for (const line of lines) {
            if (line.startsWith('event: ')) {
              eventType = line.slice(7).trim();
            } else if (line.startsWith('data: ')) {
              dataStr = line.slice(6);
            }
          }

          if (!dataStr) {
            continue;
          }
          try {
            const data = JSON.parse(dataStr) as Record<string, unknown>;
            dispatchEvent(eventType, data, onEvent);
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
