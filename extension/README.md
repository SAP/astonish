# Astonish Chrome extension

Manifest V3 side panel that chats with Studio about the current page.

## Load unpacked

1. From the repository root, build the extension:

   ```bash
   make build-extension
   ```

   Equivalent: `cd extension && npm install && npm run build`.

2. Open `chrome://extensions`, enable **Developer mode**, click **Load unpacked**, and select `extension/dist`.

3. Click the Astonish toolbar icon. Chrome opens a native right-hand side panel with a Studio URL field, Sign in, and a chat input.

## Connect to Studio

- Local Studio: `http://localhost:9393` (or `http://127.0.0.1:9393`). Chrome will prompt for host permission on first Sign in.
- Remote Studio: paste the HTTPS origin and click **Sign in**. The extension lists SSO providers, opens the verify URL in a tab, and waits until you finish login in the browser — the same device-code flow as `astonish login --sso`.

The extension cannot reuse the Studio browser cookie (`astonish_access` is HttpOnly + SameSite=Strict). It does not collect email or password.

## Using the current page

Send includes the active tab URL/title/body as hidden `systemContext`. Selection, if any, is preferred in that payload.

When Astonish proposes a page rewrite in an `astonish-page-edit` fence, an **Apply to page** bar appears. Clicks write into the focused / visible editor. Nothing is written until Apply.

GitHub: Apply requires the issue or comment editor to already be open. v1 does not click Edit or Comment. If no editor is open, the text is copied and the panel asks you to open Edit, then Apply again.

`chrome://`, the Chrome Web Store, and PDF viewer tabs cannot be read.

Minimum Chrome version: 114.

v1 is unpacked-load only; it is not published to the Chrome Web Store.
