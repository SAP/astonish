# Docs — Slides (Web Component Presentations)

> **Status**: Planned — revised proposal
> **Category**: Productivity Tools
> **First doc type**: Slides
> **Future doc types**: Documents (rich text), Spreadsheets (structured data)
> **Last research review**: 2026-08-21

## Overview

Astonish Docs enables the agent to create, refine, present, and export professional presentations from natural language. Slides are authored as a **versioned, declarative Web Component document** and rendered live in Studio. The same semantic source compiles to:

- an interactive web presentation;
- a deterministic PDF;
- a standalone HTML deck; and
- a `.pptx` whose supported text, shapes, images, tables, and charts are **native editable PowerPoint objects**.

The canonical source is not arbitrary rendered DOM and is not a screenshot. It is a constrained component tree with explicit geometry and typed data. Web and PowerPoint are separate render targets of that tree.

## Goals

1. Give the LLM a compact, composable slide vocabulary instead of unconstrained HTML/CSS.
2. Make the web deck standards-based and framework-neutral through Custom Elements.
3. Preserve native editability in PowerPoint wherever a semantic mapping exists.
4. Make export behavior predictable, inspectable, and testable.
5. Allow richer web-only components without pretending they can round-trip to PowerPoint.
6. Preserve existing Astonish tenant scoping, Studio SSE behavior, and sandbox boundaries.

## Non-goals

- A general HTML/CSS/JavaScript-to-PowerPoint converter.
- Pixel-identical rendering for arbitrary CSS and native PowerPoint objects at the same time.
- Executing arbitrary scripts supplied by the model.
- Embedding live Web Components as ordinary, portable PowerPoint shapes. PowerPoint can host HTML through an Office content add-in, but that is an add-in runtime, not native slide content, and is not the default export.
- Full PowerPoint animation, SmartArt, or macro support in V1.
- Manual WYSIWYG editing or bidirectional PPTX import in V1.

---

## Key Architectural Decision

### One semantic source, two renderers

```text
                         LLM AGENT
         create_deck / write_slide / read_slide / validate_deck
                                │
                                ▼
                ASTONISH SLIDES DOCUMENT (ASD v1)
        versioned custom-element markup + typed properties/data
                                │
                 parse → validate → normalized scene graph
                                │
                    ┌───────────┴───────────┐
                    ▼                       ▼
             WEB RENDERER              PPTX RENDERER
       Custom Elements + theme       native object compiler
       interactive components        PptxGenJS → OOXML
                    │                       │
       Studio / Presenter / HTML      editable .pptx
                    │
             Chromium print
                    │
                   PDF
```

The **normalized scene graph** is the contract. The browser DOM is a rendering output and measurement aid, not the export source of truth. Exporters never scrape arbitrary DOM and guess its meaning.

### Why not arbitrary HTML as the canonical format?

| Concern | Arbitrary HTML/CSS | Constrained Web Component document |
|---|---|---|
| LLM authoring | Familiar but easy to produce invalid or unexportable effects | Small vocabulary, schema-guided arguments |
| Web rendering | Maximum flexibility | Rich enough for presentation layouts |
| PPTX editability | Meaning must be inferred from pixels/DOM | Component type selects native PPT object |
| Validation | Mostly syntactic | Semantic, geometry, overflow, and capability checks |
| Themes | CSS can affect geometry unpredictably | Tokens have defined web and PPT mappings |
| Evolution | Undocumented conventions | Versioned schema and migrations |
| Security | Large HTML/CSS/script attack surface | Allowlisted elements, attributes, URLs, and styles |

The restricted vocabulary is a feature: it establishes the subset that can be represented consistently in both CSS and PresentationML.

---

## Technology Decision

### Public authoring model: Astonish Custom Elements

Use autonomous Custom Elements with an `ast-` prefix:

- `<ast-deck>`
- `<ast-slide>`
- `<ast-text>`
- `<ast-shape>`
- `<ast-image>`
- `<ast-table>`
- `<ast-chart>`
- `<ast-group>`
- `<ast-code>`
- `<ast-icon>`
- `<ast-fragment>` for web presentation sequencing
- `<ast-notes>` for speaker notes

Implement reactive elements with **Lit** where useful. Lit components remain standard Custom Elements; the serialized deck must not depend on React or Lit-specific syntax.

Keep meaningful content and machine-readable data in attributes/light DOM. Shadow DOM is allowed for renderer internals and controls, but must not hide the only copy of exportable text or chart/table data.

### Presentation runtime

Build a small Astonish-owned deck controller around `<ast-deck>` and `<ast-slide>`. It owns:

- keyboard, pointer, and touch navigation;
- fullscreen and overview modes;
- URL/deep-link state;
- fragment sequencing;
- presenter view and notes;
- lifecycle events such as `ast-slide-enter` and `ast-slide-leave`;
- deterministic print mode.

Do not make a third-party runtime part of the stored format.

#### Framework evaluation

