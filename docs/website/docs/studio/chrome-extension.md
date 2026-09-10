# Chrome Extension

The Astonish Chrome extension brings Studio chat directly into any browser tab through Chrome's native side panel. While browsing the web you can ask questions about the current page, fill forms, click elements, and run agent tasks — all without leaving the tab you're on.

## Requirements

- Chrome 114 or later (any Chromium-based browser that supports `chrome.sidePanel`)
- A running Astonish instance (local or cloud) reachable from your browser
- The extension loaded in Chrome (see [Installation](#installation))

## Installation

The extension is distributed as a zip file alongside each release binary on the [GitHub Releases page](https://github.com/SAP/astonish/releases). It is not published to the Chrome Web Store; you load it as an unpacked extension.

1. **Download** `astonish-extension.zip` from the latest release.
2. **Unzip** it into a permanent folder (e.g. `~/astonish-extension`). Do not delete this folder — Chrome requires it to remain present while the extension is loaded.
3. Open `chrome://extensions` in Chrome.
4. Enable **Developer mode** (toggle in the top-right corner).
5. Click **Load unpacked** and select the unzipped folder.
6. The Astonish icon appears in the Chrome toolbar.

To update the extension, download the new zip, overwrite the folder contents, and click the **↺ refresh** button on the extension card at `chrome://extensions`.

## Signing In

Click the Astonish toolbar icon to open the side panel. The first time you open it you will see the sign-in screen.

1. Enter your **Studio URL** (e.g. `http://localhost:9393` for a local instance, or your team's cloud URL).
2. Enter your **email** and **password**.
3. Click **Sign in**. The extension authenticates using the same bearer-token flow as the CLI (`astonish login`) — it does not use browser cookies.

After sign-in, Chrome will ask you to grant the extension permission to reach your Studio URL. Click **Allow** to continue.

::: tip Team accounts
If your Studio URL serves a multi-tenant platform, the extension signs into the default team for your account. Session isolation from Studio chat is maintained automatically.
:::

## Using the Side Panel

Once signed in, the side panel shows:

| Area | Description |
|------|-------------|
| **Session picker** | Dropdown listing all sessions you started from the extension. Select one to resume a previous conversation. |
| **Page context bar** | The current tab's title and URL, shown at the top of the chat. |
| **Transcript** | Conversation history, with assistant text and tool-call groups displayed inline. |
| **Composer** | Text input area. Press Enter or click Send. |

### Starting a New Session

Click **New chat** in the session picker to start a fresh conversation. Your previous sessions remain accessible in the picker.

### Page Context

When you send a message, the extension automatically captures the current tab's URL, title, and visible page content and attaches it to your message as context. The model can read the page and reason about it without you having to copy-paste anything.

If the page is restricted (Chrome settings pages, the Chrome Web Store, PDFs, or a page where you have not granted host access), a banner appears in the side panel. You can still chat — the model just won't have page content.

::: tip Granting page access
The first time you send a message on a given site, Chrome may ask you to grant the extension access to that site. This is a per-site permission and only takes effect while the panel is open for that tab.
:::

## Page Tools

The agent can interact with the current tab's live DOM — no backend sandbox or headless browser required. These tools run inside the actual Chrome tab you're looking at, including content inside cross-origin iframes.

| Tool | What it does |
|------|--------------|
| `page_snapshot` | Returns an accessibility snapshot of visible interactive elements in the tab, including elements inside iframes. |
| `page_query` | Finds elements by CSS selector or visible text and assigns reference IDs. |
| `page_click` | Clicks an element using real browser input events (works with React, Angular, SAPUI5). |
| `page_fill` | Types into a form field. |
| `page_select` | Selects option(s) in a `<select>` dropdown. |
| `page_navigate` | Navigates the tab to a URL and returns a fresh snapshot. |
| `page_scroll` | Scrolls an element into view. |

The model uses these tools by emitting structured instructions in its responses, which the extension intercepts and executes. Tool results appear as collapsed groups in the transcript so they don't clutter the conversation.

::: info Auto-run limit
To prevent runaway tool loops, the extension caps consecutive page-tool rounds at 15. After 15 rounds the agent stops and reports what it accomplished.
:::

## Applying Text to the Page

When the agent suggests content to insert into an editor (a form field, a GitHub issue body, a comment box), it appears in a highlighted **Apply** bar at the bottom of the side panel.

- Click **Apply** to write the content into the focused editor on the page.
- Click **Dismiss** to discard the proposed content without writing anything.

If the target field is not yet in edit mode (for example, a GitHub issue body that hasn't been opened for editing), the panel shows a prompt: _"This field is not in edit mode. Click Edit on the page to open the editor, then press Apply again."_ The content stays pinned until you click Apply a second time or dismiss it.

## Session Isolation

Extension sessions are stored separately from Studio Chat sessions. The extension session picker shows only sessions you created from the extension; Studio's session list shows only sessions created in Studio. This prevents the extension's page-focused conversations from cluttering your Studio history and vice versa.

Sessions are identified by a dedicated `appName` (`astonish-extension`). Opening the side panel on a new tab automatically loads all your extension sessions from the server — you don't need to be on the same tab where a session was originally started.

## Sign Out

Click the **Sign out** button in the side panel to clear your credentials from Chrome storage. The extension will return to the sign-in screen. Your sessions are preserved on the server and available again after you sign back in.

## Troubleshooting

### "Could not reach Studio"

Check that your Studio instance is running and that the URL you entered (e.g. `http://localhost:9393`) is correct. If you are using a local instance, make sure the daemon is started:

```bash
astonish daemon start
```

### "Permission denied" or page context not captured

Go to `chrome://extensions`, find Astonish, and click **Details**. Under **Site access**, set it to "On all sites" or individually allow the sites you want the extension to access. Alternatively, grant access site-by-site when prompted on first use.

### Page tools not working on a specific site

Some enterprise sites (SAP Fiori, Salesforce, etc.) use multiple nested iframes. The extension supports same-origin and cross-origin iframes via a frame coordinator, but iframes served from a `chrome://` origin or blocked by the browser's extension policies cannot be reached. If a page tool fails, the agent reports the error in the transcript and tries an alternative approach.

### Extension icon not visible

If the Astonish icon is not visible in the toolbar, click the puzzle-piece Extensions icon and pin Astonish.
