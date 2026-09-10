# Chrome extension

Manifest V3 side panel that chats with Studio about the current browser tab. v1 is unpacked-load only (`extension/dist`); it is not published to the Chrome Web Store.

## Side panel UX

Clicking the Astonish toolbar icon opens Chrome's native right-hand `chrome.sidePanel` (minimum Chrome 114). The panel is a small local chat client:

- Logged out: Studio URL and Sign in. SSO opens the same device-code browser flow as `astonish login --sso`.
- Logged in: session picker (extension-created chats only), tab title/URL line, transcript, optional Apply-to-page bar, composer.

It does **not** iframe Studio Chat and does **not** import `web/src/components/StudioChat.tsx`. Approvals render as a notice plus a "Finish this in Studio" link to the configured Studio URL.

Follow-up messages reuse the `sessionId` from the SSE `session` event (`data.sessionId`, fallback `data.id`). That id is stored in `chrome.storage.local` (`astonishExtensionChat`) together with the list of sessions **this extension created**. Studio Chat still lists every session; the panel picker is filtered client-side to extension ids only. **New** clears the current id so the next Send mints a fresh chat. Reloading the panel restores the last-used extension session and its history.

## Three contexts

| Context | Role |
|---|---|
| Service worker (`extension/src/background/service-worker.ts`) | Opens the side panel on action click; injects the content script via `chrome.scripting.executeScript` on demand; forwards `MSG_GET_CONTEXT` / `MSG_APPLY` / `MSG_PAGE_TOOL`. Cannot touch the tab DOM. |
| Side panel (`extension/src/sidepanel/`) | Login, SSE chat, `systemContext` assembly, Apply confirmation, page-tool continuation. Runs as a `chrome-extension://` page. |
| Content script (`extension/src/content/`) | Captures page/selection (`capture.ts`, GitHub adapter), runs in-tab DOM tools (`dom-tools.ts`), and writes confirmed text into an open editor (`apply.ts`). |

There is no install-time `content_scripts.matches` for `<all_urls>`. Injection happens after the user opens the panel / clicks Send (`activeTab` plus optional host permission).

## Auth: Bearer `client_type=cli`

Studio Chat in the SPA uses an HttpOnly `SameSite=Strict` cookie (`astonish_access`). A `chrome-extension://` origin cannot send that cookie.

The extension authenticates the same way the CLI does:

- `POST /api/auth/login` with `{email, password, client_type: "cli", org?, team?}`
- Persist tokens in `chrome.storage.local` (refresh) and `chrome.storage.session` (live access token)
- `Authorization: Bearer <access>` plus `X-Astonish-Team` when a team slug is set
- On 401, `POST /api/auth/refresh` with `{refresh_token}` and retry once

It never reads `astonish_access` / `astonish_refresh` cookies.

Before the first fetch it requests `optional_host_permissions` for the Studio origin via `chrome.permissions.request`.

## Page context via `systemContext`

On Send, the side panel first requests optional host access for the active tab (`requestActiveTabHostPermission`, a user-gesture `chrome.permissions.request` for the declared optional hosts). Then it asks the service worker for `PageContext` (URL, title, hostname, selection, main text, adapter, editorPresent). `buildSystemContext` turns that into markdown, appends Chrome extension page-tool instructions, and `connectChat` puts it on `POST /api/studio/chat` as `systemContext`. The user's typed text stays in `message`.

