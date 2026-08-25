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

Importing a corporate `.pptx` produces a high-fidelity ASD template (theme tokens plus per-layout archetypes, `example-*` archetypes from real sample slides, and a lossless `TemplateModel` IR), which then flows through the same ASD runtime as app-authored decks.

### Studio Templates management surface

A deep-linkable Studio area at `/slides/templates` lists both **built-in** and **scoped** (user-imported) templates. Each entry carries a **scope badge** (Built-in / Personal / Team). The surface supports **delete**, **duplicate**, and **recolor** for scoped templates, while built-ins are **read-only** (duplicate only) — deleting or recoloring a built-in returns `403`.

Note that the templates **list** endpoint (`GET /api/docs/slides/templates`) returns a lightweight DTO that intentionally omits heavy assets (for performance and security).

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
- Coordinates are integer logical pixels in source and convert to PowerPoint inches using `x / 160`, because a 12 × 6.75 inch widescreen slide maps to 1920 × 1080 units. Percentage values and 0–100 coordinate systems are invalid; validation rejects slides whose top-level layout is wholly confined to that scale.
- All exportable top-level components have explicit `x`, `y`, `w`, and `h`.
- Groups establish a local coordinate space.
- Web preview scales the fixed canvas; it does not reflow at responsive breakpoints.
- Components may perform internal layout only where the same algorithm is implemented by the PPTX renderer. Otherwise children require explicit geometry.

### Content and style rules

- Use theme tokens for fonts, colors, line styles, spacing, and reusable component styles.
- Permit a small typed property set, not arbitrary inline CSS.
- Rich text is represented as explicit semantic runs, not unbounded nested HTML. `<ast-text>` content is plain text and does not parse Markdown; authors use typed attributes/runs rather than `**`, `_`, or other Markdown markers.
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

The Go server invokes a pinned, bundled Node worker rather than building a broad custom OOXML writer in Go:

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

The worker must be reproducibly bundled and checksum-pinned. It receives no tenant credentials and no unrestricted network access. Assets are provided as validated local files/data by the Go service.

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

## Imported Template IR (`TemplateModel`)

Imported corporate `.pptx` templates are stored losslessly as a rich
intermediate representation (IR) called `TemplateModel`, in addition to the
fill-ready ASD archetypes the LLM authoring flow consumes. This is the
"Option C" design: the IR is the persisted source of truth for the future
in-browser template editor and for high-fidelity re-export, but rendering and
preview today go through a single renderer — the existing ASD runtime — by
converting IR → ASD. **There is no second/parallel renderer.**

### Data model

`TemplateModel` (Go: `pkg/docs/slides/themes/template_model.go`; TS mirror:
`web/src/api/slidesTemplateModel.ts`) is expressed in Astonish canvas units
(logical px on a 1920×1080 canvas, colors `#RRGGBB`) rather than OOXML EMUs/inches:

- `TemplateModel { schema:3, size{w,h}, theme{}, layouts[], slides[], warnings[] }`
- `IRLayout { id, name, background, objects[], placeholders[], slideNumber? }`
- `IRChrome { kind: rect|ellipse|line|text|image|path, x,y,w,h, rot, fill?, line?, rectRadius, paths[], text, style?, mediaKey }`
- `IRPlaceholder { name, type, x,y,w,h, style, prompt, ooxmlType, idx }`
- `IRBackground { kind: solid|image, color, mediaKey }`
- plus `IRFill`, `IRLine`, `IRTextStyle`, `IRSlideNumber`, `IRWarning`.

`layouts` come from `ppt/slideLayouts/slideLayoutN.xml`; `slides` from the first
sample slides. Each layout classifies every source shape as **chrome** (fixed
brand decoration) or **placeholder** (a shape whose `p:sp` carries `nvSpPr//p:ph`),
recording the OOXML placeholder type/idx.

### Fidelity import pipeline

The pptx worker (`import_worker.mjs`, template mode) extracts, per layout/sample slide:

- **Custom geometry** (`a:custGeom`) → `kind:'path'` with real SVG path segments
  (`moveTo`→M, `lnTo`→L, `arcTo`→A, `cubicBezTo`→C, `quadBezTo`→Q, `close`→Z) in
  document order — not the old "approximate as rect".
- **Rounded rectangles** → `kind:'rect'` + `rectRadius` (true radius retained in IR).
- **Connectors** → `kind:'line'` with `flipH`/`flipV`.
- **Pictures** → `kind:'image'` + `mediaKey` (asset ingested via `addAsset`).
- **Theme styling**: resolves `p:style` `fillRef`/`lnRef` when a shape has no local fill.
- **`asvg:svgBlip`**: walks `extLst` for the SVG blip `r:embed`.
- **EMF/WMF media**: uses a raster sibling if present, else records an
  `IRWarning` and skips (full EMF→SVG replay is out of scope for v1). Nothing
  degrades silently — every inexpressible construct is recorded in `warnings[]`.