| Option | Finding | Decision |
|---|---|---|
| Lit | Active, standards-based Custom Element library; no slide runtime | Use as the implementation helper for Astonish components |
| OddBird `<slide-deck>` | The closest modern true Web Component deck; small and pre-1.0; no first-class export | Use as design reference or prototype behind an adapter, not as a persisted contract |
| reveal.js | Mature presentation runtime and excellent PDF workflow; not Web Component-based | Keep as a possible runtime adapter if the owned controller becomes too costly |
| Slidev | Mature Vue/Markdown product with strong export; PPTX export is image-oriented | Do not use as the canonical model |
| WebSlides | HTML/CSS framework, not Web Components; limited recent maintenance and export ownership | Use only as visual/layout inspiration |
| DeckDeckGo | Valuable Web Component prior art, but archived | Do not depend on it |

A capability-first fallback may project `<ast-slide>` content into reveal.js `<section>` elements. Reveal-specific classes and configuration must remain inside an adapter.

---

## Canonical Slide Format

### Example

```html
<ast-deck schema="1" ratio="16:9" theme="light-corporate" lang="en-US">
  <ast-slide id="risk" title="Migration risk by service">
    <ast-text
      id="title"
      role="title"
      x="96" y="72" w="1728" h="100"
      font-token="display" size="54" weight="700">
      Migration risk by service
    </ast-text>

    <ast-table
      id="risk-table"
      x="96" y="220" w="1120" h="690"
      data-ref="risk-data"
      header="true"
      style-token="data-table">
    </ast-table>

    <ast-chart
      id="risk-chart"
      kind="bar"
      x="1280" y="250" w="544" h="560"
      data-ref="risk-chart-data"
      category-key="service"
      value-keys="probability,impact">
    </ast-chart>

    <ast-shape
      id="callout"
      kind="roundRect"
      x="1280" y="840" w="544" h="120"
      fill-token="accent-soft" line-token="accent">
      <ast-text inset="24" size="24">Prioritize checkout and identity.</ast-text>
    </ast-shape>

    <ast-notes>
      Explain that probability is delivery risk, while impact is customer impact.
    </ast-notes>

    <script type="application/json" id="risk-data">
      {"columns":["Service","Probability","Impact","Mitigation"],"rows":[["Checkout","High","High","Parallel run"]]}
    </script>
    <script type="application/json" id="risk-chart-data">
      {"rows":[{"service":"Checkout","probability":85,"impact":95}]}
    </script>
  </ast-slide>
</ast-deck>
```

`<script type="application/json">` is inert data, not executable script. The parser permits this exact use and rejects executable script types.

### Geometry

- Logical canvas: **1920 × 1080** for 16:9.
- Coordinates are integer logical pixels in source and convert to PowerPoint inches using `x / 160`, because a 12 × 6.75 inch widescreen slide maps to 1920 × 1080 units.
- All exportable top-level components have explicit `x`, `y`, `w`, and `h`.
- Groups establish a local coordinate space.
- Web preview scales the fixed canvas; it does not reflow at responsive breakpoints.
- Components may perform internal layout only where the same algorithm is implemented by the PPTX renderer. Otherwise children require explicit geometry.

### Content and style rules

- Use theme tokens for fonts, colors, line styles, spacing, and reusable component styles.
- Permit a small typed property set, not arbitrary inline CSS.
- Rich text is represented as explicit semantic runs, not unbounded nested HTML.
- Images reference managed assets by ID; remote URLs must be ingested through the existing SSRF-protected asset path before export.
- Charts and tables retain source data rather than only an SVG or canvas rendering.
- Stable element IDs are mandatory for diagnostics, incremental updates, and future import/regeneration.
- Accessibility fields include `lang`, reading order, `alt`, decorative state, and optional pronunciation/description metadata.

### Theme contract

A theme contains target-neutral tokens plus optional renderer-specific overrides:

```json
{
  "schema": 1,
  "name": "light-corporate",
  "colors": {
    "surface": "#FFFFFF",
    "ink": "#172033",
    "ink-muted": "#64748B",
    "accent": "#1E40AF",
    "accent-soft": "#DBEAFE"
  },
  "fonts": {
    "display": {"family": "Aptos Display", "fallback": ["Arial"]},
    "body": {"family": "Aptos", "fallback": ["Arial"]},
    "mono": {"family": "Consolas", "fallback": ["Courier New"]}
  },
  "styles": {
    "data-table": {"headerFill": "accent", "headerText": "#FFFFFF"}
  }
}
```

Theme changes must not alter component geometry unless a full validation/render pass succeeds. Font availability is recorded in export diagnostics.

---

## Component Export Contract

Every registered component implements the equivalent of:

```ts
interface SlideComponentDefinition {
  tagName: string
  schema: JSONSchema
  renderWeb(node: SlideNode, context: WebRenderContext): HTMLElement
  renderPptx(node: SlideNode, context: PptxRenderContext): Promise<void>
  validate(node: SlideNode, context: ValidationContext): Diagnostic[]
  fallback: 'native' | 'svg' | 'raster' | 'error'
}
```

