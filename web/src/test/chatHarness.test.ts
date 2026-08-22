import { describe, it, expect } from 'vitest'
import {
  deriveLatestHarness,
  harnessFocusEquals,
  resolveHarnessFocus,
  clampHarnessWidth,
  HARNESS_DEFAULT_WIDTH,
  HARNESS_MIN_WIDTH,
  CHAT_MIN_WIDTH,
  type HarnessFocus,
} from '../components/chat/chatHarness'
import type { ChatMsg } from '../components/chat/chatTypes'

function app(title: string, version: number, appId?: string): ChatMsg {
  return {
    type: 'app_preview',
    code: '<div/>',
    title,
    description: '',
    version,
    appId,
  }
}

function distill(flowName: string): ChatMsg {
  return {
    type: 'distill_preview',
    yaml: 'name: x',
    flowName,
    description: 'desc',
    tags: [],
    explanation: '',
  }
}

function tutorial(title: string): ChatMsg {
  return {
    type: 'tutorial_blueprint_preview',
    title,
    suite: 'suite',
    blueprintYaml: '',
    scenes: [],
  }
}

function slideshow(title: string): ChatMsg {
  return {
    type: 'tutorial_scene_slideshow',
    title,
    suite: 'suite',
    drill: 'drill',
    manifestPath: '/manifest.json',
    scenes: [],
  }
}

function browser(reason: string): ChatMsg {
  return {
    type: 'browser_handoff',
    vncProxyUrl: 'http://vnc',
    pageUrl: 'https://example.com',
    pageTitle: 'Page',
    reason,
  }
}

function slidesDeck(deckSlug: string, title = 'Deck'): ChatMsg {
  return {
    type: 'docs_update',
    docType: 'slides',
    deckSlug,
    action: 'slide_written',
    slideIndex: 1,
    totalSlides: 3,
    title,
  }
}

function artifact(path: string, isReport = true): ChatMsg {
  return {
    type: 'artifact',
    path,
    toolName: 'write_file',
    isReport,
  }
}

