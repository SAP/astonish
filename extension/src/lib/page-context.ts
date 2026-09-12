export type PageAdapter = 'github' | 'generic';

export type EditorKind = 'markdown' | 'plain' | 'none';

export type PageMapRegion = {
  name: string;
  status: string;
  notes?: string;
};

export type PageMap = {
  kind: string;
  regions: PageMapRegion[];
  actions: string[];
};

export type PageContext = {
  url: string;
  title: string;
  hostname: string;
  selection: string;
  mainText: string;
  editorPresent: boolean;
  editorKind: EditorKind;
  adapter: PageAdapter;
  pageMap?: PageMap;
};

const MAIN_TEXT_CAP = 50_000;

export function capMainText(text: string, cap = MAIN_TEXT_CAP): string {
  if (text.length <= cap) {
    return text;
  }
  return text.slice(0, cap);
}

export function formatPageMap(map?: PageMap): string {
  if (!map) {
    return '';
  }
  const lines = ['## Page map', `Kind: ${map.kind}`];
  for (const region of map.regions) {
    const notes = region.notes ? ` — ${region.notes}` : '';
    lines.push(`- ${region.name}: ${region.status}${notes}`);
  }
  if (map.actions.length) {
    lines.push('', 'Before acting:');
    for (const action of map.actions) {
      lines.push(`- ${action}`);
    }
  }
  return lines.join('\n');
}

export function retainPageDocument(previous: PageContext | undefined, next: PageContext): PageContext {
  if (!previous?.mainText.trim()) {
    return next;
  }
  if (next.mainText.trim().length >= previous.mainText.trim().length) {
    return next;
  }
  return { ...next, mainText: previous.mainText };
}

export function buildToolRoundContext(pageContextMarkdown: string, resultsMarkdown: string): string {
  return `${pageContextMarkdown}\n\n${resultsMarkdown}`;
}