The actual registry may be split between TypeScript and Go-generated metadata, but the schemas and capability names must come from one versioned source.

### Native mapping matrix

| Component | Web renderer | PPTX output | Editability | V1 fallback |
|---|---|---|---|---|
| `ast-text` | HTML text | native text box and text runs | Full | Error on unsupported text effect |
| `ast-shape` | CSS/SVG | native PowerPoint AutoShape/line | Full for mapped shapes | SVG, then raster |
| `ast-image` | `<img>` | picture with crop/contain geometry | Image-level | Raster is already the native representation |
| `ast-table` | semantic HTML table | native PowerPoint table | Cells and formatting | Raster only if feature is explicitly marked unsupported |
| `ast-chart` | web chart renderer | native PowerPoint chart with workbook data | Data, series, common formatting | SVG/raster for unsupported chart types |
| `ast-group` | positioned container | native group where supported | Child objects remain editable | Flatten group transform, not children |
| `ast-code` | highlighted HTML | native text runs with monospace font | Text editable | Raster only for unsupported highlighting |
| `ast-icon` | inline SVG | SVG picture | Picture-level; user may use PowerPoint “Convert to Shape” | PNG compatibility fallback |
| `ast-fragment` | staged visibility | final-state objects | Objects editable; sequencing omitted | Emit warning |
| custom web-only component | Custom Element | none | None | Explicit component raster plus warning |

### Fidelity tiers

- **Tier A — native**: PowerPoint text, shape, image, table, chart, group, or notes object.
- **Tier B — vector**: SVG picture; scalable but not decomposed into native objects.
- **Tier C — raster**: component-level PNG fallback.
- **Tier D — unsupported**: export fails in strict mode.

The export response and deck manifest record counts and diagnostics by tier. A deck advertised as “editable” must meet a configurable native threshold, for example 95% of non-image content by area at Tier A.

A whole-slide screenshot is allowed only as an explicit `fidelity=visual` export mode, never as the default editable PPTX path.

---

## PPTX Export Pipeline

### Selected implementation

Use **PptxGenJS** as the initial native PPTX generation engine. It generates standard OOXML and supports native text, shapes, images, tables, common charts, slide masters, and speaker notes in browser/Node environments.

PptxGenJS is a drawing/generation API, not a general HTML renderer. Astonish supplies the semantic mapping layer. Its HTML import helper is table-focused and is not the architecture for arbitrary components.

The Go server invokes a pinned, bundled Node worker rather than building a broad custom OOXML writer in Go. The Phase 0 spike selected a **controlled Node subprocess** for V1: Node is already required by the supported build environment, the JSON-over-stdio protocol is narrow and versioned, and the exporter boundary keeps a future embedded JavaScript runtime possible without changing the slide service. Production startup must verify the pinned worker checksum and Node compatibility before advertising PPTX export.

The supported-font baseline is Aptos Display/Aptos with Arial fallback and Consolas with Courier New fallback. V1 accepts solid fills, solid lines, mapped AutoShapes, plain/rich text runs, native tables, and common native charts. Unsupported filters, blend modes, complex masks, SmartArt, and animations are rejected in strict mode or use a declared component-level fallback. The default "editable" label requires at least **95% Tier A coverage by non-image component area**, zero Tier D components, and no whole-slide raster fallback.

```text
Go Slides service
  1. authorize and load tenant-scoped deck/assets
  2. parse + migrate ASD source
  3. validate and build normalized scene graph JSON
  4. invoke versioned PPTX worker with bounded resources
  5. receive pptx bytes + export diagnostics
  6. validate package and return artifact

Node PPTX worker
  1. load scene graph and theme
  2. create widescreen presentation/master/layouts
  3. dispatch each node to its registered PPTX renderer
  4. attach speaker notes and accessibility metadata where supported
  5. package OOXML with PptxGenJS
```

The worker must be reproducibly bundled and checksum-pinned. The initial implementation pins `pptxgenjs` 4.0.1 in `web/package-lock.json` and runs `pkg/docs/slides/pptxworker/worker.mjs` with a 30-second default deadline over protocol version 1. It receives no tenant credentials and no unrestricted network access. Assets are provided as validated local files/data by the Go service.

### Phase 0 spike evidence

The representative native-object test generates a widescreen package containing editable text, an AutoShape, a native table, a native chart relationship/part, and speaker notes. It unzips the result and fails if the default slide contains a picture object, establishing that the editable path is not a whole-slide screenshot. This gate runs in `go test ./pkg/docs/slides/...`; broader visual comparison and Open XML SDK validation remain Phase 3 acceptance requirements.

### Deterministic export rules

