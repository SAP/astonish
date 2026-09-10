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
  /** Bounding rect for CDP input dispatch (tab-absolute coordinates). Internal to extension. */
  rect?: { x: number; y: number; width: number; height: number };
};

/** Sent by the coordinator (top frame) to worker frames via chrome.tabs.sendMessage. */
export const MSG_FRAME_TOOL = 'astonish.frameTool' as const;

/**
 * Payload sent to a specific child frame to execute a page tool in that frame's
 * document context and return a partial PageToolResult.
 */
export type FrameToolPayload = {
  type: typeof MSG_FRAME_TOOL;
  name: string;
  args: Record<string, unknown>;
  /** Sequential ref offset so the child frame's ref numbers don't collide with the top frame's. */
  refOffset: number;
};

/** Response from a child frame for MSG_FRAME_TOOL. */
export type FrameToolResult = {
  ok?: boolean;
  /** Serialized interactive-elements text from this frame (may be empty). */
  interactive: string;
  /** Serialized headings from this frame (may be empty). */
  headings: string;
  /** If the tool was page_click or page_fill, this is the outcome string. */
  result?: string;
  error?: string;
  href?: string;
  /** How many refs were registered in this frame (so the next frame can offset). */
  refCount: number;
  /** Bounding rect for CDP input dispatch (tab-absolute coordinates). */
  rect?: { x: number; y: number; width: number; height: number };
};

export type MessageType =
  | typeof MSG_GET_CONTEXT
  | typeof MSG_CONTEXT
  | typeof MSG_APPLY
  | typeof MSG_APPLY_RESULT
  | typeof MSG_PAGE_TOOL
  | typeof MSG_FRAME_TOOL
  | typeof MSG_PING;
