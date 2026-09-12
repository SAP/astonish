export const PAGE_TOOL_FENCE = 'astonish-page-tool';

export const PAGE_TOOL_NAMES = [
  'page_snapshot',
  'page_query',
  'page_click',
  'page_navigate',
  'page_fill',
  'page_select',
  'page_scroll',
] as const;

export type PageToolName = (typeof PAGE_TOOL_NAMES)[number];

export type PageToolCall = {
  name: string;
  args: Record<string, unknown>;
};

const FENCE_RE = new RegExp('```' + PAGE_TOOL_FENCE + '\\s*\\r?\\n([\\s\\S]*?)```', 'g');

const NESTED_KEYS = new Set(['name', 'args', 'arguments']);

function parseCall(raw: string): PageToolCall | null {
  try {
    const parsed = JSON.parse(raw) as Record<string, unknown>;
    if (typeof parsed.name !== 'string' || !parsed.name.trim()) {
      return null;
    }
    const nested =
      parsed.args && typeof parsed.args === 'object' && !Array.isArray(parsed.args)
        ? (parsed.args as Record<string, unknown>)
        : parsed.arguments && typeof parsed.arguments === 'object' && !Array.isArray(parsed.arguments)
          ? (parsed.arguments as Record<string, unknown>)
          : {};
    const args: Record<string, unknown> = { ...nested };
    for (const [key, value] of Object.entries(parsed)) {
      if (NESTED_KEYS.has(key) || value === undefined) {
        continue;
      }
      if (args[key] === undefined) {
        args[key] = value;
      }
    }
    return { name: parsed.name.trim(), args };
  } catch {
    return null;
  }
}

/** Extract `astonish-page-tool` fenced JSON calls from agent text. */
export function extractPageTools(text: string): PageToolCall[] {
  const calls: PageToolCall[] = [];
  FENCE_RE.lastIndex = 0;
  let match: RegExpExecArray | null;
  while ((match = FENCE_RE.exec(text)) !== null) {
    const call = parseCall((match[1] ?? '').trim());
    if (call) {
      calls.push(call);
    }
  }
  return calls;
}

export function stripPageToolFences(text: string): string {
  FENCE_RE.lastIndex = 0;
  return text.replace(FENCE_RE, '').replace(/\n{3,}/g, '\n\n').trim();
}

export function isKnownPageTool(name: string): name is PageToolName {
  return (PAGE_TOOL_NAMES as readonly string[]).includes(name);
}

export function formatPageToolResults(
  results: Array<{ name: string; ok: boolean; result?: string; error?: string }>,
): string {
  const blocks = results.map((entry) => {
    const header = `### ${entry.name} (${entry.ok ? 'ok' : 'error'})`;
    const body = entry.ok
      ? (entry.result ?? '(empty)')
      : (entry.error || entry.result || 'failed');
    return `${header}\n${body}`;
  });
  return [
    '## Chrome extension page-tool results',
    'These results are from THIS browser tab. The original page document remains in ### Main content above — do not treat a snapshot as a replacement for it. Read ## Page map before the next page action. Do not page_navigate to the current URL. Use page_snapshot / page_query / page_click / page_fill only when you still need this tab\'s DOM. For work unrelated to this tab, keep using the normal Astonish skill and tool-discovery loop rather than page tools.',
    ...blocks,
  ].join('\n\n');
}