- Resolve all assets and fonts before compilation.
- Record exporter, schema, and theme versions in document metadata.
- Use stable object order and IDs derived from component IDs.
- Convert geometry directly from logical units; do not depend on viewport size.
- Use explicit text-box dimensions and margins.
- Run overflow detection before export. Do not silently shrink text below theme minimums.
- Convert chart/table data semantically, not from their rendered pixels.
- Treat browser measurements as optional diagnostics only. If a component requires browser measurement, serialize the measured values into the normalized graph and mark the component non-deterministic until validated.

### PowerPoint constraints

Web and PowerPoint rendering engines differ in font metrics, line breaking, gradients, shadows, clipping, masks, blending, filters, and transforms. Therefore:

- define an **export-safe style subset**;
- reject or downgrade unsupported effects explicitly;
- compare semantic layout and visual output in tests;
- never promise arbitrary CSS fidelity and complete native editability simultaneously.

SVG inserted into PowerPoint is a picture, not automatically a set of editable PowerPoint shapes. Microsoft 365 may offer “Convert to Shape” interactively, but that is lossy and is not an Astonish round-trip guarantee.

### Live web content in PowerPoint

A PowerPoint **content Office Add-in** can host HTML/JavaScript in a slide. It depends on an add-in manifest, a supported Office host, policy/trust, and usually hosted HTTPS content. The object remains an add-in surface rather than editable text/shapes/charts.

This may become a separate enterprise export profile for live demos. It is not used by standard `.pptx` export and must have a static fallback.

---

## PDF and Standalone HTML Export

### PDF

Render the print mode of the same Web Component deck in Chromium through the existing go-rod infrastructure:

1. load the complete deck at a fixed 1920 × 1080 logical canvas;
2. await `customElements.whenDefined()` for all registered tags;
3. await `document.fonts.ready`, asset readiness, and an `ast-render-complete` signal;
4. select a fragment policy (`final` by default, optionally one page per step);
5. print one slide per landscape page with backgrounds and no margins.

PDF is the visual-fidelity target. PPTX is the native-editability target. Their renderings should be close, but they are not produced by the same layout engine.

### Standalone HTML

Bundle:

- serialized ASD markup;
- the versioned component runtime;
- theme tokens/styles;
- sanitized, content-addressed assets;
- presenter/navigation code;
- a strict Content Security Policy.

Default exports are self-contained and do not make network requests. Custom interactive components must declare and package their runtime dependencies.

---

## Storage and Domain Model

Continue using scoped `DocsStore` implementations and Ent storage for personal/team contexts. Tenant routing remains mandatory.

Store the canonical deck or slide markup, schema version, normalized metadata, and assets—not complete per-slide HTML documents generated for a specific runtime.

Representative fields:

```go
type DeckManifest struct {
    ID            string            `json:"id"`
    Slug          string            `json:"slug"`
    Title         string            `json:"title"`
    Description   string            `json:"description,omitempty"`
    Version       int               `json:"version"`
    SchemaVersion int               `json:"schemaVersion"`
    Theme         ThemeInfo         `json:"theme"`
    Dimensions    Dimensions        `json:"dimensions"`
    Slides        []SlideInfo       `json:"slides"`
    Assets        []AssetInfo       `json:"assets"`
    Metadata      map[string]any    `json:"metadata,omitempty"`
    CreatedAt     time.Time         `json:"createdAt"`
    UpdatedAt     time.Time         `json:"updatedAt"`
}

type SlideContent struct {
    ID            string       `json:"id"`
    Position      int          `json:"position"`
    Title         string       `json:"title"`
    SchemaVersion int          `json:"schemaVersion"`
    Markup        string       `json:"markup"` // one <ast-slide> document fragment
    Notes         string       `json:"notes"`
    Diagnostics   []Diagnostic `json:"diagnostics,omitempty"`
}
```

The Ent slide field should be named `component_markup` (or neutral `source_content`), not `html_content`. Theme data should be structured JSON plus optional audited extension tokens, not unrestricted CSS.

Schema changes require edits in every applicable Ent scope, generated output, and a migration in the same implementation change.

### Versioning and migration

- Every deck carries `schemaVersion`.
- Readers migrate old versions into the current normalized graph without mutating stored source.
- Writers emit only the current version.
- Persisting a migrated deck is an explicit operation.
- Component removals require a migration or a stable legacy renderer.

---

## Agent Tools

| Tool | Purpose | Key arguments |
|---|---|---|
| `create_slides` | Create a deck and theme | `title`, `theme`, `description?`, `ratio?` |
| `write_slide` | Write/replace one `<ast-slide>` fragment | `deckSlug`, `slideIndex`, `markup`, `notes?` |
| `read_slide` | Return source plus diagnostics | `deckSlug`, `slideIndex` |
| `validate_slides` | Validate without persisting | `deckSlug?`, `markup?`, `targets` |
| `inspect_slide` | Return normalized nodes and export capabilities | `deckSlug`, `slideIndex` |
| `update_slide_notes` | Update speaker notes | `deckSlug`, `slideIndex`, `notes` |
| `reorder_slides` | Change ordering | `deckSlug`, `order` |
| `delete_slide` | Remove a slide | `deckSlug`, `slideIndex` |
| `update_theme` | Replace tokenized theme values | `deckSlug`, `themeName` or `theme` |
| `list_slides_decks` | List visible decks | none |

