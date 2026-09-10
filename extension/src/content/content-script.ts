import { MSG_APPLY, MSG_GET_CONTEXT, MSG_PAGE_TOOL, MSG_PING } from '../lib/messages';
import { applyToPage } from './apply';
import { capturePage } from './capture';
import { runPageTool } from './dom-tools';

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
    const name = typeof (message as { name?: unknown }).name === 'string' ? (message as { name: string }).name : '';
    const rawArgs = (message as { args?: unknown }).args;
    const args =
      rawArgs && typeof rawArgs === 'object' && !Array.isArray(rawArgs)
        ? (rawArgs as Record<string, unknown>)
        : {};
    sendResponse(runPageTool(name, args));
    return false;
  }
  return false;
});
