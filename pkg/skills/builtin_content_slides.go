package skills

// BuiltinSlides contains the complete Astonish Slides authoring reference that
// is delivered on demand via skill_lookup("slides"). It mirrors the delivery
// model of BuiltinGenerativeUI: the standard system prompt keeps only a short
// pointer, and this full guide is loaded into context the moment the agent
// works on a presentation/deck/PowerPoint. Keeping the depth here (instead of
// in the always-on prompt) keeps the standard prompt slim while giving the
// model everything it needs to build a styled deck.

const BuiltinSlides = "# Astonish Slides (Presentations) — Complete Reference\n" + `
Load this skill whenever the user asks for a **presentation, slide deck, slides, a ` + "`.pptx`" + `, or PowerPoint** — or asks to turn a document/report into slides. It teaches the exact workflow to produce a *styled* deck (not the blank black-on-white default), the full ASD v2 element vocabulary, and how to gather the information you need first.

The single most important rule: **start from a template.** A normal request must never produce an unstyled deck.

---

## The Workflow (do this in order)

1. **Choose a template — call ` + "`list_templates`" + ` first, then let the USER pick.**
   It returns a **lightweight catalog** of the available templates (built-in + any the user imported from a real ` + "`.pptx`" + `): each entry has ` + "`name`" + `, ` + "`label`" + `, ` + "`description`" + `, ` + "`scope`" + `, and the ` + "`archetypeKinds`" + ` it provides (e.g. ` + "`title`" + `/` + "`section`" + `/` + "`content`" + `). It does **not** include the theme tokens, assets, or archetype markup — those are seeded for you by ` + "`create_deck`" + ` in step 2 when you pass the template name. Built-ins include ` + "`light-corporate`" + ` (clean light), ` + "`midnight`" + ` (dark), and ` + "`aurora`" + ` (colorful/gradient).

   **The template choice belongs to the user — do NOT silently pick one yourself.** Decide which of these three cases applies:
   - **The user named a template** (e.g. "use midnight", "our corporate template", "a dark theme") → use it and proceed without asking.
   - **The user explicitly delegated the choice** (e.g. "you pick", "choose whatever fits", "your call", "surprise me") → pick the best fit, state which one and why in one line, and proceed.
   - **The user said nothing about the template** (the common case — e.g. "make a deck about X") → **STOP and ask.** Present the ` + "`list_templates`" + ` results as a short numbered list (label + one-line description + scope for each) and ask the user which they'd like. Do **not** call ` + "`create_deck`" + ` until they answer. A brand/tone hint you could infer is NOT permission to choose — only an explicit named template or explicit delegation is.

   When in doubt about which case applies, treat it as "said nothing" and ask.

2. **Create the deck WITH that template — call ` + "`create_deck`" + ` and pass ` + "`template`" + `.**
   Passing the ` + "`template`" + ` name seeds the deck's theme tokens **and** assets, so every slide is styled automatically. ` + "`create_deck`" + ` returns the template's full ` + "`archetypes`" + ` (the ready-made ` + "`title`" + `/` + "`section`" + `/` + "`content`" + ` slide skeletons with ` + "`{{TITLE}}`" + `/` + "`{{BODY}}`" + ` placeholders) — this is where you get the markup to fill. Example arguments:
   ` + "```json" + `
   { "slug": "q4-review", "title": "Q4 Business Review", "template": "midnight" }
   ` + "```" + `
   **Never call ` + "`create_deck`" + ` without a template for a normal presentation request.** Only skip the template if the user explicitly wants a blank canvas — and even then set readable ` + "`theme`" + ` tokens so text is legible.

3. **Build slides from the archetypes — call ` + "`write_slide`" + ` once per slide.**
   Use the returned archetype markup as your STARTING point and replace the ` + "`{{TITLE}}`" + ` / ` + "`{{BODY}}`" + ` placeholders with real content. Structure a deck as:
   - a **title** archetype for slide 0 (position 0),
   - a **section** archetype as a transition slide before each major topic,
   - **content** archetypes for the body slides.
   ` + "`write_slide`" + ` takes ` + "`deck_slug`" + `, a zero-based ` + "`position`" + ` (writing an occupied position replaces it), ` + "`markup`" + ` (exactly one complete ` + "`<ast-slide>`" + ` root), and optional ` + "`notes`" + ` (speaker notes — use this field, do not embed ` + "`<ast-notes>`" + ` yourself unless you need to).

4. **Inspect before revising — call ` + "`get_deck`" + `.** It returns the deck and its ordered slide markup so you can edit precisely.

5. **Validate before declaring done — call ` + "`validate_deck`" + `.** It returns structured diagnostics. **Fix every error before continuing** — do not leave a deck with validation errors.

You can also ` + "`list_decks`" + ` to see existing decks (template decks are hidden from this list).

---

## Imported corporate ` + "`.pptx`" + ` templates

When the user imported a real corporate ` + "`.pptx`" + ` (Studio → Slides → Import ` + "`.pptx`" + `), it becomes a **standard ASD template** — a set of theme colors and archetypes derived from the file, not the original file itself. Importing is inherently lossy: fine details of the corporate design are approximated. The imported template appears in ` + "`list_templates`" + ` alongside the built-ins and is used exactly the same way: call ` + "`create_deck`" + ` with its ` + "`template`" + ` name, then fill the archetypes with ` + "`write_slide`" + `. You do not do anything special to author it.

---

## Gathering Requirements (don't stall, but aim right)

Before authoring, settle these — ask the user only what you genuinely can't infer, otherwise pick sensible defaults and proceed:

- **Audience & purpose** — execs vs. engineers vs. customers changes tone and density.
- **Length** — how many slides? Default to a tight 5–8 for an overview unless told otherwise.
- **Key points per slide** — 3–5 bullets max; one idea per slide. Prefer more slides over crowded ones.
- **Tone / brand** — informs your *recommendation*, but the template is the user's choice: present the ` + "`list_templates`" + ` options and let them pick (see Workflow step 1). Never auto-select a template just because you inferred a tone.
- **Existing material** — if the user has a corporate ` + "`.pptx`" + `, they can import it as a template (Studio → Slides → Import ` + "`.pptx`" + `) so the deck matches their brand; then it appears in ` + "`list_templates`" + `.

Good defaults when unspecified — but note the template is NOT one of them: a title slide + one section + 4–6 content slides, concise bullets, speaker notes with the talking points. **The template is never defaulted silently** — if the user didn't name one or delegate the choice, ask them (Workflow step 1).

---

## The Canvas

- Fixed **1920 × 1080** logical pixels (16:9). Every coordinate is an **integer logical pixel**.
- ` + "`x`" + `, ` + "`y`" + ` = top-left; ` + "`w`" + `, ` + "`h`" + ` = size. **Never use percentages or a 0–100 coordinate system** — validation rejects it. Keep elements inside 0–1920 / 0–1080.
- Comfortable margins: content usually lives within x∈[96, 1824], y∈[72, 1008].

---

## ASD v2 Element Vocabulary

ASD v2 is a **superset of v1**: all the token attributes (` + "`fill-token`" + `, ` + "`line-token`" + `, ` + "`color-token`" + `, ` + "`font-token`" + `) still work, and v2 adds raw colors, gradients, rotation, rich text, and geometry. Prefer **readable contrast** (light ink on dark surfaces, dark ink on light).

### ` + "`<ast-slide>`" + ` (the root)
Exactly one per ` + "`write_slide`" + ` call. Attributes: ` + "`id`" + ` (required), ` + "`title`" + `, ` + "`lang`" + `. Children: any of the elements below plus ` + "`<ast-notes>`" + `.

### ` + "`<ast-text>`" + ` — text boxes
Geometry required (` + "`x/y/w/h`" + `). Renders **plain text** — never put Markdown markers (` + "`**`" + `, ` + "`_`" + `, ` + "`#`" + `) inside it.
Attributes: ` + "`id`" + `, ` + "`role`" + `, ` + "`inset`" + `, ` + "`align`" + ` (l|ctr|r), ` + "`anchor`" + ` (t|ctr|b vertical), ` + "`wrap`" + `, ` + "`size`" + `, ` + "`weight`" + `, ` + "`font-token`" + `/` + "`color-token`" + ` (theme) or ` + "`font`" + `/` + "`color`" + ` (raw), ` + "`rot`" + ` (degrees), ` + "`alt`" + `/` + "`decorative`" + `.
For **mixed formatting within one text box**, nest ` + "`<ast-run>`" + ` children instead of plain text:

### ` + "`<ast-run>`" + ` — rich text runs (child of ` + "`<ast-text>`" + `)
Each run is a styled span. Attributes: ` + "`b`" + ` (bold), ` + "`i`" + ` (italic), ` + "`u`" + ` (underline), ` + "`color`" + `, ` + "`font`" + `, ` + "`size`" + `, ` + "`weight`" + `. Run text is the element's text content.

### ` + "`<ast-shape>`" + ` — shapes, backgrounds, accents
Required: ` + "`id`" + `, ` + "`kind`" + `, ` + "`x/y/w/h`" + `.
Styling: ` + "`fill-token`" + `/` + "`line-token`" + `/` + "`line-width`" + ` (theme) OR ` + "`fill`" + `/` + "`line`" + ` (raw ` + "`#RRGGBB`" + `/` + "`#RRGGBBAA`" + `/` + "`rgb()`" + `/` + "`rgba()`" + `). Plus ` + "`line-dash`" + ` (solid|dash|dot), ` + "`head-end`" + `/` + "`tail-end`" + ` (none|arrow|triangle), ` + "`rot`" + ` (degrees), ` + "`opacity`" + ` (0–1).
` + "`geom`" + ` presets: ` + "`rect roundRect ellipse triangle rtTriangle diamond parallelogram trapezoid hexagon octagon star5 rightArrow leftArrow chevron cloud can cube line bracketPair`" + `. Or a custom ` + "`path`" + ` (SVG-subset: M L C Q Z H V + numbers).
A shape may contain an ` + "`<ast-text>`" + ` child (a label) and a **gradient** as a JSON script child:
` + "```html" + `
<ast-shape id="bg" kind="rect" geom="rect" x="0" y="0" w="1920" h="1080">
  <script type="application/json" id="bg-grad">{"kind":"linear","angle":90,"stops":[{"pos":0,"color":"#0b1220"},{"pos":100,"color":"#1e293b"}]}</script>
</ast-shape>
` + "```" + `

### ` + "`<ast-image>`" + ` — images
Required: ` + "`id`" + `, ` + "`asset-ref`" + `, ` + "`x/y/w/h`" + `. Optional: ` + "`fit`" + ` (contain|cover), ` + "`rot`" + `, ` + "`opacity`" + `, ` + "`alt`" + `/` + "`decorative`" + `. Reference assets by their ` + "`asset-ref`" + ` (template assets are already seeded onto the deck).

### ` + "`<ast-group>`" + ` — grouping
Required: ` + "`id`" + `, ` + "`x/y/w/h`" + `. Optional ` + "`rot`" + `. Children use **absolute canvas geometry** (not relative to the group).

### ` + "`<ast-table>`" + ` / ` + "`<ast-chart>`" + `
Data-driven. ` + "`<ast-table>`" + `: ` + "`id`" + `, ` + "`data-ref`" + `, ` + "`x/y/w/h`" + `, ` + "`header`" + `, ` + "`style-token`" + `. ` + "`<ast-chart>`" + `: ` + "`id`" + `, ` + "`kind`" + `, ` + "`data-ref`" + `, ` + "`x/y/w/h`" + `, ` + "`category-key`" + `, ` + "`value-keys`" + `. Provide the data as a sibling ` + "`<script type=\"application/json\" id=\"...\">`" + ` block referenced by ` + "`data-ref`" + `.

### ` + "`<ast-notes>`" + `
Speaker notes. Prefer passing ` + "`notes`" + ` to ` + "`write_slide`" + ` instead.

---

## Styling Guidance

- Make slides look **designed**: full-canvas background shapes (solid or gradient), an accent bar or shape, a clear type hierarchy (large bold title, comfortable body).
- Use the template's tokens/assets so the look is consistent; override with raw colors only for deliberate accents.
- Keep strong contrast — never place dark text on a dark background (the classic "black-on-black" failure).
- Never emit Markdown markers inside ` + "`<ast-text>`" + `; use ` + "`size`" + `/` + "`weight`" + `/` + "`<ast-run>`" + ` for emphasis.
- **Never substitute an ` + "`astonish-app`" + ` for a requested deck** — presentations are slides, not apps.

---

## Examples

### 1) Title slide (dark template, gradient background + accent)
` + "```html" + `
<ast-slide id="s0" title="Q4 Business Review">
  <ast-shape id="bg" kind="rect" geom="rect" x="0" y="0" w="1920" h="1080" decorative="true">
    <script type="application/json" id="g0">{"kind":"linear","angle":115,"stops":[{"pos":0,"color":"#0b1220"},{"pos":100,"color":"#1e293b"}]}</script>
  </ast-shape>
  <ast-shape id="accent" kind="rect" geom="rect" x="160" y="560" w="220" h="10" fill="#f59e0b" decorative="true"></ast-shape>
  <ast-text id="title" role="title" x="160" y="380" w="1600" h="160" color="#e2e8f0" size="84" weight="700">Q4 Business Review</ast-text>
  <ast-text id="subtitle" x="160" y="600" w="1600" h="80" color="#94a3b8" size="34">Revenue, retention, and the road to Q1</ast-text>
</ast-slide>
` + "```" + `

### 2) Content slide (bulleted body via rich runs)
` + "```html" + `
<ast-slide id="s2" title="Highlights">
  <ast-shape id="bg" kind="rect" geom="rect" x="0" y="0" w="1920" h="1080" fill="#0b1220" decorative="true"></ast-shape>
  <ast-text id="h" role="title" x="120" y="96" w="1680" h="120" color="#e2e8f0" size="56" weight="700">Highlights</ast-text>
  <ast-text id="body" x="120" y="280" w="1680" h="640" color="#cbd5e1" size="34">
    <ast-run b color="#f59e0b">Revenue </ast-run>
    <ast-run>up 18% YoY, driven by enterprise renewals.</ast-run>
  </ast-text>
</ast-slide>
` + "```" + `

### 3) Section / transition slide with a shape accent
` + "```html" + `
<ast-slide id="s1" title="Financials">
  <ast-shape id="bg" kind="rect" geom="rect" x="0" y="0" w="1920" h="1080" fill="#111827" decorative="true"></ast-shape>
  <ast-shape id="chev" kind="chevron" geom="chevron" x="140" y="470" w="160" h="140" fill="#38bdf8" rot="0" opacity="0.9" decorative="true"></ast-shape>
  <ast-text id="sec" role="title" x="340" y="470" w="1440" h="140" color="#f8fafc" size="72" weight="700" anchor="ctr">01 — Financials</ast-text>
</ast-slide>
` + "```" + `

---

## Quick Checklist

- [ ] Called ` + "`list_templates`" + ` and either used the user's named template, or (if they delegated) picked one and said so, or (if unspecified) asked the user to choose before creating the deck.
- [ ] Called ` + "`create_deck`" + ` **with** the ` + "`template`" + ` argument.
- [ ] Built a title slide, section transitions, and content slides from the archetypes.
- [ ] Replaced all ` + "`{{TITLE}}`" + `/` + "`{{BODY}}`" + ` placeholders; readable contrast throughout.
- [ ] Added speaker notes via ` + "`write_slide`" + `'s ` + "`notes`" + `.
- [ ] Ran ` + "`validate_deck`" + ` and fixed all errors.
`
