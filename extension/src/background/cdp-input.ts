/** Tab-absolute bounding rectangle for CDP input dispatch. */
export type Rect = { x: number; y: number; width: number; height: number };

/**
 * Open an ephemeral chrome.debugger session on tabId, run fn, then detach.
 * Ensures the session is always detached even if fn throws.
 */
async function withDebugger<T>(
  tabId: number,
  fn: (target: chrome.debugger.Debuggee) => Promise<T>,
): Promise<T> {
  const target: chrome.debugger.Debuggee = { tabId };
  await chrome.debugger.attach(target, '1.3');
  try {
    return await fn(target);
  } finally {
    try {
      await chrome.debugger.detach(target);
    } catch {
      // Already detached or tab closed — ignore.
    }
  }
}

/**
 * Dispatch a real mouse click at the center of rect via CDP Input.dispatchMouseEvent.
 * Goes through Chrome's full input pipeline (hit testing, pointer events, framework handlers).
 * Throws if chrome.debugger is unavailable (policy-blocked).
 */
export async function cdpClick(tabId: number, rect: Rect): Promise<void> {
  const x = Math.round(rect.x + rect.width / 2);
  const y = Math.round(rect.y + rect.height / 2);
  await withDebugger(tabId, async (target) => {
    const send = (method: string, params: Record<string, unknown>) =>
      chrome.debugger.sendCommand(target, method, params);
    await send('Input.dispatchMouseEvent', { type: 'mouseMoved', x, y });
    await send('Input.dispatchMouseEvent', {
      type: 'mousePressed',
      x,
      y,
      button: 'left',
      clickCount: 1,
    });
    await send('Input.dispatchMouseEvent', {
      type: 'mouseReleased',
      x,
      y,
      button: 'left',
      clickCount: 1,
    });
  });
}

/**
 * Click at rect center to focus, select all existing text, then type replacement text
 * via CDP Input.insertText.
 * Throws if chrome.debugger is unavailable.
 */
export async function cdpFill(tabId: number, rect: Rect, text: string): Promise<void> {
  const x = Math.round(rect.x + rect.width / 2);
  const y = Math.round(rect.y + rect.height / 2);
  await withDebugger(tabId, async (target) => {
    const send = (method: string, params: Record<string, unknown>) =>
      chrome.debugger.sendCommand(target, method, params);
    // Click to focus the element
    await send('Input.dispatchMouseEvent', { type: 'mouseMoved', x, y });
    await send('Input.dispatchMouseEvent', {
      type: 'mousePressed',
      x,
      y,
      button: 'left',
      clickCount: 1,
    });
    await send('Input.dispatchMouseEvent', {
      type: 'mouseReleased',
      x,
      y,
      button: 'left',
      clickCount: 1,
    });
    // Select all existing text (Ctrl+A — works on both Mac and Windows in CDP)
    await send('Input.dispatchKeyEvent', {
      type: 'keyDown',
      key: 'a',
      code: 'KeyA',
      windowsVirtualKeyCode: 65,
      nativeVirtualKeyCode: 65,
      modifiers: 2, // Ctrl
    });
    await send('Input.dispatchKeyEvent', {
      type: 'keyUp',
      key: 'a',
      code: 'KeyA',
      windowsVirtualKeyCode: 65,
      nativeVirtualKeyCode: 65,
      modifiers: 2,
    });
    // Type the replacement text
    await send('Input.insertText', { text });
  });
}
