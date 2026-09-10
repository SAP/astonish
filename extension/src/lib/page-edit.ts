export const PAGE_EDIT_FENCE = 'astonish-page-edit';

const FENCE_RE = new RegExp(
  '```' + PAGE_EDIT_FENCE + '\\s*\\r?\\n([\\s\\S]*?)```',
  'g',
);

const REF_ID = /^ref\d+$/i;

/**
 * If a write payload is a page-tool envelope (`{"ref":"ref1","text":"..."}`)
 * instead of the document, return the inner text. Markdown / plain values
 * pass through unchanged. Nested envelopes are unwrapped until the document.
 */
export function unwrapEditorPayload(raw: unknown): string {
  if (typeof raw !== 'string') {
    if (raw && typeof raw === 'object' && !Array.isArray(raw)) {
      const nested = unwrapRecord(raw as Record<string, unknown>);
      if (nested !== null) {
        return unwrapEditorPayload(nested);
      }
    }
    return '';
  }
  const trimmed = raw.trim();
  if (!trimmed.startsWith('{') || !trimmed.endsWith('}')) {
    return raw;
  }
  try {
    const parsed = JSON.parse(trimmed) as unknown;
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      return raw;
    }
    const inner = unwrapRecord(parsed as Record<string, unknown>);
    if (inner === null) {
      return raw;
    }
    return inner === trimmed ? inner : unwrapEditorPayload(inner);
  } catch {
    return raw;
  }
}

function isToolEnvelope(parsed: Record<string, unknown>, nested: Record<string, unknown>): boolean {
  const ref = nested.ref ?? parsed.ref;
  const name = parsed.name;
  if (name === 'page_fill') {
    return true;
  }
  if (typeof ref === 'string' && REF_ID.test(ref)) {
    return true;
  }
  const keys = Object.keys(nested);
  return keys.includes('text') && keys.includes('ref') && keys.every((key) => key === 'text' || key === 'ref');
}

function unwrapRecord(parsed: Record<string, unknown>): string | null {
  const nested =
    parsed.args && typeof parsed.args === 'object' && !Array.isArray(parsed.args)
      ? (parsed.args as Record<string, unknown>)
      : parsed.arguments && typeof parsed.arguments === 'object' && !Array.isArray(parsed.arguments)
        ? (parsed.arguments as Record<string, unknown>)
        : parsed;
  const text = nested.text;
  if (typeof text !== 'string') {
    return null;
  }
  if (!isToolEnvelope(parsed, nested)) {
    return null;
  }
  return text;
}

/** Extract the last `astonish-page-edit` fenced block from agent text. */
export function extractPageEdit(text: string): string | null {
  let match: RegExpExecArray | null;
  let last: string | null = null;
  FENCE_RE.lastIndex = 0;
  while ((match = FENCE_RE.exec(text)) !== null) {
    last = match[1] ?? '';
    if (last.endsWith('\n')) {
      last = last.slice(0, -1);
    }
  }
  return last === null ? null : unwrapEditorPayload(last);
}
