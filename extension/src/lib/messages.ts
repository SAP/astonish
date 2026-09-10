import type { PageContext } from './page-context';

export const MSG_GET_CONTEXT = 'astonish.getContext' as const;
export const MSG_CONTEXT = 'astonish.context' as const;
export const MSG_APPLY = 'astonish.apply' as const;
export const MSG_APPLY_RESULT = 'astonish.applyResult' as const;
export const MSG_PAGE_TOOL = 'astonish.pageTool' as const;
export const MSG_PING = 'astonish.ping' as const;

export type ApplyMode = 'replace';

export type ApplyPayload = {
  type: typeof MSG_APPLY;
  text: string;
  mode: ApplyMode;
};

export type ApplyNotEditableReason = 'not-editable';

export type ApplyResult = {
  ok: boolean;
  error?: string;
  reason?: ApplyNotEditableReason;
};

export type ContextResult = {
  ok: boolean;
  context?: PageContext;
  error?: string;
};

export type PageToolPayload = {
  type: typeof MSG_PAGE_TOOL;
  name: string;
  args?: Record<string, unknown>;
};

export type PageToolResult = {
  ok: boolean;
  name: string;
  result?: string;
  error?: string;
  href?: string;
};

export type MessageType =
  | typeof MSG_GET_CONTEXT
  | typeof MSG_CONTEXT
  | typeof MSG_APPLY
  | typeof MSG_APPLY_RESULT
  | typeof MSG_PAGE_TOOL
  | typeof MSG_PING;
