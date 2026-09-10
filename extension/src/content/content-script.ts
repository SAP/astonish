import { MSG_APPLY, MSG_FRAME_TOOL, MSG_GET_CONTEXT, MSG_PAGE_TOOL, MSG_PING, type FrameToolResult } from '../lib/messages';
import { applyToPage } from './apply';
import { capturePage } from './capture';
import { runPageTool, runPageToolInFrame } from './dom-tools';

// ─── Frame identity ────────────────────────────────────────────────────────────
// When allFrames: true, this script runs in every frame. Detect whether we are
// the top-level coordinator or a child-frame worker.
const isTopFrame = window.self === window.top;

// ─── Worker frame handler ──────────────────────────────────────────────────────
// Child frames respond to MSG_FRAME_TOOL with their own document's DOM results.
if (!isTopFrame) {
  chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
    const type = message && typeof message === 'object' ? (message as { type?: unknown }).type : undefined;
    if (type === MSG_PING) {
      sendResponse({ ok: true });
      return false;
    }
    if (type === MSG_FRAME_TOOL) {
      const name = typeof (message as { name?: unknown }).name === 'string' ? (message as { name: string }).name : '';
      const rawArgs = (message as { args?: unknown }).args;
      const args =
        rawArgs && typeof rawArgs === 'object' && !Array.isArray(rawArgs)
          ? (rawArgs as Record<string, unknown>)
          : {};
      const refOffset = typeof (message as { refOffset?: unknown }).refOffset === 'number'
        ? (message as { refOffset: number }).refOffset
        : 0;
      const result = runPageToolInFrame(name, args, refOffset);
      sendResponse(result satisfies FrameToolResult);
      return false;
    }
    return false;
  });
} else {
  // ─── Top-frame coordinator ─────────────────────────────────────────────────
  // Handles all MSG_PAGE_TOOL requests, runs locally, then fans out to all child
  // frames via MSG_FRAME_TOOL and merges the results.

  /**
   * Send MSG_FRAME_TOOL to all frames in this tab and collect FrameToolResult from each.
   * chrome.tabs.sendMessage with frameId=undefined broadcasts to all frames; we use
   * runtime.sendMessage which already scoped to this tab via the service-worker ping path.
   * However from a content script we must use window.postMessage for same-tab cross-frame,
   * OR rely on the service worker to fan out. The cleanest approach from a content script
   * is to use chrome.runtime.sendMessage back to the SW with a "fanout" request.
   *
   * Actually, chrome.tabs.sendMessage is only available in service-worker/background.
   * From a content script we can use chrome.runtime.sendMessage to the SW, but that's
   * a round-trip. Instead, since the SW already injects with allFrames:true, each frame's
   * content script is already running. We rely on the SW (service-worker.ts) to fan-out
   * MSG_FRAME_TOOL to all frames. The top frame returns a "needsFrameExpansion" marker
   * and the SW performs the fan-out, merges results, and returns to the side panel.
   *
   * So: the top frame content-script does NOT fan-out itself — the SW will call each
   * frame individually. But the top frame does handle MSG_FRAME_TOOL too (from SW).
   *
   * Revised design: the SW fans out, this file only handles MSG_FRAME_TOOL (for any frame)
   * and MSG_PAGE_TOOL (top frame only, for backwards compat — SW aggregates from all frames).
   */

  chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
    const type = message && typeof message === 'object' ? (message as { type?: unknown }).type : undefined;
    if (type === MSG_PING) {
      sendResponse({ ok: true });
      return false;
    }
    if (type === MSG_GET_CONTEXT) {
      sendResponse({ ok: true, context: capturePage() });
      return false;
    }
    if (type === MSG_APPLY) {
      const text = typeof (message as { text?: unknown }).text === 'string' ? (message as { text: string }).text : '';
      void applyToPage(text).then(sendResponse);
      return true;
    }
    if (type === MSG_PAGE_TOOL) {
      // Legacy path — the SW sends MSG_PAGE_TOOL to the top frame which executes
      // against top-frame document only (same-origin iframes included via getAccessibleDocuments).
      // The SW has already aggregated results from cross-origin child frames via MSG_FRAME_TOOL.
      const name = typeof (message as { name?: unknown }).name === 'string' ? (message as { name: string }).name : '';
      const rawArgs = (message as { args?: unknown }).args;
      const args =
        rawArgs && typeof rawArgs === 'object' && !Array.isArray(rawArgs)
          ? (rawArgs as Record<string, unknown>)
          : {};
      sendResponse(runPageTool(name, args));
      return false;
    }
    if (type === MSG_FRAME_TOOL) {
      // The SW can also send MSG_FRAME_TOOL to the top frame (frameId=0) to get a
      // partial result from the top-frame document with a specific ref offset.
      const name = typeof (message as { name?: unknown }).name === 'string' ? (message as { name: string }).name : '';
      const rawArgs = (message as { args?: unknown }).args;
      const args =
        rawArgs && typeof rawArgs === 'object' && !Array.isArray(rawArgs)
          ? (rawArgs as Record<string, unknown>)
          : {};
      const refOffset = typeof (message as { refOffset?: unknown }).refOffset === 'number'
        ? (message as { refOffset: number }).refOffset
        : 0;
      const result = runPageToolInFrame(name, args, refOffset);
      sendResponse(result satisfies FrameToolResult);
      return false;
    }
    return false;
  });
}