export const EXTENSION_PAGE_TOOLS_INSTRUCTIONS = `## Chrome extension page tools
You are chatting from a Chrome side panel about THIS browser tab (URL/title above). The user is already signed in here. This is not a sandbox browser and not Studio's built-in browser.

This side panel is a full Astonish Studio chat surface with page context added. You have the same tenant-authorized Astonish skills and server-side tools as Studio chat; page tools are an additional source for THIS tab, not a restriction on other tools.

Use \`page_snapshot\`, \`page_query\`, \`page_click\`, \`page_fill\`, \`page_select\`, \`page_scroll\`, or \`page_navigate\` only when the task requires inspecting or interacting with THIS browser tab. Do not use backend browser/web tools to inspect this page or follow its links, because they do not have this tab's cookies and may reach a sign-in wall.

When the user says a request is unrelated to this page, do not inspect the page. Use \`search_tools\` to discover the relevant Astonish capability, call \`describe_tools\` before executing a discovered tool, and use \`execute_tool\` for deferred tools. Inspect Available Skills and call \`skill_lookup\` when a listed skill matches. Do not claim that a backend tool is unavailable, restricted, or unauthorized unless its actual Astonish tool result says so.

Read ## Page map first. It names the regions on this page (for a GitHub issue: the description vs the always-visible comment box) and how to edit them. Plan against that map before any tool call.

Page concept: identify the primary document versus secondary composers (comments, chat, search). To change the primary document, enter edit mode first if it is not already editable, then write. If the content cannot be edited, do not dump the update into a comment or search field — offer alternatives such as adding a comment if that field exists.

To inspect or interact with THIS tab, emit one or more fenced JSON blocks. The extension runs them locally in the tab and returns the results.

\`\`\`astonish-page-tool
{"name":"page_snapshot"}
\`\`\`

Available tools (name + args). Put arguments either nested ({"name":"page_click","args":{"ref":"ref3"}}) or at the top level ({"name":"page_click","ref":"ref3"}).
- page_snapshot — interactive elements with [refN] ids, plus the page map. Unfiltered snapshots omit elements after the first 400 and prefer in-viewport ones (typical on news homepages). Optional args: {"text":"visible text?","selector":"css?"} to keep only matches. Snapshots list controls, not the document — the issue/page body stays under ### Main content.
- page_query — find by CSS and/or visible text, then assign [refN] ids you can pass to page_click. Prefer this over a full snapshot when looking for one link or headline. Args: {"selector":"css?","text":"visible text?"}. Do not use this to re-read the issue description.
- page_click — click a ref from the latest snapshot or query in THIS tab. Args: {"ref":"ref3"}. Link clicks stay in this tab (the extension loads the href here even if a page overlay would swallow a real mouse click) and the tool result includes a fresh snapshot of the new page. Read that snapshot; do not web_fetch the href.
- page_navigate — load a URL in THIS tab, then snapshot. Args: {"url":"https://..."}. Relative paths are resolved against the current tab. Skip this when the URL is already this tab (the extension will not reload). Use it only when you need a different page in the user's browser session.
- page_fill — type into a snapshot ref. Args: {"ref":"ref4","text":"..."}. \`text\` is the literal field value. For a markdown editor (GitHub issue/PR body), that value is Markdown — headings, lists, links — not HTML and not a JSON object. On GitHub, never fill the new-comment box unless the user asked to comment.
- page_select — choose option(s). Args: {"ref":"ref5","values":["..."]}
- page_scroll — scroll. Args: {"ref":"ref2"} or {"y":800}

When rewriting an open text/markdown field, put the document in the tool args or in an astonish-page-edit fence. Never write a page-tool envelope into the page.

Correct page_fill (JSON string, newlines escaped):
\`\`\`astonish-page-tool
{"name":"page_fill","ref":"ref1","text":"### Heading\\n\\nBody"}
\`\`\`

Correct apply (raw Markdown, not JSON):
\`\`\`astonish-page-edit
### Heading

Body
\`\`\`

Wrong: \`{"ref":"ref1","text":"### Heading"}\` as the field value or as the astonish-page-edit body. \`ref\` is a tool argument, not page content.

On a GitHub issue/PR, the description and the comment box are different fields. If the description must change and it is not in edit mode, page_click Edit on the description to enter edit mode, then Apply or page_fill the issue-description field. Never write the update into the comment box.

If the description cannot be edited (no Edit control), offer alternatives such as adding a comment if a comment field is available — ask the user first. Do not click GitHub Comment / submit.

Backend tools (memory, cluster, services, and similar) remain available. Use \`web_fetch\` or \`browser_snapshot\` only when the user explicitly asks to fetch a URL outside this tab, or for a resource this tab cannot open.

For rewriting an open editor, prefer an astonish-page-edit fence so the user can click Apply. The fence body is the Markdown/plain document only.`;

export function buildSystemContext(ctx: PageContext): string {
  const lines = [
    '## Browser page context',
    `URL: ${ctx.url}`,
    `Title: ${ctx.title}`,
    `Site: ${ctx.hostname}`,
    `Adapter: ${ctx.adapter}`,
    `Editor present: ${ctx.editorPresent ? 'yes' : 'no'}`,
    `Editor kind: ${ctx.editorKind}`,
  ];
  const map = formatPageMap(ctx.pageMap);
  if (map) {
    lines.push('', map);
  }
  if (ctx.selection.trim()) {
    lines.push('', '### Selection', ctx.selection.trim());
  }
  if (ctx.editorKind === 'markdown' || ctx.adapter === 'github') {
    lines.push(
      '',
      'The main content is Markdown source for this page (GitHub issue/PR bodies are Markdown). When updating it, write Markdown — not HTML, not JSON. Preserve existing sections unless the user asked to replace them.',
    );
  }
  const contentLabel =
    ctx.editorKind === 'markdown' || ctx.adapter === 'github'
      ? '### Main content (markdown)'
      : '### Main content';
  lines.push('', contentLabel, ctx.mainText.trim() || '(empty)');
  lines.push('', EXTENSION_PAGE_TOOLS_INSTRUCTIONS);
  return lines.join('\n');
}