describe('chatHarness', () => {
  describe('deriveLatestHarness', () => {
    it('returns null for empty messages', () => {
      expect(deriveLatestHarness([], new Set())).toBeNull()
    })

    it('picks the most recent harness emission', () => {
      const messages: ChatMsg[] = [
        { type: 'user', content: 'hi' },
        distill('flow_a'),
        app('Dashboard', 1, 'app-1'),
      ]
      expect(deriveLatestHarness(messages, new Set())).toEqual({
        kind: 'app',
        appId: 'app-1',
        messageIndex: 2,
      })
    })

    it('switches from distill to app when app arrives later', () => {
      const messages: ChatMsg[] = [distill('flow_a'), app('A', 1, 'a')]
      expect(deriveLatestHarness(messages, new Set())?.kind).toBe('app')
    })

    it('updates app focus messageIndex on version bump', () => {
      const messages: ChatMsg[] = [
        app('Dashboard', 1, 'app-1'),
        { type: 'user', content: 'tweak' },
        app('Dashboard', 2, 'app-1'),
      ]
      expect(deriveLatestHarness(messages, new Set())).toEqual({
        kind: 'app',
        appId: 'app-1',
        messageIndex: 2,
      })
    })

    it('includes gated reports only', () => {
      const messages: ChatMsg[] = [
        artifact('/tmp/a.md', true),
        artifact('/tmp/b.md', true),
      ]
      const gated = new Set(['/tmp/b.md'])
      expect(deriveLatestHarness(messages, gated)).toEqual({
        kind: 'report',
        path: '/tmp/b.md',
        messageIndex: 1,
      })
    })

    it('ignores ungated report artifacts', () => {
      const messages: ChatMsg[] = [artifact('/tmp/a.md', true)]
      expect(deriveLatestHarness(messages, new Set())).toBeNull()
    })

    it('picks tutorial blueprint', () => {
      const messages: ChatMsg[] = [tutorial('My Tutorial')]
      expect(deriveLatestHarness(messages, new Set())).toEqual({
        kind: 'tutorial_blueprint',
        messageIndex: 0,
      })
    })

    it('includes gated videos only', () => {
      const messages: ChatMsg[] = [
        artifact('/tmp/a.mp4', false),
        artifact('/tmp/b.mp4', false),
      ]
      const videos = new Set(['/tmp/b.mp4'])
      expect(deriveLatestHarness(messages, new Set(), videos)).toEqual({
        kind: 'video',
        path: '/tmp/b.mp4',
        messageIndex: 1,
      })
    })

    it('ignores videos not in videoPaths (e.g. slideshow-owned)', () => {
      const messages: ChatMsg[] = [artifact('/tmp/scene.mp4', false)]
      expect(deriveLatestHarness(messages, new Set(), new Set())).toBeNull()
    })

    it('picks browser handoff then switches to slideshow when later', () => {
      const messages: ChatMsg[] = [browser('CAPTCHA'), slideshow('Scenes')]
      expect(deriveLatestHarness(messages, new Set(), new Set())).toEqual({
        kind: 'tutorial_slideshow',
        messageIndex: 1,
      })
    })

    it('picks a slides deck from a docs_update message', () => {
      const messages: ChatMsg[] = [
        { type: 'user', content: 'make a deck' },
        slidesDeck('quarterly-review', 'Quarterly Review'),
      ]
      expect(deriveLatestHarness(messages, new Set())).toEqual({
        kind: 'slides',
        deckSlug: 'quarterly-review',
        messageIndex: 1,
      })
    })
  })

  describe('resolveHarnessFocus', () => {
    const older: HarnessFocus = { kind: 'distill', messageIndex: 1 }
    const newer: HarnessFocus = { kind: 'app', appId: 'x', messageIndex: 5 }

    it('uses latest when no manual override', () => {
      expect(resolveHarnessFocus(newer, null)).toEqual(newer)
    })

    it('keeps manual until a newer emission arrives', () => {
      expect(resolveHarnessFocus(older, older)).toEqual(older)
      const manualOlder: HarnessFocus = { kind: 'distill', messageIndex: 1 }
      expect(resolveHarnessFocus(newer, manualOlder)).toEqual(newer)
    })

    it('keeps manual when it is the same or newer index', () => {
      const manual: HarnessFocus = { kind: 'report', path: '/r.md', messageIndex: 5 }
      expect(resolveHarnessFocus(newer, manual)).toEqual(manual)
    })
  })

  describe('harnessFocusEquals', () => {
    it('matches apps by appId', () => {
      expect(
        harnessFocusEquals(
          { kind: 'app', appId: 'a', messageIndex: 1 },
          { kind: 'app', appId: 'a', messageIndex: 9 },
        ),
      ).toBe(true)
      expect(
        harnessFocusEquals(
          { kind: 'app', appId: 'a', messageIndex: 1 },
          { kind: 'app', appId: 'b', messageIndex: 1 },
        ),
      ).toBe(false)
    })

    it('matches distill by messageIndex', () => {
      expect(
        harnessFocusEquals(
          { kind: 'distill', messageIndex: 2 },
          { kind: 'distill', messageIndex: 2 },
        ),
      ).toBe(true)
      expect(
        harnessFocusEquals(
          { kind: 'distill', messageIndex: 2 },
          { kind: 'distill', messageIndex: 3 },
        ),
      ).toBe(false)
    })

    it('matches slides by deckSlug', () => {
      expect(
        harnessFocusEquals(
          { kind: 'slides', deckSlug: 'deck-a', messageIndex: 1 },
          { kind: 'slides', deckSlug: 'deck-a', messageIndex: 9 },
        ),
      ).toBe(true)
      expect(
        harnessFocusEquals(
          { kind: 'slides', deckSlug: 'deck-a', messageIndex: 1 },
          { kind: 'slides', deckSlug: 'deck-b', messageIndex: 1 },
        ),
      ).toBe(false)
    })
  })

  describe('clampHarnessWidth', () => {
    const sidebar = 48

    it('uses preferred width when space allows (default 1080)', () => {
      // 1920 row: max = 1920 - 48 - 380 = 1492
      expect(clampHarnessWidth(HARNESS_DEFAULT_WIDTH, 1920, sidebar)).toBe(1080)
    })

    it('caps harness so chat keeps CHAT_MIN_WIDTH', () => {
      // 1200 row: max = 1200 - 48 - 380 = 772
      expect(clampHarnessWidth(1080, 1200, sidebar)).toBe(772)
      expect(1200 - sidebar - 772).toBe(CHAT_MIN_WIDTH)
    })

    it('enforces HARNESS_MIN_WIDTH when dragging below the floor', () => {
      expect(clampHarnessWidth(200, 1920, sidebar)).toBe(HARNESS_MIN_WIDTH)
    })

    it('allows harness below HARNESS_MIN_WIDTH when viewport cannot fit both floors', () => {
      // 700 row: max = 700 - 48 - 380 = 272
      expect(clampHarnessWidth(1080, 700, sidebar)).toBe(272)
    })
  })
})