`write_slide` must return validation diagnostics and an export capability summary. The LLM should call `validate_slides` for complex tables, charts, or custom components before finalizing a deck.

### Prompt rules

The model receives generated documentation for the registered component vocabulary. Core rules:

1. Emit exactly one `<ast-slide>` fragment per `write_slide` call.
2. Use only registered tags and properties.
3. Give every object a stable ID and explicit geometry.
4. Use theme tokens; do not emit CSS or executable JavaScript.
5. Store table/chart data semantically.
6. Prefer Tier A components. Use web-only components only when the user values interactivity over editable PPTX.
7. Read before modifying and validate before finalizing.
8. Keep text within declared boxes and minimum font sizes.

---

## Web UI and User Journey

Web Components are the deck's internal authoring and rendering model; users do not need to write component markup. V1 remains an **AI-first viewer and structured workspace**, not a free-form PowerPoint-style editor.

### Create a deck in chat

1. The user asks for a presentation in natural language and may specify audience, length, tone, required visuals, and PowerPoint editability.
2. The agent creates a deck, chooses a theme, and writes slides incrementally using registered components.
3. On the first `docs_update`, Studio inserts a `SlidesCard` into the conversation.
4. As slides arrive, the card updates its count and preview. Users can inspect completed slides while generation continues.
5. Validation and PowerPoint capability summaries update with each slide.

```text
┌────────────────────────────────────────────────────────┐
│ Microservices Migration                      3 / 10    │
│                                                        │
│             Live Web Component preview                 │
│                                                        │
├────────────────────────────────────────────────────────┤
│ ◀ Previous          ● ● ● ○ ○ ○          Next ▶      │
│                                                        │
│ PowerPoint: Native 9 · Vector 1 · Raster 0             │
│ [Open deck]       [Present]       [Export ▾]           │
└────────────────────────────────────────────────────────┘
```

The preview uses the real Web Component runtime, not an export screenshot. Preview controls can disable pointer interaction in the compact chat card while the full viewer permits registered interactive components.

### Refine conversationally

The user can request changes such as:

- “Make slide 4 less crowded.”
- “Replace the list with an editable comparison table.”
- “Use a formal light theme.”
- “Keep this interactive on the web, but use the current chart state in PowerPoint.”

The agent reads the canonical slide, patches stable component IDs, validates it, and refreshes the preview. Structured table/chart data can be changed without rewriting arbitrary HTML.

### Open deck workspace

```text
┌──────────────┬────────────────────────────────────┬──────────────────┐
│ Slides       │                                    │ Deck details     │
│              │                                    │                  │
│ [thumbnail]  │       Selected slide preview       │ Theme            │
│ [thumbnail]  │                                    │ Speaker notes    │
│ [thumbnail]  │       Web Component runtime        │ Validation       │
│ [thumbnail]  │                                    │ PPTX capability  │
│              │                                    │                  │
├──────────────┴────────────────────────────────────┴──────────────────┤
│ Ask AI: “Turn this slide into a timeline and keep it PPTX-native.”   │
└─────────────────────────────────────────────────────────────────────┘
```

V1 workspace actions:

- navigate, reorder, duplicate, and delete slides;
- select a slide as context for the agent;
- inspect and edit speaker notes;
- switch approved themes;
- view validation, overflow, missing-asset, and export-capability warnings;
- enter presenter mode;
- export PDF, standalone HTML, or PPTX.

Direct manipulation of arbitrary canvas objects is deferred. A later structured editor may expose forms and drag/resize operations, but every change must update the canonical component model rather than mutate rendered DOM.

### Presenter mode

Presenter mode opens a dedicated full-screen web deck and provides:

- keyboard, touch, and pointer navigation;
- audience and presenter windows;
- speaker notes, progress, and slide overview;
- fragments and registered web interactions;
- fullscreen and second-display behavior.

The web presentation may be richer than PowerPoint. Each interactive component must define its static PowerPoint state. For example, an interactive chart may export the currently selected/default data view as a native chart; a simulation may export an SVG or PNG with a warning.

### Export experience

Before downloading a PPTX, Studio shows a compatibility summary:

```text
PowerPoint compatibility: Partial

✓ 18 text elements export as editable text
✓ 6 shapes and connectors export as editable objects
✓ 1 table exports as an editable table
✓ 2 charts export as editable charts with data
⚠ Interactive architecture explorer exports as SVG

[Cancel] [Export strict-native] [Export with fallback]
```

- **Export strict-native** fails if any Tier B–D component is present.
- **Export with fallback** permits declared SVG/PNG substitutions and lists them in the result.
- **Visual-fidelity PPTX** is a separate opt-in whole-slide-image profile and is never labeled editable.
- PDF export prioritizes browser visual fidelity.
- Standalone HTML preserves registered interaction and presenter behavior.