If capture fails (`chrome://`, Chrome Web Store, PDF viewer, denied host access, or no script), the turn still sends with the page-tool instructions (so the model does not fall back to Studio's sandbox browser) and the panel shows a non-blocking banner.

This reuses `StudioChatRequest.systemContext` (`pkg/api/chat_handlers.go` / `pkg/api/chat_runner.go`). No new chat routes.

## Extension page tools (not sandbox browser tools)

Studio's `browser_*` tools drive a backend sandbox browser. They are the wrong surface for "which page am I on?" in the side panel.

The model is instructed **not** to call `search_tools`, `describe_tools`, `browser_snapshot`, `browser_navigate`, `web_fetch`, `http_request`, or other sandbox/backend browser tools to inspect this tab or follow links on it. Those run on the Studio backend (no tab cookies; authenticated pages fail). Backend tools such as memory and cluster lookup remain available. `web_fetch` / backend browser only when the user **explicitly** asks to fetch a URL outside this tab.

To inspect or click the open page the model emits fenced JSON:

````markdown
```astonish-page-tool
{"name":"page_snapshot"}
```
````

The panel parses those fences (`extractPageTools`), sends `MSG_PAGE_TOOL` through the service worker into the content script, and `runPageTool` executes against the live DOM:

Arguments may be nested (`{"name":"page_click","args":{"ref":"ref3"}}`) or top-level (`{"name":"page_click","ref":"ref3"}`). `page_query` assigns the same `[refN]` ids as `page_snapshot`, so a query can be followed by `page_click`. Unfiltered snapshots cap at 400 refs and prefer in-viewport elements; pass `text` or `selector` to keep later matches (e.g. a news ticker). Click failures go in `error`. If a page overlay swallows `element.click()` on a link, the service worker loads the href in this tab.

| Tool | Action |
|---|---|
| `page_snapshot` | Accessibility snapshot of visible interactive elements with `[refN]` ids. Optional `text` / `selector` filters. |
| `page_query` | Find by CSS selector and/or visible text (also matches href), then assign `[refN]` ids for `page_click`. |
| `page_click` | Click a snapshot or query ref in **this** tab (refuses GitHub Edit/Comment). `_blank` is forced to `_self`. If the href leaves the document, the service worker waits for this tab to finish loading, re-injects the content script, and returns a fresh snapshot. Hash-only / same-document links do not wait. Overlay-swallowed link clicks still navigate this tab via the href. |
| `page_navigate` | `chrome.tabs.update` this tab to a URL, wait for load, re-inject, snapshot. Same-tab only — never opens a new tab. Relative paths resolve against the current tab URL. |
| `page_fill` | Type into a snapshot ref |
| `page_select` | Choose `<select>` option(s) |
| `page_scroll` | Scroll a ref into view or the window |

After `page_click` / `page_navigate`, the tool result includes the new page snapshot and tells the model to read it instead of calling `web_fetch` on the href.

Results are returned as a hidden follow-up turn (`systemContext` + a short continuation `message`), up to 8 rounds. Fences are stripped from the transcript the user sees.

### Iframe support

Page tools support both same-origin and **cross-origin** iframes via a frame coordinator architecture.

#### Same-origin iframes (direct document access)

For same-origin iframes, the top-frame content script accesses `iframe.contentDocument` directly via `getAccessibleDocuments()` in `dom-tools.ts`. Interactive elements from same-origin iframes appear in `page_snapshot` and `page_query` results tagged with `(iframe: <src>)`. Cross-origin `contentDocument` access throws `SecurityError` and is silently skipped at this level.

#### Cross-origin iframes (frame coordinator pattern)

Cross-origin iframes require a different approach since `contentDocument` is inaccessible. The extension uses a **coordinator/worker** pattern:

1. **Injection:** The content script is injected into every frame (`allFrames: true` in `chrome.scripting.executeScript` and the manifest) including cross-origin iframes.

2. **Frame detection:** Each content script instance checks `window.self === window.top` to determine if it's the coordinator (top frame) or a worker (child frame).

3. **Message relay:** The service worker fans out `MSG_FRAME_TOOL` messages to each frame individually using `chrome.tabs.sendMessage(tabId, ..., { frameId })`. Each frame executes the page tool against its own `document` via `runPageToolInFrame()` and returns a `FrameToolResult`.

4. **Ref offsets:** Each frame receives a `refOffset` so its ref numbers don't collide with other frames (e.g. top frame uses ref1–ref10, child frame uses ref11–refN).

5. **Result aggregation:** The service worker's `mergeFrameResults()` combines interactive elements and headings from all frames. For action tools (`page_click`, `page_fill`), the first successful result is used.

6. **Timeout handling:** Frames that don't respond within 5 seconds are silently skipped.

**Example on the SAP Fiori CAT2 calendar page:**
- Top frame = SAP Launchpad shell (header, navigation buttons)
- Child frame (`sapit-finance-prod-eagle.launchpad.cfapps.eu10.hana.ondemand.com/cat2ui/...`) = Activity Recording calendar

When `page_snapshot` runs, the service worker sends `MSG_FRAME_TOOL` to both the top frame and the CAT2 iframe. The CAT2 frame's content script queries its own document for calendar cells and returns them. The merged snapshot includes both the launchpad controls and the calendar day cells with sequential ref IDs.

**Key files:**
- `extension/src/background/service-worker.ts` — `getAllFrameIds()`, `sendToFrame()`, `mergeFrameResults()`, `runPageToolAllFrames()`
- `extension/src/content/dom-tools.ts` — `runPageToolInFrame()`, `collectCandidatesFrameLocal()`, `snapshotFrameInteractive()`, `snapshotFrameHeadings()`
- `extension/src/content/content-script.ts` — frame detection (`isTopFrame`), `MSG_FRAME_TOOL` handler
- `extension/src/lib/messages.ts` — `MSG_FRAME_TOOL`, `FrameToolPayload`, `FrameToolResult` types
- `extension/manifest.json` — `webNavigation` permission (for `getAllFrames`)

### CDP-based input dispatch

`page_click` and `page_fill` use the **Chrome DevTools Protocol** (`chrome.debugger` API) to dispatch real browser input events instead of synthetic DOM events. This ensures compatibility with modern web frameworks (SAPUI5, Angular, React) that rely on Chrome's full input pipeline for event handling.

**Flow:**
1. Content script resolves the ref, computes the element's tab-absolute bounding rect via `getElementTabRect()` (accounts for iframe nesting), and returns it alongside the result.
2. Service worker opens an ephemeral `chrome.debugger` session (attach → commands → detach).
3. For clicks: dispatches `Input.dispatchMouseEvent` (mouseMoved → mousePressed → mouseReleased) at the element's center coordinates.
4. For fills: dispatches a click to focus, Ctrl+A to select all, then `Input.insertText` with the text. The content script also sets `.value` and fires `input`/`change` events as a belt-and-suspenders approach for React/Angular.

**Fallback:** If `chrome.debugger` is policy-blocked or the element has a zero bounding rect (hidden/off-screen), the content script falls back to synthetic DOM `MouseEvent` dispatch and `element.click()`.

**Key files:**
- `extension/src/background/cdp-input.ts` — `cdpClick()`, `cdpFill()`, `withDebugger()` helper
- `extension/src/content/dom-tools.ts` — `getElementTabRect()`, updated `click()` and `fill()` returning rects
- `extension/src/background/service-worker.ts` — CDP dispatch after `runPageToolAllFrames`

## Confirmed apply

The model may propose a page rewrite in a fenced block with language id `astonish-page-edit` (`PAGE_EDIT_FENCE`). The panel parses the latest agent text (`extractPageEdit`) and shows **Apply** / **Dismiss**. DOM writes never run on stream complete.

On Apply, `MSG_APPLY { text, mode: "replace" }` goes to the content script, which only ever writes into an **already-open** editor:

- Prefer the focused textarea/contenteditable
- Else a visible GitHub issue-body textarea (never the comment box as a substitute for the description)
- Else the first visible textarea/contenteditable
- Set `value` / `textContent` and dispatch bubbling `input` + `change` so React/GitHub editors notice

### Two-step handshake when the field is not in edit mode

The extension never auto-clicks Edit and never writes the GitHub comment box as a fallback. If no editor is open for the target, `applyToPage` returns a typed result `{ ok: false, reason: 'not-editable', error }` instead of a bare string. The panel then turns the Apply bar into a **loud blocking prompt**: it shows a highlighted message ("This field is not in edit mode. Click Edit on the page to open the editor, then press Apply again."), keeps the proposed content pinned in the preview, relabels the primary button to **Apply again**, and writes/copies nothing. The user opens Edit on the page themselves and presses **Apply again**; now that the editor exists, the content is written and the bar hides. Pressing Apply again while the field is still read-only simply re-shows the same prompt — there is no clipboard fallback and no comment-box write on this path. **Dismiss** clears the bar and discards the pending edit.

Thrown channel errors (not the not-editable path) still fall back to copying the text to the clipboard and showing a status line. v1 never auto-clicks GitHub Edit / Comment and never submits forms.

## Permission model

Install-time permissions: `sidePanel`, `storage`, `activeTab`, `scripting`, `debugger`.

Optional hosts (user-granted on Sign in for Studio, on Send for the tab): `http://localhost/*`, `http://127.0.0.1/*`, `https://localhost/*`, `https://*/*`.

No install-time `host_permissions`, no `<all_urls>`, no remotely hosted code.

## Out of scope (v1)

- Embedding the Studio SPA or stealing cookies
- CORS wildcards or new `/api/studio` routes
- Fleet, drills, apps, slides, report harness
- Chrome Web Store publication
- Automated GitHub form clicks

## Security and trust model

### Model-driven input and auto-run limit

Once the user grants host access to a tab's origin, the model can invoke `page_click` / `page_fill` / `page_navigate` against the live tab using CDP input events (`chrome.debugger`). These actions run without per-action user confirmation, up to `MAX_PAGE_TOOL_ROUNDS = 15` consecutive rounds. The user sees the results in the transcript but does not approve each individual action.

Acceptance criteria for this design:
- The user explicitly grants host access on a per-turn basis via `requestActiveTabHostPermission` (a gesture-gated `chrome.permissions.request`).
- Chrome's "started debugging this browser" banner is displayed for the duration of each CDP session.
- `isUnreadableUrl` blocks `chrome:`, `chrome-extension:`, Web Store, and `.pdf` targets.
- `withDebugger` always detaches in a `finally` block.
- The 15-round limit caps runaway tool loops.

### Host permission scope (`https://*/*`)

`https://*/*` is listed in `optional_host_permissions` — it is **not** granted at install time. The extension requests it in two situations:

1. **Studio origin** — requested during login so it can reach the configured Studio URL (which may be any HTTPS host).
2. **Active tab** — requested from a user gesture when the user clicks Send and the model needs page context or page tools.

The breadth (`https://*/*`) is required because the Studio host and the active tab host are both user-configurable and not known at build time. Narrowing to a fixed set of origins would block valid use cases. The optional / gesture-driven pattern is the correct mitigation; per-origin grants on each Send would be more restrictive but would require a permission prompt on every tab switch.

### `chrome.debugger` permission

`debugger` is declared as an install-time permission because `chrome.permissions.request` cannot add the `debugger` permission dynamically (Chrome enforces this). Its use is narrowly scoped: `cdpClick()` and `cdpFill()` in `cdp-input.ts` open an ephemeral session, dispatch the input events, and detach in a `finally`. CDP is only invoked after the user has already granted host access to the tab.

### Token storage

The `AuthSession` (including `accessToken` and `refreshToken`) is persisted to `chrome.storage.local` (persistent, unencrypted on disk). The `accessToken` is additionally mirrored to `chrome.storage.session` (cleared on browser close). `clearSession()` removes both.

Trade-off: the refresh token survives browser restarts so the user does not need to log in again, at the cost of the token being at rest unencrypted in the Chrome profile directory. This is the standard extension authentication pattern. The threat model is a compromised local user account or a malicious extension update — not a web page (web page JavaScript cannot read `chrome.storage` belonging to another extension).

### Service worker message sender validation

`chrome.runtime.onMessage` only receives messages from contexts inside this extension (side panel, content scripts). It does not register `chrome.runtime.onMessageExternal`, so web pages cannot reach the listener. As a defence-in-depth measure the listener validates `sender.id === chrome.runtime.id` at the top and rejects any message from an unexpected sender before touching the CDP dispatch path.