Each layout is classified into an archetype **kind** from its **layout name
first** (the corporate pptx has no `p:sldLayout type` attributes, so the name is
the signal). `kindOf()` **normalizes the name** (`toLowerCase().replace(/[^a-z0-9]+/g,' ')`)
before matching, so `TITLE_SLIDE`→`title slide` and `DividerPage`→`divider page`
are recognized rather than slipping past the patterns. Broadened patterns:
`cover`/`title slide`/`title`→`title`, `divider`/`separator`/`section`/`chapter`/`transition`→`section`,
`agenda`/`toc`/`overview`/`contents`→`agenda`,
`thank`/`closing`/`contact`/`copyright`/`q&a`/`conclusion`/`summary`/`wrap up`/`next steps`/`end`→`closing`,
`blank`→`blank`; else it falls back to the placeholder signature (title +
no-body→`title`, has-body→`content`). A suffixer de-duplicates within a family so
multiple layouts of a role become `title`/`title-2`/… — preserving variant
multiplicity. **Every archetype's human label is the real PowerPoint layout name**
(`cSld @name`, e.g. "Blue cover, anvil and image", "Pink cover with anvil",
"Divider Page with Image", "Full Bleed Image"), so the user and the agent can tell
the colorful covers apart instead of seeing a bare `title-7`.

### Two tiers: fixed brand chrome vs flexible content

Every archetype is tagged with a **tier** (`themes.Archetype.Tier`, surfaced on
the `list_templates`/`create_deck` tool results):

- **`fixed` — brand chrome** (`title`, `section`/divider, `agenda`,
  `closing`/thank-you). These are reproduced **verbatim**: the agent copies the
  archetype markup and substitutes **only** the text inside the ast-text ids
  listed in `FillSlots` (`themes.Archetype.FillSlots`, computed by `layoutToAsd`
  as it emits the `{{TITLE}}`/`{{BODY}}` placeholders). No shape/image/background
  may be moved, resized, recolored, added, or removed. Rebuilding the slide from
  its elements or adding an ad-hoc accent/background is forbidden — that is what
  produced the off-brand white cover with a stray green shape.
- **`flexible` — content** (everything else; also all built-in archetypes, which
  leave `Tier` unset). The agent starts from the archetype and adapts the body
  region to the content type (bullets, small table, chart, image+caption) while
  keeping the template background/tokens.

**Classification is signature-first (`kindOf` in `import_worker.mjs`).** The tier
is derived from a layout's *role*, and the role is decided by placeholder
signature BEFORE the name. A layout carrying a **body or picture placeholder is
flexible `content`** even when its name contains "Title" — so "Title and Text",
"Title and Text: 2/3 Columns", "Title and Content", "Title Only", "Text and
Screenshot", "N Columns – Text and Images", "Quote", "Full Bleed Image", and
"Q&A" are all editable content (the agent adapts their body/table/image), NOT
locked chrome. Only **pure chrome names** become `fixed`: cover names
(`/cover/`, `title slide`, exactly `title`), dividers (`divider|section|
separator|chapter|transition` or `secHead`), `agenda`/`toc`/`contents`, and
thank-you/closing (`thank|farewell|goodbye|copyright|contact`, and only when the
layout has no body of its own). The loose `\btitle\b` match was removed because
it wrongly caught every "Title and …" content layout and froze it as verbatim
brand chrome the agent could not adapt. The stable-chrome guarantee (below)
still ensures a real cover/divider/agenda/closing exists by aliasing the branded
cover layouts, so tightening `kindOf` never drops a chrome role.

`Tier`/`FillSlots` are optional (`omitempty`) and persist without a schema change:
`SaveTemplate`/`ListTemplates` encode them after a `\u0000` (NUL) delimiter in
`SlideContent.Notes` (`label\u0000{"tier":…,"fillSlots":[…]}`). A delimiter-free
Notes decodes to `Tier=""`/`FillSlots=nil` (backward compat for pre-tier
templates). Built-ins stay exactly `{title,section,content}` with unset tier.

### Stable chrome set guarantee

Imported templates always expose the **complete stable chrome family** —
`title`, `section`, `agenda`, and `closing` — plus at least one flexible
`content` archetype. For each chrome role not already present after
classification, the guarantee step (in `import_worker.mjs`):

1. **Prefers a real branded layout** — it scores every imported layout for
   role suitability and, when one scores above threshold, aliases it (reuses its
   markup + `_layout` IR, `tier:'fixed'`, label = that layout's real name,
   `fillSlots` copied). Real corporate decks usually have a cover/divider/agenda
   to alias, so this is the primary path.
2. **Else synthesizes in the template's own style** via `synthChrome(want, {masterCtx, themeTokens, siblingLayout})`:
   it builds an IRLayout-shaped object whose background is the deck's **real
   shared master background** (`masterCtx.bg`, or the closest branded layout's
   background if the master bg is the neutral fallback), whose objects are a
   **copy of the master chrome** (`masterCtx.chromeObjects` — shared logo/accent
   bars/brand shapes), plus role-appropriate text/accent placeholders styled from
   `themeTokens` (`surface/ink/accent/accent2/displayFont/bodyFont`). The result
   is coherent with the rest of the chrome family.

The old white generic `fallbackMarkup()` slab (hard-coded `#4472C4` accent on a
white bg, unrelated to the template) has been **deleted**. A `warn()` is emitted
only when both branded-alias and master-chrome synthesis are unavailable and it
falls back to pure theme tokens.