### Primary V1 story

```text
User requests a deck
  → SlidesCard appears and updates during generation
  → user previews and asks for revisions in chat
  → user opens the deck workspace for navigation, notes, and diagnostics
  → user presents through the Web Component runtime
  → user exports native-first PPTX, visual PDF, or interactive HTML
```

---

## Backend and Frontend Integration

### Package shape

```text
pkg/docs/slides/
├── types.go                   # manifest, source, diagnostics, capabilities
├── service.go                 # scoped business logic
├── parser.go                  # ASD markup → normalized graph
├── validation.go             # schema, geometry, overflow, security
├── migrations/               # source schema migrations
├── tools.go                  # agent tools
├── export_pdf.go             # Chromium print orchestration
├── export_html.go            # standalone component bundle
├── export_pptx.go            # worker invocation and package checks
├── components/               # schemas and target-neutral metadata
├── themes/                   # embedded tokenized themes
└── pptxworker/               # embedded, pinned JS bundle and protocol

web/src/components/docs/slides/
├── runtime/                   # ast-* Custom Elements and deck controller
├── SlidesCard.tsx            # in-chat host
├── SlidesViewer.tsx          # full viewer
├── SlideNavigator.tsx
├── PresenterMode.tsx
└── SlidesExport.tsx
```

New UI files use TypeScript/TSX. The Web Component runtime is framework-neutral; React hosts it rather than owning slide semantics.

### API

```text
GET    /api/docs?type=slides
GET    /api/docs/slides/{deckSlug}
GET    /api/docs/slides/{deckSlug}/slides/{idx}
POST   /api/docs/slides/validate
POST   /api/docs/slides/{deckSlug}/export/pdf
POST   /api/docs/slides/{deckSlug}/export/pptx
POST   /api/docs/slides/{deckSlug}/export/html
GET    /api/docs/slides/{deckSlug}/present
DELETE /api/docs/slides/{deckSlug}
GET    /api/docs/slides/themes
GET    /api/docs/slides/components
```

The slide endpoint returns either the source fragment as data or a sandboxed runtime page. It must not concatenate untrusted source into the Studio document.

### SSE

Preserve the existing ChatRunner drain pattern: tools capture pending document updates, `ChatRunner` emits `docs_update`, Studio consumes the matching event, and persisted markers reconstruct the card after reload.

Add optional fields without changing the event name:

```json
{
  "type": "slides",
  "deckSlug": "...",
  "action": "slide_written",
  "slideIndex": 4,
  "totalSlides": 10,
  "title": "Migration risk",
  "schemaVersion": 1,
  "validation": {"errors": 0, "warnings": 1},
  "pptxCapability": {"native": 9, "vector": 1, "raster": 0, "unsupported": 0}
}
```

Backend emitters, Studio consumers, persisted parsing, terminal behavior, and scenario fixtures must change together when this contract is implemented.

---

## Security

- Parse source into an allowlisted AST; never execute author markup while validating.
- Reject unknown elements, event-handler attributes, executable scripts, arbitrary `style`, unsafe URL schemes, foreign objects, and unsanitized SVG.
- Treat inert JSON blocks as data with a size limit and schema validation.
- Ingest remote assets through SSRF-protected fetching with MIME, size, redirect, and address validation.
- Render previews in a sandboxed opaque-origin iframe, consistent with the generative UI boundary.
- Give the PPTX worker no network and no tenant credentials; use resource/time limits.
- Escape all text sent to XML-generating libraries and validate resulting packages.
- Apply tenant scope at every store and export lookup.

---

## Validation and Test Strategy

### Source conformance

- schema tests for each component and theme;
- parser fuzzing and hostile markup fixtures;
- geometry bounds, overlap, and reading-order checks;
- text overflow using approved font metrics;
- missing asset/data reference checks;
- capability diagnostics for every export target.

### Web rendering

- Custom Element unit tests and accessibility checks;
- deterministic screenshots at fixed Chromium/font versions;
- presenter navigation, fragments, notes, fullscreen, and print-mode tests;
- standalone export tested offline under its CSP.

### PPTX structure

For every fixture deck:

1. unzip and inspect expected slide, media, notes, chart, and relationship parts;
2. confirm text is represented by text shapes, tables by table markup, and charts by chart parts rather than a slide-sized image;
3. validate OOXML using the Open XML SDK validator in CI or a pinned validation container;
4. open/save smoke-test in supported PowerPoint versions where CI infrastructure permits;
5. open in LibreOffice Impress as a secondary interoperability signal, not the authority for PowerPoint fidelity;
6. verify speaker notes, hyperlinks, alt text, reading order, masters, and theme values;
7. compare rendered slide images from PowerPoint/LibreOffice with web reference images using tolerances and component masks.

### Acceptance gates

