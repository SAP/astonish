import type {
  AppPreviewMessage,
  ArtifactMessage,
  ChatMsg,
  DistillPreviewMessage,
  DocsUpdateMessage,
  TutorialBlueprintPreviewMessage,
} from './chatTypes'

/** Focus target for the right-hand chat harness panel. */
export type HarnessFocus =
  | { kind: 'app'; appId: string; messageIndex: number }
  | { kind: 'report'; path: string; messageIndex: number }
  | { kind: 'video'; path: string; messageIndex: number }
  | { kind: 'distill'; messageIndex: number }
  | { kind: 'tutorial_blueprint'; messageIndex: number }
  | { kind: 'tutorial_slideshow'; messageIndex: number }
  | { kind: 'browser_handoff'; messageIndex: number }
  | { kind: 'slides'; deckSlug: string; messageIndex: number }

/**
 * Scan messages chronologically and return the most recently emitted harness
 * item. `reportPaths` / `videoPaths` must already be gated (last-turn) —
 * do not pass ungated artifact paths. Video paths must exclude slideshow-owned clips.
 */
export function deriveLatestHarness(
  messages: ChatMsg[],
  reportPaths: Set<string>,
  videoPaths: Set<string> = new Set(),
): HarnessFocus | null {
  let latest: HarnessFocus | null = null

  for (let i = 0; i < messages.length; i++) {
    const m = messages[i]
    if (m.type === 'app_preview') {
      const app = m as AppPreviewMessage
      latest = {
        kind: 'app',
        appId: app.appId || app.title,
        messageIndex: i,
      }
    } else if (m.type === 'distill_preview') {
      latest = { kind: 'distill', messageIndex: i }
    } else if (m.type === 'tutorial_blueprint_preview') {
      latest = { kind: 'tutorial_blueprint', messageIndex: i }
    } else if (m.type === 'tutorial_scene_slideshow') {
      latest = { kind: 'tutorial_slideshow', messageIndex: i }
    } else if (m.type === 'browser_handoff') {
      latest = { kind: 'browser_handoff', messageIndex: i }
    } else if (m.type === 'docs_update') {
      const docs = m as DocsUpdateMessage
      if (docs.docType === 'slides' && docs.deckSlug) {
        latest = { kind: 'slides', deckSlug: docs.deckSlug, messageIndex: i }
      }
    } else if (m.type === 'artifact') {
      const art = m as ArtifactMessage
      if (reportPaths.has(art.path)) {
        latest = { kind: 'report', path: art.path, messageIndex: i }
      } else if (videoPaths.has(art.path)) {
        latest = { kind: 'video', path: art.path, messageIndex: i }
      }
    }
  }

  return latest
}

/**
 * Manual placeholder click wins until a newer harness emission arrives
 * (higher messageIndex).
 */
export function resolveHarnessFocus(
  latest: HarnessFocus | null,
  manual: HarnessFocus | null,
): HarnessFocus | null {
  if (!manual) return latest
  if (!latest) return manual
  if (latest.messageIndex > manual.messageIndex) return latest
  return manual
}

/** Whether two focuses refer to the same harness item (for placeholder highlight). */
export function harnessFocusEquals(
  a: HarnessFocus | null | undefined,
  b: HarnessFocus | null | undefined,
): boolean {
  if (!a || !b) return false
  if (a.kind !== b.kind) return false
  switch (a.kind) {
    case 'app':
      return a.appId === (b as Extract<HarnessFocus, { kind: 'app' }>).appId
    case 'slides':
      return a.deckSlug === (b as Extract<HarnessFocus, { kind: 'slides' }>).deckSlug
    case 'report':
    case 'video':
      return a.path === (b as Extract<HarnessFocus, { kind: 'report' | 'video' }>).path
    case 'distill':
    case 'tutorial_blueprint':
    case 'tutorial_slideshow':
    case 'browser_handoff':
      return a.messageIndex === b.messageIndex
  }
}

export function harnessKindLabel(kind: HarnessFocus['kind']): string {
  switch (kind) {
    case 'app':
      return 'App'
    case 'slides':
      return 'Slides'
    case 'report':
      return 'Report'
    case 'video':
      return 'Video'
    case 'distill':
      return 'Flow Draft'
    case 'tutorial_blueprint':
      return 'Tutorial Blueprint'
    case 'tutorial_slideshow':
      return 'Tutorial Scenes'
    case 'browser_handoff':
      return 'Browser'
  }
}

export function appVersionsForFocus(
  messages: ChatMsg[],
  appId: string,
): AppPreviewMessage[] {
  return messages.filter(
    (m): m is AppPreviewMessage =>
      m.type === 'app_preview' &&
      ((m.appId || m.title) === appId),
  )
}

export function messageAt<T extends ChatMsg>(
  messages: ChatMsg[],
  index: number,
): T | null {
  if (index < 0 || index >= messages.length) return null
  return messages[index] as T
}

/** Preferred / reset harness panel width (px). */
export const HARNESS_DEFAULT_WIDTH = 1080
/** Floor when the user drags the resize handle (px). */
export const HARNESS_MIN_WIDTH = 480
/** Chat column never thinner than this; harness shrinks first (px). */
export const CHAT_MIN_WIDTH = 380
/** Approximate collapsed session sidebar strip width (px). */
export const SIDEBAR_COLLAPSED_WIDTH = 48
/** Expanded session sidebar width (px). */
export const SIDEBAR_EXPANDED_WIDTH = 280

export const HARNESS_WIDTH_STORAGE_KEY = 'astonish.harnessPanelWidth'

/**
 * Clamp the harness panel width so chat keeps at least CHAT_MIN_WIDTH
 * and the harness stays at least HARNESS_MIN_WIDTH when space allows.
 */
export function clampHarnessWidth(
  preferred: number,
  containerWidth: number,
  sidebarWidth: number,
): number {
  const maxHarness = Math.max(0, containerWidth - sidebarWidth - CHAT_MIN_WIDTH)
  const floor = Math.min(HARNESS_MIN_WIDTH, maxHarness)
  const capped = Math.min(preferred, maxHarness)
  return Math.max(floor, capped)
}

export function readStoredHarnessWidth(): number {
  if (typeof localStorage === 'undefined') return HARNESS_DEFAULT_WIDTH
  try {
    const raw = localStorage.getItem(HARNESS_WIDTH_STORAGE_KEY)
    if (raw == null) return HARNESS_DEFAULT_WIDTH
    const n = Number(raw)
    if (!Number.isFinite(n) || n <= 0) return HARNESS_DEFAULT_WIDTH
    return n
  } catch {
    return HARNESS_DEFAULT_WIDTH
  }
}

export function writeStoredHarnessWidth(width: number): void {
  if (typeof localStorage === 'undefined') return
  try {
    localStorage.setItem(HARNESS_WIDTH_STORAGE_KEY, String(Math.round(width)))
  } catch {
    // ignore quota / private mode
  }
}

export type { AppPreviewMessage, DistillPreviewMessage, TutorialBlueprintPreviewMessage }