Colorful cover/divider chrome is captured by resolving the **master→layout
inheritance chain**, the way PowerPoint renders. A layout's effective background is
the first non-fallback of `[layout own <p:bg>, master <p:bg>]` — so a layout with
no explicit background inherits the master's (not white). The master's decorative
chrome objects (backgrounds, logos, accent shapes) are merged **behind** the
layout's own chrome (master first in z-order), skipping master placeholders and
honoring `showMasterSp="0"`. Suppressing the master is only a fidelity LOSS when
the layout has **no own chrome to replace it** — the branded covers/dividers set
`showMasterSp="0"` precisely because they paint their own full-page chrome (bg +
anvil + logo + footer). So the importer warns about suppression **only** when a
`showMasterSp="0"` layout ends up with zero own decorative objects (a genuinely
sparse slide), never for the richly-chromed covers; this keeps the warning list a
real signal instead of ~19 false alarms. This is why imported covers now render
in full color.

The pptx's authored **sample slides are NO LONGER surfaced as archetypes.** They
were previously turned into `example-*` archetypes, but 22 of 23 real corporate
slides carry no own `<p:bg>` — they just drop a photo into the layout's picture
placeholder — so those example archetypes rendered **white**. All the colorful
variety (covers, dividers ±image, agenda, content) lives in the **layouts** under
the single master, and those inheritance-corrected layout archetypes are the
on-brand starting points. Sample slides are still captured into
`templateModel.slides[]` for the future editor, just not as archetypes. The
stable chrome set (`title`/`section`/`agenda`/`closing`) **plus** a flexible
`content` role is guaranteed via the branded-alias-preferred / style-derived
synthesis described above, so imported templates always carry the full family
even for a sparse pptx.

### Picture placeholders: borrowing the authored sample photo

A corporate layout's hero-image region is frequently an **empty picture
placeholder** — a `<p:sp>` (or `<p:pic>`) carrying `<p:ph type="pic">` with **no
`<a:blip>`**, i.e. an "insert picture here" slot. The actual photo is authored on
the **sample slide** that uses the layout, usually as a free-floating `<p:pic>`
(often with no `<p:ph>` binding at all). Because sample slides are not surfaced as
archetypes, a naive import left that placeholder with no `mediaKey`, and
`layoutToAsd` fell back to a synthetic neutral panel — the reported **blank blue
box** (the panel color came from `themeTokens.accent2`).

To fix this without re-surfacing sample slides, the importer **borrows the
sample picture's fill _and_ its own geometry + flip**. Sample slides are
extracted **before** the layout loop and indexed by their `slideLayout`
relationship target (`samplesByLayoutPath`). For each layout, before
serialization, `borrowSampleImages(ir, layoutPath)` walks that layout's
`type:'image'` placeholders that lack a `mediaKey` and picks the best candidate
among the sample slides' image objects/placeholders. Candidates are **ranked**
(not by raw IoU alone — that would wrongly pick a decorative overlay): (1) a
**raster photo is strongly preferred over a vector/SVG shape** — corporate
covers place the real photo full-bleed *behind* a colored single-path SVG
"anvil" overlay that sits exactly over the placeholder box, so a naive
highest-IoU match grabs the flat shape instead of the photo; (2) then larger
image area; (3) then box overlap (IoU) as the tie-breaker.

The winning candidate contributes its `mediaKey` **plus the sample picture's own
box** (`borrowX/borrowY/borrowW/borrowH`) **and its `flipH`/`flipV`** onto the
placeholder (`processPic` now captures image flip the same way `processSp` does
for shapes). `layoutToAsd`'s `if (p.mediaKey)` branch then emits a real
`<ast-image asset-ref=…>` at the **borrowed sample geometry** (not squeezed into
the placeholder's small declared hole) with `flip-h`/`flip-v` attributes when the
sample was mirrored, so the default hero renders **faithfully** — correct
size, position, and mirroring — matching the source PowerPoint out of the box.
This still borrows only the picture fill (mediaKey + that picture's own box +
flip), never the whole sample slide, so it is **not** "sample slide as
archetype." The layout's own decorative shape (e.g. the dark-green anvil) stays
**behind** the borrowed image because `layoutToAsd` emits `bg → objects →
placeholders` and paint order == document order (see the renderer contract
below); no z-index is introduced.

The borrowed hero is also a **replaceable image fill slot**: its element id
(`ph-pic-N`) is added to the archetype's `fillSlots`, exactly like the text
`ph-title`/`ph-body` slots. `fillSlots` stays a flat `[]string` of element ids —
an image slot is just an `<ast-image>` id in that list — so `create_deck` /
`write_slide` and the slides skill can advertise the photo as swappable. A user
can later ask to "replace the hero image with X" and only that image node
changes; the replacement **inherits the slot's geometry and flip** (the default
is pre-populated from the template so the slide looks right before any edit).