- No whole-slide image in default editable export.
- All Tier A fixture objects remain individually selectable/editable after PowerPoint open/save.
- No source validation errors.
- No unexpected Tier C/D downgrade.
- Text must not clip or overflow in the supported font environment.
- Package validation has zero errors.

---

## Dependencies

### Go

Continue using existing Go infrastructure for stores, APIs, go-rod PDF rendering, asset handling, and worker orchestration. Do not implement a broad PresentationML writer in Go for V1.

### Web/runtime

- `lit` — Custom Element implementation helper (BSD-3-Clause).
- optional deck-runtime adapter kept behind Astonish interfaces.

### PPTX worker

- `pptxgenjs` — native OOXML generation (MIT).
- bundled with the application build; no runtime `npm install`.

Adding a Node-authored build artifact does not turn the distributed product into multiple binaries: the worker can be bundled and invoked through an available JS runtime strategy selected during implementation. Before committing to subprocess Node in production, prototype and choose among:

1. embedded worker executed by a small bundled JS runtime;
2. a Node subprocess available in the controlled server image;
3. browser-side PptxGenJS generation for trusted, already-loaded scene graphs.

The exporter interface must isolate this choice. Browser-side export reduces server dependencies but can expose large assets and produce device-dependent behavior; the server-side worker is preferred for deterministic platform export.

---

## Implementation Phases

### Phase 0 — Fidelity spike and decision gate (1 week)

- Implement five representative slides: title, rich text, shapes/connectors, table, and chart/image.
- Render with prototype `ast-*` components.
- Export with PptxGenJS to native objects.
- Test PowerPoint open/edit/save and compare visual output.
- Decide worker runtime and document supported fonts/effects.
- Exit only when native-object and package-validation gates pass.

### Phase 1 — Component model and web runtime (2–3 weeks)

- Define ASD v1 schemas, parser, normalized graph, diagnostics, and migrations.
- Implement core `ast-deck`, `ast-slide`, `ast-text`, `ast-shape`, `ast-image`, `ast-group`, and `ast-notes`.
- Implement deck navigation, presenter view, print mode, theme tokens, and offline bundle.
- Add validation tools and generated model guidance.
- Add scoped storage schemas/implementations and migrations.

### Phase 2 — Astonish integration (2 weeks)

- Add deck/slide tools and conditional prompt guidance.
- Add APIs and sandboxed slide serving.
- Wire `docs_update` through agent capture, ChatRunner drain, persistence, Studio consumer, terminal parity review, and scenario fixtures.
- Add Docs list, SlidesCard, viewer, navigation, presenter, and error states.

### Phase 3 — Native PPTX vertical slices (2–3 weeks)

- Build the exporter protocol and PptxGenJS worker.
- Map text, shape, image, group, notes, theme, and master components.
- Add table and common native chart mappings.
- Add SVG and component-raster fallback with diagnostics.
- Add OOXML validation, native-object assertions, and PowerPoint smoke fixtures.

### Phase 4 — PDF, HTML, and production hardening (1–2 weeks)

- Add deterministic go-rod PDF export.
- Add self-contained HTML export and CSP.
- Add asset ingestion, font checks, time/resource limits, caching, and export observability.
- Add strict/native and visual-fidelity export profiles.

### Future

- Additional chart types, connectors, animations, and transitions where both targets support them.
- Optional Office content add-in export for explicitly live enterprise decks.
- Version history, collaboration, templates, and manual editing.
- PPTX import into ASD with explicit loss diagnostics.
- Theme ingestion from corporate `.pptx` templates.

---

## Risks

| Risk | Impact | Mitigation |
|---|---|---|
| Web and PowerPoint text engines wrap differently | High | Fixed boxes, approved fonts, overflow checks, PowerPoint render tests |
| LLM requests unsupported CSS/effects | Medium | Schema-generated prompt, allowlist, strict validation, explicit fallback tiers |
| Custom components silently flatten | High | Capability metadata, export diagnostics, strict mode, native-area acceptance gate |
| PptxGenJS lacks a required PresentationML feature | Medium | Exporter abstraction; targeted OOXML post-processing or commercial SDK evaluation only when justified |
| Node/JS worker complicates one-binary distribution | Medium | Phase 0 runtime spike, embedded bundle, isolated protocol, browser option for local mode |
| PowerPoint and LibreOffice differ | Medium | PowerPoint is authoritative; LibreOffice is secondary compatibility testing |
| Fonts unavailable on recipient machine | High | Approved font set, fallback declaration, preflight warning; investigate licensed embedding later |
| Arbitrary asset URLs create SSRF/privacy risk | High | Managed asset ingestion and offline export |
| Office add-in is mistaken for portable native content | Medium | Keep it a separate opt-in profile and always provide static fallback |
| Component/schema evolution breaks old decks | High | Versioned source, migrations, legacy renderers, fixture corpus |

---

## Technical Decisions Log

