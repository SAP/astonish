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

Install-time permissions: `sidePanel`, `storage`, `activeTab`, `scripting`.

Optional hosts (user-granted on Sign in for Studio, on Send for the tab): `http://localhost/*`, `http://127.0.0.1/*`, `https://localhost/*`, `https://*/*`.

No install-time `host_permissions`, no `<all_urls>`, no `chrome.debugger`, no remotely hosted code.

## Out of scope (v1)

- Embedding the Studio SPA or stealing cookies
- CORS wildcards or new `/api/studio` routes
- Fleet, drills, apps, slides, report harness
- Chrome Web Store publication
- Automated GitHub form clicks