Flip is expressed end-to-end as `flip-h`/`flip-v` boolean attributes (optional,
`omitempty`, no protocol-version bump): the Lit web runtime
(`PositionedElement.updated()`) and the standalone-HTML export
(`nodeInlineStyle`) both compose `scaleX(-1)`/`scaleY(-1)` with the existing
`rotate(...)` transform, and the PPTX export worker passes `flipH`/`flipV` to
PptxGenJS `addImage` so a re-export stays faithful (never re-mirrored).

An empty picture placeholder that is **not** a borrowable hero (i.e. no sample
photo overlaps it — the common case for CONTENT layouts like "N Columns – Text
and Images", "Text and Screenshot", "Title and Content") is **not** a fidelity
failure: it is a legitimate "insert picture here" hole the author fills. The
importer therefore emits it as a **fillable image drop-slot** — its id
(`ph-pic-N`) is added to `fillSlots` exactly like the borrowed-hero slot, and it
is rendered as a light neutral affordance (`<ast-shape id="ph-pic-N" … alt="…">`,
because `ast-image` requires an `asset-ref`) that `create_deck`/`write_slide`
replaces with the chosen image. It carries an `alt`, is **not** marked
`decorative`, and emits **no warning**. This is what makes content-layout image
slots contiguous in `fillSlots` (`ph-1, ph-pic-2, ph-3, ph-pic-4, …`) instead of
the old non-contiguous `ph-1, ph-3, ph-5` (where the interleaved image regions
were silently dropped to decorative panels). The old
`Picture placeholder has no image; rendered neutral panel` warning is gone: an
empty picture placeholder in a content layout is a fillable slot, not an error.
The distinction is: a **borrowable hero** (a sample photo overlaps the region) is
pre-populated with the default image at the sample's own geometry/flip; an
**empty content slot** (no overlap) is an advertised, empty image slot — both are
advertised in `fillSlots` and both are swappable.

### Renderer contract: paint order == document order

The runtime and HTML exporter have **no z-index** and **no implicit background
layer**: paint order is document order (the first child paints at the back).
`layoutToAsd` therefore emits the **full-canvas background first**, so a faithful
branded archetype renders correctly. This was verified **not** to be the source
of the white-cover bug — the failure was upstream (the AI never received a
faithful branded archetype for the title role); do not add a z-index or an
implicit-bg layer to "fix" rendering.

### IR → ASD serialization

Each `IRLayout` is serialized to a valid `<ast-slide>` archetype (fill-ready with
`{{TITLE}}`/`{{BODY}}` placeholders). Notable mappings:

- Full-bleed image background → a full-canvas
  `<ast-image asset-ref=… x=0 y=0 w=1920 h=1080 fit=cover decorative>` emitted as
  the **first** child (z-order behind chrome). No schema change — `ast-image` was
  already allowed, and images still flow through the `AssetIngestor` (no `data:`
  smuggling).
- Custom paths → `<ast-shape kind="rect" path="M … A … Z">`; the SVG viewBox is
  `0 0 W H` in canvas px, path coords scaled into the shape box.
- Rounded rects → `geom="roundRect"` (the runtime's fixed ~0.12 radius). The IR
  keeps the true `rectRadius` for the future editor / high-fidelity export; this
  is a **known v1 approximation**.
- **Font sizes are scaled to the ASD canvas.** OOXML run sizes are in points; the
  runtime applies `ast-text size` as **px** on the fixed 1920×1080 canvas. Because
  all geometry (x/y/w/h) is already scaled by `scale = min(1920/pxW, 1080/pxH)`
  (a 1280×720 source ⇒ `scale ≈ 1.5`), the importer scales each run's point size by
  the **same** `scale` exactly once, in `styleOf` (where `scale` is in closure
  scope — `extractRuns` stores the raw pt value). A 10 pt footer (`sz="1000"`) thus
  emits `size≈15` to match its 1.5×-enlarged box, instead of rendering tiny at
  `size="10"`. Runs that carry **no explicit** `@sz`/`a:latin` inherit a real
  size/font from the shape's own `a:lstStyle` first, else the master/layout
  `p:txStyles` (`titleStyle`/`bodyStyle`/`otherStyle` → `a:lvl1pPr/a:defRPr`),
  before scaling — so they no longer fall back to the hard-coded 24 pt / serif
  default. All of this is generic (no template-specific fonts/sizes) and defensive
  (absent `txStyles`/`lstStyle` ⇒ unchanged behavior). Emitted `font` families
  carry a **web-safe fallback chain** (`<brand family>, Aptos, Arial, sans-serif`):
  corporate fonts like `72 Brand`/`72 Brand Medium` are not installed in a browser,
  and `ast-text` sets `font-family` directly, so a bare uninstalled family would
  fall back to the browser default **serif** (Times). The appended chain degrades
  an uninstalled brand font to Aptos/Arial/sans-serif (matching the deck body font)
  while still preferring the real brand font when it is available.

### ASD path-arc widening

`validation.go`'s `safePathPattern` was widened as a **strict superset** to allow
the arc command letters `A`/`a` (previously only `M L C Q Z H V`). This is a
character-class allowlist only — no letters that would enable entity/script
injection — and unlocks ellipses and curved brand geometry. `safeColor`/`safeFont`
guards and the export CSP runtime-hash test are unchanged (no runtime change).

### Persistence

- The `Deck` Ent schema (personal + team scopes) gains an optional
  `template_model` text column holding the raw IR JSON. It is additive and
  populated **only** for imported templates.
- Imported templates persist with `schemaVersion = SchemaV3` (`3`); built-in
  templates and normal decks keep `SchemaV1`/`SchemaV2` and are untouched.
- The worker's template-mode response is `{ schema:2, name, label, tokens,
  assets, archetypes[], templateModel }`. `archetypes[]` (the IR→ASD output) keeps
  the LLM authoring flow identical; `templateModel` banks the lossless IR.
  `SaveTemplate` marshals `Template.Model` into the column; `ListTemplates`
  unmarshals it back, so the IR round-trips.
- Archetype **variants** surface to the agent and Studio: `list_templates`
  returns `archetypeVariants[]` (`{kind,label}`); the Templates UI renders each as
  a friendly label chip showing the real PowerPoint layout name. The human label is
  persisted in `SlideContent.Notes` (kind stays in `SlideContent.Title`) — backward
  compatible. Multiple same-role layout variants (several `title` covers, several
  `section` dividers) each get their own chip by label.

### Response performance (slim DTOs)

The stored `store.DeckManifest` carries two heavy fields — `Assets` (a base64
`data:` URI per logo/image) and `TemplateModel` (the multi-megabyte lossless IR).
These are needed for **rendering** (present iframe + exporters) and **round-trip**
(`SaveTemplate`/`ListTemplates`), but no client or model consumer reads them off a
list/get/tool response. Serializing them verbatim made an imported-template deck
slow to list, slow to open, and made the chat `SlidesDeckView` hang. The fix trims
them at the **serialization boundary only** (the store record is unchanged):

- `GET /api/docs` (Slides list) → `slidesDeckListItem` (id/slug/title/description/
  schemaVersion/scope/timestamps; no theme/assets/templateModel).
- `GET /api/docs/slides/{slug}` (deck open, `fetchSlidesDeck` → `SlidesDeckView`)
  → `slidesDeckDTO` (adds `theme`, still drops assets + templateModel).
- Slide tool results (`create_deck`/`get_deck`/`list_decks`/`write_slide`/
  `validate_deck`) → `DeckView` (keeps theme; replaces the heavy fields with
  `assetCount` + `hasTemplateModel` so the model still knows they exist). Single-deck
  results (`create_deck`/`get_deck`/`list_deck_assets`/`add_deck_image`) additionally
  carry a lightweight `assets[]` **catalog** (see *Deck image assets* below) via
  `deckViewWithAssets`; `list_decks` uses plain `deckView` (no catalog) to stay light.

`Service.Scene` still reads `Assets` straight from the store, so `/present` and the
PPTX/PDF/HTML exporters resolve `asset-ref` → `data:image/…` unchanged; the asset
security path (asset-ref → `resolveImageSrc`, CSP) is untouched.

### Deck image assets (catalog + AI add/swap)

Imported templates carry their media (logos, brand marks, hero photos) in the
deck's `Assets` map, keyed by a content hash (`sha256-<hex>`) with a
`data:<mime>;base64,…` value. `ast-image` **requires** an `asset-ref`
(`schemas.go`), and `asset-ref` resolves to a stored `data:` URI via
`resolveImageSrc` — so the AI can only fill or swap an image slot if it knows
which asset-refs exist. Two mechanisms surface and grow that library **without**
leaking the heavy `data:` bytes into the model context or the wire:

- **Catalog projection.** `DeckView.Assets` is a `[]AssetInfo` built by
  `assetCatalog(assets)` — one entry per asset with `ref` (the map key, usable
  verbatim as an `ast-image` `asset-ref`), `mime` (parsed from the `data:` prefix),
  approximate decoded `bytes`, and a `kind` hint (`logo` for SVG/very-small images,
  else `image`). It **never** copies the `data:` value. `create_deck` (template
  branch) and `get_deck` return it via `deckViewWithAssets`, so the AI immediately
  sees the imported template images it can reference or swap. This keeps the
  perf/security guard (`TestSlidesResponsesOmitHeavyManifestFields`) green — the
  catalog is hints only.
- **`list_deck_assets`** loads the full manifest and returns the same catalog, so
  the AI can enumerate an existing deck's images and pick an `asset-ref` to place
  in an `ast-image` (fill a drop-slot) or to swap one image for another catalog id.
- **`add_deck_image`** fetches a **public https image URL** through the
  SSRF-protected `AssetIngestor.Fetch` (MIME/SVG validation, redirect re-check,
  20 MB cap — no private-network fetches, no `data:` smuggling), stores it under
  `ref = "sha256-" + asset.ID` (matching the importer's key convention so
  `resolveImageSrc` lookups are uniform), persists it via `Service.AddDeckAsset`
  (clone the `Assets` map → set `ref` → `UpdateDeck`; idempotent for the same URL),
  and returns the `assetRef` for the AI to drop into an `ast-image`. This is the
  supported path for "add this image to the deck" — there is no Studio upload UI;
  the AI does it via the tool.

The asset-ref → `resolveImageSrc` contract (data: only) and the `sha256-<hex>`
key convention are unchanged; both new tools are additive (no protocol bump).

### Embedded fonts (imported brand faces → `@font-face`)

Corporate templates set the deck theme fonts to concrete brand families (e.g.
`72 Brand`), which the theme surfaces as `--ast-display` / `--ast-body-font` and
`ast-text` applies as `font-family`. But a bare, uninstalled family name resolves
to the browser's default **serif (Times)** — the family name alone does not load a
font. The template's actual font *files* must travel with the deck and be declared
to the browser via `@font-face`:

- **Extraction on import.** A `.pptx` that enabled "Embed fonts in the file" carries
  its faces under `ppt/fonts/*.fntdata`, mapped family + variant
  (`regular`/`bold`/`italic`/`boldItalic`) via `<p:embeddedFontLst>` in
  `presentation.xml` (each variant → `r:id` → `.rels` `Target`). `import_worker.mjs`
  `collectEmbeddedFonts()` walks that list, resolves each part, and recovers a plain
  TrueType.
- **`.fntdata` is EOT-wrapped, usually MTX-compressed.** These parts are Embedded
  OpenType (EOT) containers — **not** the Word `.odttf` GUID-XOR obfuscation (there
  is nothing to de-XOR). PowerPoint compresses the sfnt with MicroType Express (MTX),
  so a naive header-strip is insufficient; the worker uses the `mtx-decompressor`
  npm package (`eotToTtf`, MPL-2.0, zero-dependency) to parse the EOT header, apply
  its compression/encryption flags, and reconstruct a standard `.ttf`. Any face that
  fails to decode is skipped with a warning — import still succeeds.
- **Stored as font assets.** Each recovered face is stored in the SAME deck `Assets`
  map as images but under a font-namespaced key `font:<family>:<variant>` with a
  `data:font/ttf;base64,…` value (distinct from the `sha256-<hex>` image keys). Faces
  larger than a 4 MB per-face cap (typically a full CJK fallback like *Arial Unicode
  MS*) are skipped — the web-safe fallback chain covers those glyphs.
- **Manifest in the theme.** The importer records a compact manifest as a single
  JSON string under the theme key `embedded-fonts`
  (`[{family,variant,assetKey}]`). Because `Theme` is `map[string]string`, a string
  value passes through the existing `tokens → DeckManifest.Theme → SceneGraph.Theme`
  plumbing verbatim — no Go struct change.
- **`@font-face` emission.** `export_html.go` `writeFontFaces` parses
  `embedded-fonts` (via `parseEmbeddedFonts` in `fonts.go`), looks up each
  `assetKey` in `Assets`, and emits an `@font-face` rule (family, `data:` src,
  `format(truetype|opentype)`, `font-weight` 400/700 + `font-style` normal/italic per
  variant, `font-display:swap`) inside the deck `<style>`. The export CSP already
  allows `font-src data:`, so no CSP change is needed and no remote font is fetched.
  `writeThemeCSS` skips the `embedded-fonts` key (it is a manifest, never a
  `--ast-*` variable). With the `@font-face` declared, the concrete family the theme
  names (e.g. `72 Brand`) resolves to the real font; the appended
  `Aptos, Arial, sans-serif` chain is used only for missing glyphs/variants.
- **Digit-leading families are quoted.** Per the CSS grammar an *unquoted*
  `font-family` value is a list of identifiers, and a CSS identifier may **not**
  start with a digit — so a brand family like `72 Brand` is invalid unquoted and
  the browser silently drops the whole declaration, falling back to serif (Times)
  *even when the `@font-face` is present*. Both the importer (`cssFontFamilyName` /
  `withFontFallback` in `import_worker.mjs`, which normalizes the family list it
  bakes into each `ast-text` `font=` attribute) and the runtime (`cssFontFamily`
  in `AstText.ts`, the choke point that assigns `this.style.fontFamily`) double-
  quote any family that starts with a digit or contains a character outside
  `[A-Za-z0-9 _-]`, leaving generic keywords and already-quoted names untouched.
  The runtime normalization is idempotent and also fixes decks stored before this
  change. `@font-face` `font-family` names are always double-quoted string tokens,
  so they were never affected.
- **Kept out of the image surfaces.** `assetCatalog` skips `font:` keys, so embedded
  fonts never appear in the image catalog the AI browses, are never selectable as an
  `ast-image` `asset-ref`, and — like images — their heavy `data:` bytes never leak
  into a tool result or HTTP response (`TestSlidesResponsesOmitHeavyManifestFields`
  asserts no `data:image`/`data:font` substring).

Generic across templates: a `.pptx` with no embedded fonts is a no-op (no
`embedded-fonts` key, no `@font-face`), and the existing web-safe fallback chain
remains the correct degraded behavior.

**Store-level field projection.** The slim DTO above trims the *wire* payload, but
`personalDocsStore.ListDecks`/`teamDocsStore.ListDecks` still ran
`Deck.Query()…All()`, which SELECTs *every* column — including `template_model`
(multi-MB IR) and `assets` (base64) — and `fillPersonalDeck`/`fillTeamDeck` copied
them in, so the server still paid the read+deserialize cost on every Slides-list.
The fix adds `DocsStore.ListDecksLite`, implemented with Ent field projection
(`.Select(FieldID, FieldSlug, FieldTitle, FieldDescription, FieldSchemaVersion,
FieldTheme, FieldCreatedAt, FieldUpdatedAt)`, **omitting** `FieldAssets` +
`FieldTemplateModel`) and a `fill…Lite` that leaves those two fields zero. The HTTP
list path (`ListDocsHandler` → `Service.ListDecksLite`) uses it, so the heavy
columns are never read for the list. `ListTemplates` **keeps** full `ListDecks`
(it needs `TemplateModel` to rehydrate the IR), and deck-open (`GetDeck`) stays
full (a single row is acceptable and `Scene`/exporters need it). In-memory test
stores implement `ListDecksLite` by delegating to `ListDecks` and nil-ing the heavy
fields.

### Resolved decision: one template with layout variants (Option A)

A pptx like *GCO IPE&D PPT TEMPLATE — SAP PARTNER* is a **single design system**:
one slide master, one slide-relevant theme ("SAP Colors 2023"), and 39 layouts.
All the colorful variety (blue/pink/green anvil covers, image covers, agenda,
divider ±image, content layouts) lives as **distinct layouts under that one
master** — there is no natural boundary to split on.

Two options were considered:

- **Option A (chosen):** keep **one Astonish template per pptx** and expose its
  layouts as classified, human-labeled, selectable **variants** (label = the real
  PowerPoint layout name). Chrome fidelity comes from master→layout inheritance.
- **Option B (rejected):** emit **multiple templates from one pptx** (e.g. one per
  cover family). Rejected because there is a single master + single theme, so
  splitting would fragment one coherent brand system and complicate recolor/manage.

Option A is implemented. The white output was **not** a template-boundary problem —
it was (1) example archetypes built from thin, background-less authored slides and
(2) missing master→layout background/chrome inheritance. Both are now fixed:
example-from-slide archetypes were removed, and inheritance is resolved at import.

### Roadmap: in-browser template editing

In-browser editing (contenteditable placeholders + `collectFills`, as validated in
the pilot) is a **known next step, not part of this version**. The IR is persisted
losslessly and typed in both Go and TS (`slidesTemplateModel.ts`) specifically so
the editor can be built later **without re-importing** the source `.pptx`. The
architecture must not preclude it — hence the IR is the source of truth even though
rendering currently goes through IR → ASD.

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
DELETE /api/docs/slides/templates/{name}            # delete a scoped template (built-ins read-only → 403; idempotent on missing); honors ?scope=personal|team
POST   /api/docs/slides/templates/{name}/duplicate  # clone a built-in or scoped template into a NEW scoped template (optional body {"newName":"...","newLabel":"..."}); returns {"template":{"name":...,"label":...}}
PATCH  /api/docs/slides/templates/{name}/recolor    # update a scoped template's palette tokens (surface/ink/accent; validated hex); 403 for built-ins, 400 for bad hex / unknown keys
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

Backend emitters, Studio consumers, persisted parsing, terminal behavior, and scenario fixtures must change together when this contract is implemented. Successful `create_deck`, `write_slide`, and `get_deck` tool results emit this event. `get_deck` uses the `deck_viewed` action so requests such as “show me the deck” render the existing `SlidesCard` with its authenticated preview and export actions rather than only returning raw tool data or prose.

### Turn-scoped SlidesCard coalescing

Studio folds slide `docs_update` messages into cards by `deckSlug` **only within one assistant turn**. The preceding user message is the turn boundary: updates for the same deck that occur after that user message and before the next user message belong to one fold. A same-turn `create_deck` followed by one or more `write_slide` updates therefore remains one evolving `SlidesCard`, with progress changes refreshing its authenticated preview.

A later assistant turn starts a new fold even when it edits the same `deckSlug`. Its first successful slide update appends a fresh card rather than mutating or replacing the prior turn's card. That new card presents the latest deck state through the authenticated preview and exposes the **Present**, **PPTX**, **PDF**, and **HTML** actions. Earlier cards remain in transcript order as records of their turns.

The same folding result is required across all three delivery paths: live SSE, active-run reconnect, and static history reconstructed from persisted `[docs_update]` markers. History loading must apply the same preceding-user boundary and must not globally deduplicate a deck across turns.

This is a **frontend rendering and history-folding contract only**. It does not change backend `docs_update` payloads, marker persistence, tenant-scoped deck persistence or storage scope, or any presentation/export API.

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

- `pptxgenjs` — native OOXML generation for app-authored ASD decks (MIT).
- bundled with the application build; no runtime `npm install`.

Adding a Node-authored build artifact does not turn the distributed product into multiple binaries: the worker can be bundled and invoked through an available JS runtime strategy selected during implementation. Before committing to subprocess Node in production, prototype and choose among:

1. embedded worker executed by a small bundled JS runtime;
2. a Node subprocess available in the controlled server image;
3. browser-side PptxGenJS generation for trusted, already-loaded scene graphs.

The exporter interface must isolate this choice. Browser-side export reduces server dependencies but can expose large assets and produce device-dependent behavior; the server-side worker is preferred for deterministic platform export.

---

## Implementation Phases

### Phase 0 — Export spike and decision gate (1 week)

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

## Interactive questions (`ask_user`)

When a template offers **multiple variants per role** (several title covers, several
dividers) or the flow needs a yes/no decision (e.g. "add an agenda slide?"), the
agent no longer asks with a plain-text numbered list in Studio. Instead it renders a
**generic, reusable inline chat questionnaire** — one question at a time — using the
`ask_user` tool. Slides is the first consumer of this generic primitive; nothing
about the question mechanism is slides-specific.

### The `ask_user` tool

`ask_user` is a cross-cutting agent tool (registered with the slides/docs toolset,
consumed on the general chat path). Arguments:

- `kind`: `"yesno"` (renders a Yes/No card) or `"select"` (renders a pick-one card).
- `prompt`: the single question sentence.
- `options` (for `select`): `[{ id, label, description? }]`.
- `thumbnails` (optional): `[{ optionId, kind, markup?, assetRef?, theme?, assets? }]`
  attached per option for a **visual** picker. For slides, `kind` is
  `"slides-archetype"` and `markup` is the archetype's ASD `ast-slide` fragment
  (from `get_template_variant_previews`).
- `slidesTemplate` (optional slides convenience, `select`): a template name. ask_user
  then resolves that template's per-role variants itself, auto-generates one option
  per variant, and attaches a live `slides-archetype` thumbnail to each — so the model
  never hand-copies markup. `slidesKind` filters to one role (title/section/…).
- `slidesTemplatePicker` (optional slides convenience, `select`): set `true` (omit
  `options`) for the **first** question, "which template should I use?". ask_user
  enumerates every available template (built-in + scoped/imported) via
  `templatePickerOptions`, generates one option per template (`id` = template name,
  `label`/`description` from the `list_templates` catalog), and attaches a live cover
  thumbnail per template — the template's first `title` archetype (else its first
  archetype), tagged with the template name so the frontend resolves asset-refs at
  render time. Never embeds `data:` bytes.

The tool does **not** block the agent loop. On invocation the chat runner
(`maybeEmitChatQuestion` in `pkg/api/chat_runner.go`) turns the result into an inline
question card, then ends the turn. The user answers by clicking; that click is sent
back as an **ordinary user message** (the chosen option label, or `Yes`/`No`) via the
existing `connectChat` path — there is no dedicated answer endpoint and no sentinel
token, so history stays readable and the model reads the answer naturally.

### Slides variant previews (`get_template_variant_previews`)

`get_template_variant_previews(template, kind?)` returns each archetype variant's
`{ kind, label, tier, fillSlots, markup }` plus the shared `theme` tokens and
`assets`. It carries **ASD text and asset-refs only** — never `data:` image/font
bytes (those resolve through the deck asset plumbing at render time), so it is safe
to return from a tool and keeps `TestSlidesResponsesOmitHeavyManifestFields` green.
The agent passes a variant's `markup` (+ `theme`/`assets`) as an `ask_user`
`slides-archetype` thumbnail.

### The `[chat_question]` event contract

The card is emitted with the same prefix-marker pattern as `distill_preview` /
`app_preview` / `tutorial_blueprint_preview`:

- **Live**: an SSE event of type `chat_question` with data
  `{ questionId, kind, prompt, options: [{ id, label, description?, thumbnail? }] }`.
- **Reload**: a persisted `model` message text prefixed `[chat_question]` followed by
  the same JSON, reconstructed on load by `tryParseChatQuestionMessage`
  (`pkg/api/chat_utils.go`) into a typed `chat_question` `StudioMessage`.

### Rendering

- **Studio** (`web/src/components/StudioChat.tsx`): a `chat_question` message renders
  a generic **`YesNoCard`** or **`SingleSelectCard`**
  (`web/src/components/chat/questions/`). For a `slides-archetype` thumbnail,
  **`SlidesArchetypeThumb`** live-renders the variant's `ast-slide` markup by mounting
  the same `ast-*` runtime components the deck viewer uses, scaled down from the
  1920×1080 canvas and set to `pointer-events: none` (non-interactive). On answer the
  card collapses to a read-only `You chose: <label>` state and the chosen label is
  sent as a normal user message, entering the agent loop like typed input.
- **Terminal TUI** (`pkg/launcher/tui_chat.go`, `mapSSEToEvents`): the same event
  **degrades to text** — the prompt followed by a numbered list of option labels
  (or Yes/No). There are no thumbnails; the user answers by typing the number/label,
  which flows through the existing input → SSE user-message path (terminal parity).

### Slides workflow integration

For a template with multiple variants, the agent asks **one question at a time in
sequence**: (0) when the user did not name a template, `ask_user kind="select"` with
`slidesTemplatePicker=true` — a card with one live cover thumbnail per available
template, so the user chooses the design visually; the reply is the template name for
`create_deck`. Then (1) `ask_user kind="select"` with `slides-archetype` thumbnails for the
title/cover variant, (2) `ask_user kind="yesno"` for "Would you like an agenda
slide?", (3) `ask_user kind="select"` with thumbnails for the divider/section
variant. The questionnaire remains **agent-driven** (the model chooses to ask); it is
not hard-enforced at the runtime level in this pass.

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