| Decision | Choice | Rationale |
|---|---|---|
| Canonical source | Versioned `ast-*` Custom Element markup | Human/LLM-readable web format with explicit export semantics |
| Internal contract | Normalized semantic scene graph | Decouples source syntax, web DOM, and PPTX library |
| Web implementation | Astonish components, Lit where useful | Standards-based and framework-neutral without making Lit syntax persistent |
| Runtime | Small owned controller behind an adapter | Avoids coupling persisted decks to a third-party framework |
| Geometry | Fixed 1920 × 1080 logical canvas | Deterministic 16:9 mapping to 12 × 6.75 inch PowerPoint |
| Styling | Typed theme tokens and export-safe properties | Predictable dual-target behavior and stronger security |
| PPTX engine | PptxGenJS behind an exporter protocol | Best open-source balance of native objects, notes, charts, tables, and browser/Node support |
| PPTX default | Native-first component compilation | Meets PowerPoint editability requirement |
| Fallback | Per-component SVG/raster with diagnostics | Preserves the rest of the slide as editable objects |
| Screenshot deck | Explicit visual-fidelity mode only | Useful escape hatch without mislabeling output as editable |
| PDF | Chromium print of Web Components | Web rendering is the visual-fidelity target |
| Live HTML in PPTX | Separate optional Office add-in profile | Add-ins are hosted runtime surfaces, not native portable shapes |
| Storage | Tenant-scoped database and managed assets | Preserves Astonish scope invariants |
| Security | Parsed allowlisted DSL in sandboxed iframe | Web Components do not justify arbitrary model-authored script execution |

---

## Research Summary and Primary Sources

Research conducted for this revision found:

1. **WebSlides is not based on Web Components.** It is a conventional HTML/CSS/JavaScript presentation framework. Its design examples remain useful, but it should not define a new architecture.
2. **Lit is the strongest standards-based component foundation**, but it is not a presentation runtime or exporter.
3. **reveal.js is the mature runtime and PDF benchmark**, but its canonical slide structure is `<section>`-based rather than Custom Elements. It remains a viable adapter.
4. **OddBird `<slide-deck>` is promising true-Web-Component prior art**, but it is pre-1.0 and lacks a complete export pipeline.
5. **PptxGenJS is suitable for native PPTX generation**, but does not convert arbitrary DOM/CSS. Its HTML conversion is primarily for tables. Semantic application mapping is required.
6. **PPTX cannot contain arbitrary live Web Components as ordinary native shapes.** Office content add-ins can host web UI, but introduce deployment, trust, network, and portability constraints.
7. **SVG is normally inserted as a picture.** PowerPoint may let a user convert suitable SVG to shapes, but that is not a lossless round trip.

### Primary references

- Lit component model: <https://lit.dev/docs/components/overview/>
- Lit repository and BSD-3-Clause license: <https://github.com/lit/lit>
- OddBird slide-deck: <https://github.com/oddbird/slide-deck>
- reveal.js markup: <https://revealjs.com/markup/>
- reveal.js plugins: <https://revealjs.com/plugins/>
- reveal.js PDF export: <https://revealjs.com/pdf-export/>
- reveal.js repository and MIT license: <https://github.com/hakimel/reveal.js>
- Slidev export behavior: <https://sli.dev/guide/exporting.html>
- WebSlides repository: <https://github.com/webslides/WebSlides>
- Archived DeckDeckGo repository: <https://github.com/deckgo/deckdeckgo>
- PptxGenJS repository: <https://github.com/gitbrent/PptxGenJS>
- PptxGenJS documentation: <https://gitbrent.github.io/PptxGenJS/>
- PptxGenJS HTML/table conversion: <https://gitbrent.github.io/PptxGenJS/docs/html-to-powerpoint/>
- PptxGenJS speaker notes: <https://gitbrent.github.io/PptxGenJS/docs/speaker-notes/>
- PptxGenJS masters/placeholders: <https://gitbrent.github.io/PptxGenJS/docs/masters/>
- Microsoft PowerPoint add-ins: <https://learn.microsoft.com/en-us/office/dev/add-ins/powerpoint/powerpoint-add-ins>
- Microsoft content Office Add-ins: <https://learn.microsoft.com/en-us/office/dev/add-ins/design/content-add-ins>
- Microsoft SVG editing and Convert to Shape: <https://support.microsoft.com/en-us/office/edit-svg-images-in-microsoft-365-69f29d39-194a-4072-8c35-dbe5e7ea528c>
- Microsoft Open XML notes slides: <https://learn.microsoft.com/en-us/office/open-xml/presentation/working-with-notes-slides>
- Microsoft PresentationML extensions: <https://learn.microsoft.com/en-us/openspecs/office_standards/ms-pptx/efd8bb2d-d888-4e2e-af25-cad476730c9f>
- Open XML SDK and validator: <https://github.com/dotnet/Open-XML-SDK>
- ECMA-376 Office Open XML standard: <https://ecma-international.org/publications-and-standards/standards/ecma-376/>
