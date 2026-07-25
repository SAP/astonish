package skills

// BuiltinGenerativeUI contains the complete Generative UI guidance that is
// delivered via skill_lookup("generative-ui"). This was previously stored in
// the vector store as a guidance document but is now a built-in skill for
// deterministic, full-context delivery — especially important for smaller models.

const BuiltinGenerativeUI = `# Generative UI (Visual Apps) — Complete Reference

When the user asks you to build a visual interface, create a UI, make a dashboard, build an app, or any request that implies a visual interactive component, you should generate a live-rendered React component.

## How to Create a Visual App

Wrap your React component code in a special code fence using ` + "the marker `astonish-app`" + `:

` + "````" + `
` + "```" + `astonish-app
import React, { useState } from 'react';
import { BarChart, Bar, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer } from 'recharts';
import { TrendingUp } from 'lucide-react';

export default function Dashboard() {
  const [period, setPeriod] = useState('week');
  const data = [
    { name: 'Mon', value: 40 },
    { name: 'Tue', value: 65 },
    { name: 'Wed', value: 55 },
    { name: 'Thu', value: 80 },
    { name: 'Fri', value: 45 },
  ];

  return (
    <div className="p-6 space-y-4">
      <div className="flex items-center gap-2">
        <TrendingUp className="w-5 h-5 text-brand" />
        <h1 className="text-xl font-bold text-app">Weekly Stats</h1>
      </div>
      <div className="flex gap-2">
        <button
          onClick={() => setPeriod('week')}
          className={` + "`px-3 py-1 rounded text-sm ${period === 'week' ? 'bg-brand text-white' : 'bg-surface-2 text-app-muted hover:text-app border border-app-border'}`" + `}
        >Week</button>
        <button
          onClick={() => setPeriod('month')}
          className={` + "`px-3 py-1 rounded text-sm ${period === 'month' ? 'bg-brand text-white' : 'bg-surface-2 text-app-muted hover:text-app border border-app-border'}`" + `}
        >Month</button>
      </div>
      <ResponsiveContainer width="100%" height={200}>
        <BarChart data={data}>
          <CartesianGrid strokeDasharray="3 3" stroke="#3d2450" />
          <XAxis dataKey="name" stroke="#b49bc3" />
          <YAxis stroke="#b49bc3" />
          <Tooltip contentStyle={{ background: '#1d0f28', border: '1px solid rgba(240,220,255,0.1)', borderRadius: '8px' }} />
          <Bar dataKey="value" fill="#ff6b9d" radius={[4,4,0,0]} />
        </BarChart>
      </ResponsiveContainer>
    </div>
  );
}
` + "```\n````" + `

The component will render live in the user's browser as an interactive preview.

## Available Libraries — ONLY These Exist

The sandbox has ONLY these libraries. Nothing else is available:

1. **React 19** — ` + "`import React, { useState, useEffect, useMemo, useCallback, useRef } from 'react'`" + `
2. **Tailwind CSS v4** — All utility classes work. Use ` + "`className`" + ` on HTML elements.
3. **Recharts** — ` + "`import { BarChart, Bar, LineChart, Line, PieChart, Pie, AreaChart, Area, RadarChart, Radar, ScatterChart, Scatter, XAxis, YAxis, ZAxis, CartesianGrid, Tooltip, Legend, ResponsiveContainer, Cell, PolarGrid, PolarAngleAxis, PolarRadiusAxis, ReferenceLine, Brush } from 'recharts'`" + `
4. **Lucide React icons** — ` + "`import { ArrowRight, Check, Settings, User, Heart, Star, Search, Menu, X, ChevronDown, Plus, Trash2, Edit, Download, Upload, Clock, Calendar, Mail, Phone, MapPin, Globe, Lock, Eye, Bell, Home, Folder, File, Image, Code, Terminal, Database, Server, Cloud, Zap, Sun, Moon, TrendingUp, TrendingDown, Activity, AlertCircle, Info, CheckCircle, XCircle, Filter, SortAsc, SortDesc, MoreHorizontal, ExternalLink, Copy, RefreshCw, Play, Pause, SkipForward, Volume2, Maximize2, Minimize2, ChevronRight, ChevronLeft, ChevronUp, ArrowUp, ArrowDown, ArrowLeft, RotateCcw, Bookmark, Share2, Send, Layers, Grid, List, BarChart3, PieChart as PieChartIcon, LineChart as LineChartIcon } from 'lucide-react'`" + `

## CRITICAL: What is NOT Available

There is NO component library. The following DO NOT EXIST in the sandbox:
- No ` + "`Button`" + `, ` + "`Card`" + `, ` + "`Input`" + `, ` + "`Badge`" + `, ` + "`Dialog`" + `, ` + "`Select`" + `, ` + "`Tabs`" + `, ` + "`Avatar`" + `, ` + "`Separator`" + ` — shadcn/ui does NOT exist
- No ` + "`@/components/*`" + ` or ` + "`@/lib/*`" + ` imports — there is no filesystem
- No Material UI, Chakra UI, Ant Design, Mantine, or any other component library
- **No ` + "`fetch()`" + `, ` + "`XMLHttpRequest`" + `, or ` + "`axios`" + `** — Network requests are BLOCKED in the sandbox. To get external data, use ` + "`useAppData`" + ` (see "Live Data" section below). NEVER use fetch() directly.
- No ` + "`lodash`" + `, ` + "`date-fns`" + `, ` + "`framer-motion`" + `, or any npm package not listed above
- No ` + "`next/image`" + `, ` + "`next/link`" + `, or any framework-specific imports

## How to Build UI Without a Component Library

Use native HTML elements styled with Tailwind. Follow the **Astonish App Canvas** design system so apps match Studio (brand pack + light/dark).

### App Canvas (light/dark + brand-pack aware)

The sandbox receives **light or dark** tokens for the active brand pack from Studio (same dual axis as the shell). Apps that use token classes retheme when the user toggles light/dark or switches brand packs.

Use **token classes** (` + "`bg-surface`" + `, ` + "`bg-brand`" + `, ` + "`text-app`" + `) so apps retheme automatically. Do **not** hard-code hex colors, gray backgrounds, or black/white text for surfaces. Do **not** implement your own theme toggle — the parent pushes mode.

### Color Palette (prefer these token classes)

Sandbox Tailwind tokens (use these instead of raw slate ` + "`bg-gray-900`" + `):

- **Outermost container:** transparent (NO bg-* class) — canvas shows through
- **Page/section cards:** ` + "`bg-surface border border-app-border rounded-xl`" + `
- **Inner elements (keys, inputs, nested):** ` + "`bg-surface-2 border border-app-border rounded-lg`" + `
- **Text hierarchy:** ` + "`text-app`" + ` (primary), ` + "`text-app-muted`" + ` (secondary/labels)
- **Primary actions:** ` + "`bg-brand text-white`" + ` (pack brand accent)
- **Secondary highlight:** ` + "`text-brand-strong`" + `
- **Tertiary accent:** ` + "`text-accent3`" + ` for secondary metrics
- **Error banner:** ` + "`bg-danger-soft border border-danger-border text-danger`" + ` (readable in light and dark)
- **Warning banner:** ` + "`bg-warning-soft border border-warning-border text-warning`" + `
- **Success / positive:** ` + "`bg-success-soft border border-success-border text-success`" + ` or ` + "`text-success`" + `

Avoid raw slate (` + "`bg-gray-900`" + `, ` + "`bg-gray-800`" + `, ` + "`text-gray-*`" + `) and default blue CTAs when brand tokens fit.
**Do not** use pastel status text alone on light backgrounds (` + "`text-red-400`" + `, ` + "`text-yellow-300`" + `, ` + "`text-amber-300`" + `) — they fail contrast in Studio light mode. Prefer the danger/warning/success tokens above.

### Standard Component Patterns

**Card:**
` + "`<div className=\"bg-surface rounded-xl p-4 border border-app-border\">...</div>`" + `

**Color-coded KPI / summary card:**
` + "```" + `jsx
<div className="bg-gradient-to-br from-brand/20 to-surface rounded-xl p-4 border border-brand/30">
  <p className="text-xs text-brand mb-1">Label</p>
  <p className="text-2xl font-bold text-app">$12,500</p>
  <p className="text-xs text-app-muted mt-1">Supporting text</p>
</div>
` + "```" + `
Use brand / accent3 / emerald / amber per card to distinguish metrics.

**Input with label (inside a card):**
` + "```" + `jsx
<div className="bg-surface rounded-xl p-3 border border-app-border">
  <label className="text-xs text-app-muted flex items-center gap-1 mb-1">
    <DollarSign className="w-3 h-3" /> Label
  </label>
  <input
    type="number"
    className="w-full bg-surface-2 text-app rounded-lg px-3 py-2 text-sm border border-app-border focus:border-brand focus:outline-none"
  />
</div>
` + "```" + `

**Button:** ` + "`<button className=\"px-4 py-2 bg-brand text-white rounded-lg hover:opacity-90 transition-opacity text-sm\">Click me</button>`" + `

**Subtle/secondary button:** ` + "`<button className=\"px-3 py-1.5 rounded-lg text-xs bg-surface-2 text-app-muted hover:text-app border border-app-border transition-colors\">Option</button>`" + `

**Badge:** ` + "`<span className=\"inline-flex items-center rounded-full border border-app-border bg-surface-2 px-2 py-0.5 text-xs text-app-muted\">Status</span>`" + `

**Select:** ` + "`<select className=\"px-3 py-2 bg-surface-2 border border-app-border rounded-lg text-app text-sm focus:border-brand focus:outline-none\"><option>...</option></select>`" + `

**Table:**
` + "```" + `jsx
<table className="w-full text-sm">
  <thead>
    <tr className="text-app-muted text-xs border-b border-app-border">
      <th className="text-left py-2 px-3">Name</th>
      <th className="text-right py-2 px-3">Amount</th>
    </tr>
  </thead>
  <tbody>
    <tr className="border-b border-app-border/50 hover:bg-surface-2/50">
      <td className="py-2 px-3 text-app">Row label</td>
      <td className="py-2 px-3 text-right text-success font-medium">$1,234</td>
    </tr>
  </tbody>
</table>
` + "```" + `
Right-align numeric columns. Use colored text for values and ` + "`font-medium`" + ` for emphasis.

**Header with icon:**
` + "```" + `jsx
<div className="flex items-center gap-3">
  <div className="p-2 bg-brand/20 rounded-lg">
    <Calculator className="w-6 h-6 text-brand" />
  </div>
  <div>
    <h1 className="text-2xl font-bold text-app">Title</h1>
    <p className="text-app-muted text-sm">Description text</p>
  </div>
</div>
` + "```" + `

**Tabs:**
` + "```" + `jsx
const [tab, setTab] = useState('overview');
<div className="flex border-b border-app-border">
  {['overview', 'details'].map(t => (
    <button key={t} onClick={() => setTab(t)}
      className={` + "`px-4 py-2 text-sm ${tab === t ? 'border-b-2 border-brand text-app' : 'text-app-muted hover:text-app'}`" + `}
    >{t}</button>
  ))}
</div>
` + "```" + `

**Info/explanation block:**
` + "`<div className=\"bg-surface/50 rounded-xl p-4 border border-app-border text-sm text-app-muted\">...</div>`" + `

**Calculator / keypad pattern (example):**
` + "```" + `jsx
<div className="max-w-xs mx-auto space-y-3">
  <div className="bg-surface rounded-xl p-4 border border-app-border text-right text-3xl tabular-nums text-app">0</div>
  <div className="grid grid-cols-4 gap-2">
    <button className="bg-surface-2 text-app rounded-xl py-3 border border-app-border">7</button>
    {/* … */}
    <button className="bg-brand text-white rounded-xl py-3">=</button>
  </div>
</div>
` + "```" + `

### Layout Principles

- Use ` + "`space-y-6`" + ` between major page sections
- Use ` + "`gap-3`" + ` within grids
- Use responsive grids: ` + "`grid grid-cols-2 md:grid-cols-3 lg:grid-cols-4 gap-3`" + ` (adjust column count to content)
- Wrap summary/KPI cards in a ` + "`grid grid-cols-2 md:grid-cols-4 gap-3`" + `
- Wrap control inputs in a ` + "`grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-3`" + `
- Use ` + "`tabular-nums`" + ` for numbers that change dynamically (counters, financial data)
- Typical page structure: header → controls → summary cards → charts → tables (adapt to the content; not all sections are needed)
- **Always include a Recharts visualization** (AreaChart, LineChart, BarChart, etc.) when the app involves numerical data, time series, growth projections, financial calculations, comparisons, or any data that benefits from a visual representation. Charts are a key strength of the sandbox — use them proactively. Chart strokes/fills: prefer brand pink ` + "`#ff6b9d`" + `, amber ` + "`#ffb86b`" + `, lilac ` + "`#b478ff`" + `; grid ` + "`#3d2450`" + `.

**If you need a reusable component, define it as a top-level function ABOVE the main export (NEVER nested inside):**
` + "```" + `jsx
function Badge({ children, variant = 'default' }) {
  const colors = { default: 'bg-surface-2 text-app-muted', success: 'bg-success-soft text-success border border-success-border' };
  return <span className={` + "`px-2 py-0.5 text-xs rounded-full ${colors[variant]}`" + `}>{children}</span>;
}

export default function App() {
  return <Badge variant="success">Online</Badge>;
}
` + "```" + `

## Rules for Generated Components

1. **Use ONLY native HTML elements** — ` + "`<div>`" + `, ` + "`<button>`" + `, ` + "`<input>`" + `, ` + "`<select>`" + `, ` + "`<table>`" + `, ` + "`<span>`" + `, ` + "`<a>`" + `, ` + "`<img>`" + `, ` + "`<form>`" + `, etc. Style them with Tailwind.
2. **Define helper components at the TOP LEVEL of the file, BEFORE the main export** — If you need a ` + "`Button`" + ` or ` + "`Card`" + ` abstraction, define it as a standalone function ABOVE the main component. **NEVER define helper components as nested functions inside the main component** — the sandbox cannot resolve them. Never import from non-existent modules.
3. **Export default** — Export your main component as the default export.
4. **Single file** — Everything must be in one file. Helper components and utility functions go at the top, the main ` + "`export default function`" + ` goes at the bottom.
5. **Self-contained** — Include all data, state, and logic within the component. Use hardcoded sample data for static apps; use ` + "`useAppData`" + ` for live data (see below).
6. **NEVER use fetch(), XMLHttpRequest, or axios** — The sandbox blocks direct network access. ALL external data MUST go through ` + "`useAppData('http:GET:<url>')`" + ` or ` + "`useAppData('mcp:<server>/<tool>')`" + `. This is the ONLY way to get external data. If the user gives you a URL or API endpoint, put it in the useAppData sourceId, e.g. ` + "`useAppData('http:GET:https://api.example.com/data')`" + `.
7. **App Canvas tokens** — **Do NOT set any background class (bg-*) on the outermost container** — it must be transparent so the Studio canvas shows through. Use App Canvas tokens (` + "`bg-surface`" + `, ` + "`bg-surface-2`" + `, ` + "`text-app`" + `, ` + "`bg-brand`" + `). Do not hard-code light-only or dark-only colors; one set of token classes works for both modes.
8. **Make it interactive** — Use ` + "`useState`" + ` for buttons, toggles, tabs, filters.
9. **Responsive** — Use responsive Tailwind classes (` + "`md:`" + `, ` + "`lg:`" + `) where appropriate.

## Live Data — useAppData & useAppAction

When the user asks for an app that displays live/dynamic data from external sources, use the built-in data hooks. These are available as **global functions** — no import needed (they are pre-injected in the sandbox).

### useAppData(sourceId, options?)

Fetches data from a backend source. Returns ` + "`{ data, loading, error, refetch }`" + `.

**sourceId convention:**
- ` + "`\"mcp:<serverName>/<toolName>\"`" + ` — Invokes an MCP tool. Example: ` + "`\"mcp:postgres-mcp/query\"`" + `
- ` + "`\"http:GET:<url>\"`" + ` — Makes an HTTP GET request. Example: ` + "`\"http:GET:https://api.example.com/data\"`" + `
- ` + "`\"http:POST:<url>\"`" + ` — Makes an HTTP POST request.
- ` + "`\"http:<METHOD>:<url>@<credential-name>\"`" + ` — Makes an HTTP request with authentication. The ` + "`@credential-name`" + ` suffix references a named credential from the Astonish credential store (configured in Settings > Credentials). The credential is resolved server-side and its auth header is injected into the request. Example: ` + "`\"http:GET:https://api.example.com/data@my-api-key\"`" + `

**options:**
- ` + "`args`" + ` — Object passed to the backend (MCP tool args, or HTTP body for POST).
- ` + "`args.headers`" + ` — Object of custom HTTP headers to include in the request. Example: ` + "`{ headers: { \"AI-Resource-Group\": \"default\" } }`" + `. These are applied alongside credential-based auth headers.
- ` + "`interval`" + ` — Polling interval in milliseconds. If set, the data auto-refreshes. Example: ` + "`30000`" + ` for 30 seconds.

**Example — MCP tool (database query):**
` + "```" + `jsx
export default function SalesTable() {
  const { data, loading, error } = useAppData('mcp:postgres-mcp/query', {
    args: { query: 'SELECT * FROM sales ORDER BY date DESC LIMIT 20' }
  });

  if (loading) return <div className="p-4 text-app-muted">Loading...</div>;
  if (error) return <div className="p-3 rounded-lg border border-danger-border bg-danger-soft text-danger text-sm">Error: {error}</div>;

  return (
    <table className="w-full text-sm text-app">
      <thead><tr className="border-b border-app-border">{/* ... */}</tr></thead>
      <tbody>{data?.rows?.map((row, i) => <tr key={i}>{/* ... */}</tr>)}</tbody>
    </table>
  );
}
` + "```" + `

**Example — HTTP API with dynamic URL and user input:**
` + "```" + `jsx
export default function WeatherApp() {
  const [city, setCity] = React.useState('Orlando');
  const [query, setQuery] = React.useState('Orlando');

  // sourceId changes when query changes → hook re-fetches automatically
  const url = ` + "`http:GET:https://wttr.in/${encodeURIComponent(query)}?format=j1`" + `;
  const { data, loading, error } = useAppData(url);

  return (
    <div className="p-6 space-y-4">
      <div className="flex gap-2">
        <input value={city} onChange={e => setCity(e.target.value)}
          className="flex-1 px-3 py-2 bg-surface-2 border border-app-border rounded-lg text-white"
          placeholder="Enter city..." />
        <button onClick={() => setQuery(city)}
          className="px-4 py-2 bg-brand text-white rounded-lg hover:opacity-90">
          Search
        </button>
      </div>
      {loading && <p className="text-app-muted">Loading...</p>}
      {error && <p className="p-2 rounded-lg border border-danger-border bg-danger-soft text-danger text-sm">{error}</p>}
      {data && <p className="text-2xl text-white">{data?.current_condition?.[0]?.temp_C}°C</p>}
    </div>
  );
}
` + "```" + `

**Example — Authenticated API using a credential:**
` + "```" + `jsx
export default function GitHubRepos() {
  const { data, loading, error } = useAppData('http:GET:https://api.github.com/user/repos@github-token');

  if (loading) return <div className="p-4 text-app-muted">Loading...</div>;
  if (error) return <div className="p-3 rounded-lg border border-danger-border bg-danger-soft text-danger text-sm">Error: {error}</div>;

  return (
    <div className="p-4 space-y-2">
      <h2 className="text-lg font-bold text-white">My Repos</h2>
      {data?.map(repo => (
        <div key={repo.id} className="p-2 bg-surface-2 rounded text-app">{repo.full_name}</div>
      ))}
    </div>
  );
}
` + "```" + `

**Example — Authenticated API with custom headers:**
` + "```" + `jsx
export default function AIDeployments() {
  const resourceGroup = 'default';
  const { data, loading, error } = useAppData(
    'http:GET:https://api.ai.prod.us-east-1.aws.ml.hana.ondemand.com/v2/lm/deployments?$top=50@sap-ai-core',
    { args: { headers: { "AI-Resource-Group": resourceGroup } } }
  );

  if (loading) return <div className="p-4 text-app-muted">Loading...</div>;
  if (error) return <div className="p-3 rounded-lg border border-danger-border bg-danger-soft text-danger text-sm">Error: {error}</div>;

  return (
    <div className="p-4 space-y-2">
      <h2 className="text-lg font-bold text-white">Deployments</h2>
      {data?.resources?.map(d => (
        <div key={d.id} className="p-2 bg-surface-2 rounded text-app">{d.name} — {d.status}</div>
      ))}
    </div>
  );
}
` + "```" + `

### useAppAction(actionId, options?)

Returns an async function for triggering write operations (mutations). Uses the same sourceId convention.

Optional second argument: ` + "`{ timeout }`" + ` in milliseconds (default ` + "`30000`" + `). Raise it only for known long-running MCP actions (for example Docker builds); leave the default otherwise.

` + "```" + `jsx
const runBuild = useAppAction('mcp:server/build', { timeout: 300000 }); // 5 min
` + "```" + `

**Example:**
` + "```" + `jsx
export default function TaskManager() {
  const { data, loading, refetch } = useAppData('mcp:postgres-mcp/query', {
    args: { query: 'SELECT * FROM tasks WHERE status != \'done\'' }
  });
  const markDone = useAppAction('mcp:postgres-mcp/query');

  async function handleComplete(taskId) {
    await markDone({ query: ` + "`UPDATE tasks SET status = 'done' WHERE id = ${taskId}`" + ` });
    refetch();
  }

  if (loading) return <div className="p-4 text-app-muted">Loading...</div>;
  return (
    <div className="p-4 space-y-2">
      {data?.rows?.map(task => (
        <div key={task.id} className="flex justify-between items-center p-2 bg-surface-2 rounded">
          <span className="text-white">{task.title}</span>
          <button onClick={() => handleComplete(task.id)} className="px-2 py-1 text-xs bg-green-600 text-white rounded">Done</button>
        </div>
      ))}
    </div>
  );
}
` + "```" + `

### When to use data hooks vs hardcoded data

- **Use hardcoded data** for mockups, prototypes, static dashboards, and demos where no external data source is mentioned.
- **Use useAppData** whenever the user mentions a URL, API endpoint, database, MCP server, or says things like "connect to", "fetch from", "query", "pull data from", "call this API", or provides any URL. The sourceId for HTTP APIs is simply ` + "`\"http:GET:<the-url>\"`" + ` — put the user's URL directly in the sourceId string.
- **Authenticated APIs** — If the API requires authentication, append ` + "`@credential-name`" + ` to the sourceId: ` + "`\"http:GET:https://api.example.com/data@my-api-key\"`" + `. The credential must exist in the Astonish credential store (Settings > Credentials). Ask the user for the credential name if they mention authentication. Credentials support API keys, Bearer tokens, Basic auth, and OAuth (client_credentials and authorization_code with auto-refresh).
- **Dynamic URLs** — If the URL contains a variable part (like a city name), construct the sourceId dynamically: ` + "`const { data, loading } = useAppData(` + \"`http:GET:https://api.example.com/${variable}`\" + `)`" + `. The hook re-fetches automatically when the sourceId string changes.
- **Ask the user** what MCP server/tool or HTTP endpoint to use if they request live data but don't specify the source.
- **NEVER use fetch() or XMLHttpRequest** — even if it seems simpler. The proxy is required for all external data.

## AI Capabilities — useAppAI

The ` + "`useAppAI`" + ` hook lets your component make one-shot LLM calls for tasks like summarization, classification, text generation, or analysis. It uses the same model configured for the Astonish agent.

### useAppAI(options?)

Returns an async function you call with a prompt. The LLM processes it and returns a text response.

` + "```" + `jsx
import { useAppAI } from 'astonish';

// Basic usage — returns a callable function
const askAI = useAppAI();
const summary = await askAI('Summarize this text: ...');

// With a system instruction — shapes the AI's behavior
const analyst = useAppAI({ system: 'You are a concise data analyst. Respond in bullet points.' });
const insights = await analyst('What trends do you see?', { context: salesData });
` + "```" + `

**Parameters:**
- ` + "`options.system`" + ` (optional string) — System instruction that shapes the AI's role/behavior

**The returned function signature:**
- ` + "`askAI(prompt, callOptions?)`" + ` → ` + "`Promise<string>`" + `
- ` + "`prompt`" + ` — The user's request text
- ` + "`callOptions.context`" + ` (optional) — Structured data to include as context (automatically serialized to JSON)

### Example: Summarize Button

` + "```" + `jsx
import { useState } from 'react';
import { useAppAI } from 'astonish';

export default function ArticleViewer() {
  const [article] = useState('The quarterly earnings report shows...');
  const [summary, setSummary] = useState(null);
  const [loading, setLoading] = useState(false);

  const summarize = useAppAI({ system: 'Summarize in 2-3 sentences. Be concise.' });

  const handleSummarize = async () => {
    setLoading(true);
    try {
      const result = await summarize('Summarize this article', { context: article });
      setSummary(result);
    } catch (err) {
      setSummary('Error: ' + err.message);
    }
    setLoading(false);
  };

  return (
    <div className="p-4">
      <p className="text-app">{article}</p>
      <button onClick={handleSummarize} disabled={loading}
        className="mt-4 px-4 py-2 bg-brand text-white rounded-lg disabled:opacity-50">
        {loading ? 'Summarizing...' : 'Summarize'}
      </button>
      {summary && <div className="mt-4 p-3 bg-surface-2 rounded-lg text-app">{summary}</div>}
    </div>
  );
}
` + "```" + `

### Example: Combining data + AI

` + "```" + `jsx
import { useState } from 'react';
import { useAppData, useAppAI } from 'astonish';

export default function SmartDashboard() {
  const { data: metrics, loading } = useAppData('http:GET:https://api.example.com/metrics');
  const [analysis, setAnalysis] = useState(null);
  const [analyzing, setAnalyzing] = useState(false);

  const analyze = useAppAI({ system: 'You are a business analyst. Identify anomalies and trends.' });

  const handleAnalyze = async () => {
    setAnalyzing(true);
    try {
      const result = await analyze('Analyze these metrics and highlight anomalies', { context: metrics });
      setAnalysis(result);
    } catch (err) {
      setAnalysis('Error: ' + err.message);
    }
    setAnalyzing(false);
  };

  if (loading) return <div>Loading data...</div>;
  return (
    <div className="p-4">
      {/* Render metrics charts/tables */}
      <button onClick={handleAnalyze} disabled={analyzing}
        className="px-4 py-2 bg-purple-600 text-white rounded-lg">
        {analyzing ? 'Analyzing...' : 'AI Analysis'}
      </button>
      {analysis && <div className="mt-4 p-3 bg-surface-2 rounded-lg whitespace-pre-wrap">{analysis}</div>}
    </div>
  );
}
` + "```" + `

### When to use useAppAI

- **Summarization** — "Add a button to summarize these results"
- **Classification** — Categorizing items, sentiment analysis, labeling data
- **Text generation** — Writing descriptions, generating reports, drafting responses
- **Analysis** — "Let me ask questions about this data", "Explain these metrics"
- **Any on-demand AI processing** triggered by user action in the app

**Tips:**
- Pass relevant data as ` + "`context`" + ` rather than embedding it in the prompt string — it gets serialized cleanly
- Set a ` + "`system`" + ` prompt to control response format and tone
- Show a loading state while waiting (the call may take a few seconds)
- Handle errors with try/catch — network or LLM failures throw an Error

## Persistent State — useAppState

The ` + "`useAppState`" + ` hook gives your component a per-app **SQLite database** for persistent structured data. Data survives page refreshes, app restarts, and server restarts. Each app gets its own isolated database file.

### useAppState()

**Import required:** ` + "`import { useAppState } from 'astonish';`" + `

Returns an object with two methods:
- ` + "`db.exec(sql, params?)`" + ` — Execute write/DDL SQL (CREATE, INSERT, UPDATE, DELETE). Returns ` + "`Promise<{ rowsAffected, lastInsertId }>`" + `.
- ` + "`db.query(sql, params?)`" + ` — Reactive read query. **Returns a rows array directly** — you can call ` + "`.map()`" + `, ` + "`.filter()`" + `, ` + "`.reduce()`" + ` on the result immediately. The array also has ` + "`.loading`" + ` and ` + "`.error`" + ` properties. Returns ` + "`[]`" + ` while loading. Automatically re-fetches when any ` + "`db.exec()`" + ` is called.

Both patterns work:
` + "```" + `jsx
// Pattern 1 — direct (recommended):
const rows = db.query('SELECT * FROM items');
rows.map(item => ...)     // works — rows IS the array
rows.loading              // boolean — true while fetching

// Pattern 2 — destructured:
const { data, loading } = db.query('SELECT * FROM items');
data.map(item => ...)     // also works
` + "```" + `

### Complete Example: Todo App

` + "```" + `jsx
import React, { useState, useEffect } from 'react';
import { useAppState } from 'astonish';

export default function TodoApp() {
  const db = useAppState();
  const [newTodo, setNewTodo] = useState('');

  // Create table on first load (idempotent)
  useEffect(() => {
    db.exec('CREATE TABLE IF NOT EXISTS todos (id INTEGER PRIMARY KEY AUTOINCREMENT, text TEXT NOT NULL, done INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)');
  }, []);

  // Reactive query — automatically re-runs after any db.exec()
  const todos = db.query('SELECT * FROM todos ORDER BY created_at DESC');

  const addTodo = async () => {
    if (!newTodo.trim()) return;
    await db.exec('INSERT INTO todos (text) VALUES (?)', [newTodo]);
    setNewTodo('');
  };

  const toggleDone = async (id, currentDone) => {
    await db.exec('UPDATE todos SET done = ? WHERE id = ?', [currentDone ? 0 : 1, id]);
  };

  const deleteTodo = async (id) => {
    await db.exec('DELETE FROM todos WHERE id = ?', [id]);
  };

  if (todos.loading) return <div className="p-4 text-app-muted">Loading...</div>;

  return (
    <div className="p-4 space-y-4">
      <div className="flex gap-2">
        <input value={newTodo} onChange={e => setNewTodo(e.target.value)}
          onKeyDown={e => e.key === 'Enter' && addTodo()}
          className="flex-1 bg-surface-2 border border-app-border rounded-lg px-3 py-2 text-app"
          placeholder="Add a todo..." />
        <button onClick={addTodo} className="px-4 py-2 bg-brand text-white rounded-lg">Add</button>
      </div>
      {todos.map(todo => (
        <div key={todo.id} className="flex items-center gap-3 p-3 bg-surface rounded-lg border border-app-border">
          <input type="checkbox" checked={!!todo.done} onChange={() => toggleDone(todo.id, todo.done)} />
          <span className={todo.done ? 'line-through text-app-muted' : 'text-app'}>{todo.text}</span>
          <button onClick={() => deleteTodo(todo.id)} className="ml-auto text-danger text-sm">Delete</button>
        </div>
      ))}
    </div>
  );
}
` + "```" + `

### Example: Inventory Tracker with Categories

` + "```" + `jsx
import React, { useState, useEffect } from 'react';
import { useAppState } from 'astonish';

export default function InventoryTracker() {
  const db = useAppState();
  const [name, setName] = useState('');
  const [category, setCategory] = useState('General');
  const [quantity, setQuantity] = useState(1);

  useEffect(() => {
    db.exec('CREATE TABLE IF NOT EXISTS items (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, category TEXT DEFAULT \'General\', quantity INTEGER DEFAULT 1, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)');
  }, []);

  const items = db.query('SELECT * FROM items ORDER BY category, name');
  const categories = db.query('SELECT DISTINCT category FROM items ORDER BY category');

  const addItem = async () => {
    if (!name.trim()) return;
    await db.exec('INSERT INTO items (name, category, quantity) VALUES (?, ?, ?)', [name, category, quantity]);
    setName('');
  };

  const updateQuantity = async (id, newQty) => {
    if (newQty <= 0) {
      await db.exec('DELETE FROM items WHERE id = ?', [id]);
    } else {
      await db.exec('UPDATE items SET quantity = ? WHERE id = ?', [newQty, id]);
    }
  };

  return (
    <div className="p-4 space-y-4">
      <div className="flex gap-2">
        <input value={name} onChange={e => setName(e.target.value)} placeholder="Item name"
          className="flex-1 bg-surface-2 border border-app-border rounded-lg px-3 py-2 text-app" />
        <input value={category} onChange={e => setCategory(e.target.value)} placeholder="Category"
          className="w-32 bg-surface-2 border border-app-border rounded-lg px-3 py-2 text-app" />
        <input type="number" value={quantity} onChange={e => setQuantity(+e.target.value)} min={1}
          className="w-20 bg-surface-2 border border-app-border rounded-lg px-3 py-2 text-app" />
        <button onClick={addItem} className="px-4 py-2 bg-emerald-600 text-white rounded-lg">Add</button>
      </div>
      {items.map(item => (
        <div key={item.id} className="flex items-center gap-3 p-3 bg-surface rounded-lg border border-app-border">
          <span className="text-app-muted text-sm">{item.category}</span>
          <span className="text-app">{item.name}</span>
          <div className="ml-auto flex items-center gap-2">
            <button onClick={() => updateQuantity(item.id, item.quantity - 1)} className="px-2 py-1 bg-surface-2 rounded text-app">-</button>
            <span className="text-app w-8 text-center">{item.quantity}</span>
            <button onClick={() => updateQuantity(item.id, item.quantity + 1)} className="px-2 py-1 bg-surface-2 rounded text-app">+</button>
          </div>
        </div>
      ))}
    </div>
  );
}
` + "```" + `

### When to use useAppState

- **Persistent data** — the user says "save", "remember", "store", "track", "keep a list of", "maintain", "log", "record"
- **CRUD apps** — todo lists, inventory, contacts, notes, bookmarks, wish lists
- **User-maintained datasets** — categories, tags, configuration, preferences
- **Collected results** — data gathered from APIs or AI that the user wants to keep

### useAppState Tips & Critical Rules

- Always use ` + "`CREATE TABLE IF NOT EXISTS`" + ` in a ` + "`useEffect(() => { ... }, [])`" + ` for schema setup — it runs once and is idempotent
- Use ` + "`INTEGER`" + ` for booleans (0/1) — SQLite has no native boolean type
- Always use parameterized queries (` + "`?`" + ` placeholders with params array) — NEVER string-concatenate user input into SQL
- ` + "`db.query()`" + ` is reactive — it automatically re-runs after any ` + "`db.exec()`" + ` call, so you don't need to manually refetch
- ` + "`db.query()`" + ` **returns a rows array directly** — you can call ` + "`.map()`" + `, ` + "`.filter()`" + `, ` + "`.reduce()`" + ` on the result immediately. It also has ` + "`.loading`" + ` and ` + "`.error`" + ` properties. Returns empty ` + "`[]`" + ` while loading (never null).
- ` + "`db.query()`" + ` is NOT a hook — it is a pure lookup. You can safely call it conditionally, inside helper functions, or in loops. Only ` + "`useAppState()`" + ` itself must be called at the component top level.
- **CRITICAL: NEVER call ` + "`db.exec()`" + ` inside a ` + "`useEffect`" + ` that depends on ` + "`db.query()`" + ` results** — this creates an infinite loop (exec triggers re-fetch, results change, effect fires, exec again). The ONLY safe ` + "`db.exec()`" + ` inside useEffect is schema creation with an empty dependency array ` + "`useEffect(() => { db.exec('CREATE TABLE IF NOT EXISTS ...') }, [])`" + `.
- Data persists across page refreshes and app restarts — it's stored in a SQLite database file on the server

### Common Mistakes with useAppState (DO NOT DO THESE)

` + "```" + `jsx
// ❌ WRONG — Using useAppState without import:
const db = useAppState(); // ERROR — useAppState requires: import { useAppState } from 'astonish'

// ❌ WRONG — Calling db.exec() in a useEffect that depends on query results:
const items = db.query('SELECT * FROM items');
useEffect(() => {
  if (items.length === 0) db.exec('INSERT INTO items ...'); // INFINITE LOOP!
}, [items]); // items changes → exec → refetch → items changes → exec → ...

// ❌ WRONG — String-concatenating user input into SQL:
await db.exec(` + "`INSERT INTO items (name) VALUES ('${name}')`" + `); // SQL INJECTION!
// ✅ CORRECT:
await db.exec('INSERT INTO items (name) VALUES (?)', [name]);

// ❌ WRONG — Treating db.query() like a Promise or async call:
const items = await db.query('SELECT * FROM items'); // NOT async! It's synchronous.
// ✅ CORRECT:
const items = db.query('SELECT * FROM items'); // Returns array immediately ([] while loading)
` + "```" + `

## When to Generate a Visual App

Generate a visual app when the user:
- Asks to "build", "create", "make", "design" a UI, dashboard, app, widget, form, chart, or page
- Asks for a visualization of data
- Requests an interactive tool (calculator, timer, converter, editor)
- Asks for a prototype or mockup
- Says "show me" something visual

Do NOT generate a visual app when:
- The user is clearly asking about code architecture, asking you to write backend code, or asking for explanations.
- The user asks for a **report**, **analysis**, **review**, or **document** — even if it includes diagrams, charts, or flows. Use the two-step report contract instead: ` + "`write_file`" + ` for the markdown file, then an ` + "`astonish-report`" + ` fence to signal it as a report (see "Structured Output & Reports" guidance).
- The output is primarily textual content with supporting visuals (architecture diagrams, process flows, data breakdowns). That is a markdown report with mermaid, not an app.

**Key distinction:** Use ` + "`astonish-app`" + ` when **interactivity** is needed (user input, live data, filtering, real-time updates). Use **markdown with mermaid** when the goal is a static document the user can read, export, and share.

## Iterative Refinement

After generating a visual app, the user may ask for modifications ("make the header blue", "add a search bar", "change the chart type"). When refining:

- **The current app source code will be provided in your system context** under "Active App Refinement". Use that as your starting point — do NOT re-invent the component from scratch.
- Output the COMPLETE updated component (not a diff or partial code).
- Keep all existing functionality unless explicitly asked to remove it.
- Wrap the updated code in the same ` + "`astonish-app`" + ` fence.
- Maintain the same component name and structure. Only change what the user asked for.
- If the user asks for something that conflicts with existing features, explain the tradeoff and implement their request.

**Important:** When you see "Active App Refinement" in your session context, the user is iterating on an existing app. You MUST:
1. Read the provided source code carefully
2. Apply ONLY the requested changes
3. Output the full updated component in an ` + "`astonish-app`" + ` fence
4. Do NOT add features the user didn't ask for
`
