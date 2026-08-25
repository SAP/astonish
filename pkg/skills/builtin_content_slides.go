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

   **A template may offer MULTIPLE variants per role, and each variant is now tagged with a ` + "`tier`" + `.** ` + "`list_templates`" + ` returns an ` + "`archetypes`" + ` list of ` + "`{kind, label, tier, fillSlots}`" + ` entries — each a distinct source **layout** carrying its own background/logos/accent chrome, **labeled with the real PowerPoint layout name** (e.g. "Blue cover, anvil and image", "Pink cover with anvil", "Divider Page with Image", "Full Bleed Image"), and marked ` + "`tier`" + `=` + "`fixed`" + ` (**STABLE BRAND CHROME**) or ` + "`tier`" + `=` + "`flexible`" + ` (**CONTENT**). The ` + "`fillSlots`" + ` field lists exactly which element ids inside that archetype hold text you may change (the ` + "`{{TITLE}}`" + `/` + "`{{BODY}}`" + ` holes). **The stable chrome set — ` + "`title`" + ` (cover), ` + "`section`" + ` (divider), ` + "`agenda`" + `, and ` + "`closing`" + ` (thank-you/end) — is ALWAYS available** for every imported template (see the "Imported corporate ` + "`.pptx`" + ` templates" section). When a role has several variants, **ask the user which to use — visually, one question at a time** (see "Asking the user with ` + "`ask_user`" + `" below) — unless the user already specified. Do not silently pick the first variant, and **do not enumerate the variants as text — you MUST use the ask_user tool to ask (see below).**

2. **Create the deck WITH that template — call ` + "`create_deck`" + ` and pass ` + "`template`" + `.**
   Passing the ` + "`template`" + ` name seeds the deck's theme tokens **and** assets, so every slide is styled automatically. ` + "`create_deck`" + ` returns the template's full ` + "`archetypes`" + ` (the ready-made ` + "`title`" + `/` + "`section`" + `/` + "`content`" + ` slide skeletons with ` + "`{{TITLE}}`" + `/` + "`{{BODY}}`" + ` placeholders) — this is where you get the markup to fill. Example arguments:
   ` + "```json" + `
   { "slug": "q4-review", "title": "Q4 Business Review", "template": "midnight" }
   ` + "```" + `
   **Never call ` + "`create_deck`" + ` without a template for a normal presentation request.** Only skip the template if the user explicitly wants a blank canvas — and even then set readable ` + "`theme`" + ` tokens so text is legible.

3. **Run the visual questionnaire BEFORE authoring any content slide — call ` + "`ask_user`" + `, one question at a time.**
   This step is **not optional**. After the deck is created and you know the topic/content, ask the user a short, visual questionnaire so version zero already matches how they want their information shown. Two parts:
   - **Static picks** (always, when the template has them): title/cover variant → agenda yes/no → divider/section variant. See "Asking the user with ` + "`ask_user`" + `".
   - **Adaptive content questions** (this is the part that makes the deck feel bespoke): look at the actual content you're about to build and ask targeted questions about HOW to present it. **If the content shows one or more strong signals (numbers, a comparison, dates/phases, a process, notable length), you MUST ask at least one adaptive question — do not silently default to bullets.** Cap the adaptive questions at **5**, ask only when the answer changes what you build, and skip a signal that isn't strong. See "Adaptive content questions" for the signal→question map. **Do not proceed to ` + "`write_slide`" + ` for the body slides until the questionnaire is done.**

4. **Build slides from the archetypes — call ` + "`write_slide`" + ` once per slide.**
   Honor every questionnaire answer (e.g. build an ` + "`ast-chart`" + `, a comparison table, or a timeline instead of plain bullets when the user chose it). How you fill an archetype depends on its ` + "`tier`" + ` — this is the **TWO-TIER rule**:
   - **FIXED chrome slides** (` + "`tier`" + `=` + "`fixed`" + ` — the ` + "`title`" + `/` + "`section`" + `/` + "`agenda`" + `/` + "`closing`" + ` roles): copy the archetype markup **VERBATIM** into ` + "`write_slide`" + ` and change **ONLY the text inside the element ids listed in that archetype's ` + "`fillSlots`" + `** (the ` + "`{{TITLE}}`" + `/` + "`{{BODY}}`" + ` holes). Do **NOT** move, resize, recolor, add, or remove any shape/image/background. **Never "rebuild the slide from its elements" and never add your own accent shapes or backgrounds** — that is exactly what produces an off-brand white slide with a stray shape. Keep every pixel of the branded chrome; touch only the ` + "`fillSlots`" + ` text.
   - **FLEXIBLE content slides** (` + "`tier`" + `=` + "`flexible`" + `, or built-in templates whose archetypes carry no ` + "`tier`" + `): start from the archetype and **ADAPT the body region to the content type** — bullets, a small table, a simple chart, or an image+caption — while keeping the template background/tokens intact.

   Structure a deck as:
   - a **title** (cover) archetype for slide 0 (position 0),
   - an optional **agenda** chrome slide near the front,
   - a **section** (divider) archetype as a transition slide before each major topic,
   - **content** archetypes for the body slides,
   - a **closing** (thank-you/end) chrome slide to finish.

   ` + "`agenda`" + ` and ` + "`closing`" + ` are part of the stable brand chrome and are always available (real or synthesized in the template's own style) — reproduce them verbatim and fill only their ` + "`fillSlots`" + ` text, exactly like ` + "`title`" + ` and ` + "`section`" + `.
   ` + "`write_slide`" + ` takes ` + "`deck_slug`" + `, a zero-based ` + "`position`" + ` (writing an occupied position replaces it), ` + "`markup`" + ` (exactly one complete ` + "`<ast-slide>`" + ` root), and optional ` + "`notes`" + ` (speaker notes — use this field, do not embed ` + "`<ast-notes>`" + ` yourself unless you need to).

5. **Inspect before revising — call ` + "`get_deck`" + `.** It returns the deck and its ordered slide markup so you can edit precisely.

6. **Validate before declaring done — call ` + "`validate_deck`" + `.** It returns structured diagnostics. **Fix every error before continuing** — do not leave a deck with validation errors.

You can also ` + "`list_decks`" + ` to see existing decks (template decks are hidden from this list).

---

## Asking the user with ` + "`ask_user`" + ` (visual variant picker)

When a role has multiple variants, or you need a yes/no decision, ask the user with the generic ` + "`ask_user`" + ` tool so Studio renders an **inline, visual, one-question-at-a-time card** instead of a plain-text list. **This is a hard rule: NEVER enumerate the variants as a numbered/bulleted list in your chat reply and ask the user to type a choice — that bypasses the visual picker; always call the ask_user tool.** Ask **ONE question at a time, in this sequence**, waiting for each answer before the next:

1. **Title / cover** — call ` + "`ask_user`" + ` with ` + "`kind: \"select\"`" + `, ` + "`slidesTemplate: <the template name>`" + `, and ` + "`slidesKind: \"title\"`" + `. That's it — ` + "`ask_user`" + ` fetches the variant previews itself and shows the user a **live mini-render (thumbnail) of each cover**, auto-generating one option per variant. (You may pass explicit ` + "`options`" + ` to control labels/order, but you do **not** need to and must **not** hand-copy markup or thumbnails.)
2. **Agenda** — call ` + "`ask_user`" + ` with ` + "`kind: \"yesno\"`" + `, ` + "`prompt: \"Would you like an agenda slide?\"`" + `.
3. **Divider / section** — same as step 1 but with ` + "`slidesKind: \"section\"`" + `.

Repeat the same ` + "`slidesTemplate`" + ` + ` + "`slidesKind`" + ` select pattern for any other role that has multiple variants (` + "`agenda`" + `, ` + "`closing`" + `, ` + "`content`" + `). After each ` + "`ask_user`" + ` call, **end your turn** — the user's next message is their answer (the chosen label, or ` + "`Yes`" + `/` + "`No`" + `). Do not proceed to ` + "`write_slide`" + ` for that role until they answer.

> The old manual path (calling ` + "`get_template_variant_previews`" + ` yourself and passing each variant's ` + "`markup`" + ` as an ` + "`ask_user`" + ` ` + "`slides-archetype`" + ` thumbnail) still works, but prefer ` + "`slidesTemplate`" + ` — it is fewer tokens and guarantees the thumbnails actually appear.

**Terminal fallback:** in the terminal chat client there are no thumbnails — ` + "`ask_user`" + ` degrades to the prompt followed by a numbered list of the option labels, and the user answers by typing the number or label. This is the same information as the old plain-text list, so behavior is consistent everywhere; you do not need to do anything special for the terminal.

### Adaptive content questions (make the deck ready from v0)

The picks above (title, agenda yes/no, divider) are the **static** questions — always ask those when they apply. Beyond them, ask a SMALL number of **adaptive** questions that are driven by the actual content you're about to build, so the first version already reflects how the user wants their information shown — not a generic wall of bullets. Use the same ` + "`ask_user`" + ` tool (` + "`select`" + ` or ` + "`yesno`" + `), one question at a time.

**Hard limits — respect these to avoid question fatigue:**
- **At most 5 adaptive questions total**, on top of the static picks. Fewer is better.
- Ask an adaptive question **only if the answer would materially change what you build** (a different visual form, an extra slide, a different structure). If a sensible default is obvious, **just use it and don't ask.**
- Never ask about something the user already told you, and never ask two questions that resolve to the same decision.
- Prefer questions the content *invites*. If the topic has no numbers, don't ask about charts; if there are no phases/dates, don't ask about a timeline.

**Decide adaptively from the material you've gathered.** Inspect the content and, for each strong signal, consider ONE targeted question. Examples (illustrative, not a fixed script):
- The content has **metrics / quantities / trends** → "Show these figures as a **chart** or keep them as a **table/bullets**?" (` + "`select`" + `: Bar chart / Line chart / Table / Bullets).
- The content **compares two or more things** → "Present the comparison as a **side-by-side layout**, a **comparison table**, or **pros/cons columns**?"
- The content has **events, phases, milestones, or dates** → "Show the sequence as a **visual timeline** or a **plain list**?"
- The content is a **process or steps** → "Render as a **numbered step diagram** or **bullets**?"
- The content is **long** → "Prefer **more slides with less on each** (recommended) or a **denser** deck?"
- A **section clearly warrants emphasis** → "Add a **highlight / key-takeaway** slide for <topic>?" (` + "`yesno`" + `).
- The deck could benefit from a **closing** → "End with a **thank-you / next-steps** slide?" (` + "`yesno`" + `) — only if the template has a ` + "`closing`" + ` role and you haven't already decided.

Sequence: do the static picks first (title → agenda → divider), then the adaptive questions (most impactful first), then build. If NONE of the adaptive signals are strong, ask nothing extra and proceed with good defaults — a fast, clean deck beats an interrogation. When the user answers, honor the choice in the slides you author (e.g. build an ` + "`ast-chart`" + ` timeline instead of a bullet list).

---

## Imported corporate ` + "`.pptx`" + ` templates

When the user imported a real corporate ` + "`.pptx`" + ` (Studio → Slides → Import ` + "`.pptx`" + `), it becomes ONE **high-fidelity ASD template** (the pptx is a single design system — one master, one theme). Its source **layouts** are extracted into role-classified, **layout-name-labeled variants**, each tagged with a ` + "`tier`" + ` and preserving that layout's background (including full-bleed image backgrounds), logos, accent bars, and curved/brand custom-path shapes — not just a set of theme colors. Colorful cover/divider chrome is captured by resolving the master→layout inheritance chain (backgrounds and decorative shapes flow down from the slide master into each layout, the way PowerPoint renders them).

**Every imported template is GUARANTEED to provide the stable brand-chrome set** — ` + "`title`" + ` (cover), ` + "`section`" + ` (divider), ` + "`agenda`" + `, and ` + "`closing`" + ` (thank-you/end). These are the ` + "`tier`" + `=` + "`fixed`" + ` roles. If the source ` + "`.pptx`" + ` lacks a real branded layout for one of them, a chrome slide is **synthesized in the template's own style** (same background/logo/accent tokens) — **never a blank white slide**. All other layouts (e.g. "Title and Content", "Full Bleed Image") are ` + "`tier`" + `=` + "`flexible`" + ` content archetypes. Expect **multiple variants per role** — e.g. several ` + "`title`" + ` covers ("Blue cover, anvil and image", "Pink cover with anvil", "White cover with green anvil"), several ` + "`section`" + ` dividers ("Divider Page", "Divider Page with Image") — the human label is always the real PowerPoint layout name. The lossless source model is also persisted for future in-browser editing.

Use it exactly like a built-in: call ` + "`create_deck`" + ` with its ` + "`template`" + ` name, then fill the archetypes with ` + "`write_slide`" + ` following the TWO-TIER rule (Workflow step 4). **For a chrome variant, reproduce it VERBATIM and fill only its ` + "`fillSlots`" + ` text** — do not hand-build, rebuild from elements, or add your own backgrounds/accents. For a flexible content archetype, adapt the body to the content type while keeping the template chrome. The one extra step: because these templates carry multiple variants per role, **ask the user which title/section/agenda/closing/content variant to use with the visual ` + "`ask_user`" + ` picker** (one question at a time — see "Asking the user with ` + "`ask_user`" + `") when more than one exists for a role they need — unless they already told you. A small number of genuinely inexpressible constructs (e.g. EMF vector icons) may be approximated or omitted; the import records a warning for each so nothing degrades silently.

---

## Gathering Requirements (don't stall, but aim right)

Before authoring, settle these — ask the user only what you genuinely can't infer, otherwise pick sensible defaults and proceed:

- **Audience & purpose** — execs vs. engineers vs. customers changes tone and density.
- **Length** — how many slides? Default to a tight 5–8 for an overview unless told otherwise.
- **Key points per slide** — 3–5 bullets max; one idea per slide. Prefer more slides over crowded ones.
- **Tone / brand** — informs your *recommendation*, but the template is the user's choice: present the ` + "`list_templates`" + ` options and let them pick (see Workflow step 1). Never auto-select a template just because you inferred a tone.
- **Existing material** — if the user has a corporate ` + "`.pptx`" + `, they can import it as a template (Studio → Slides → Import ` + "`.pptx`" + `) so the deck matches their brand; then it appears in ` + "`list_templates`" + `.

Good defaults when unspecified — but note the template is NOT one of them: a title slide + one section + 4–6 content slides, concise bullets, speaker notes with the talking points. **The template is never defaulted silently** — if the user didn't name one or delegate the choice, ask them (Workflow step 1).

After you know the template and rough content, run the visual questionnaire: the static picks (title / agenda / divider) **and** up to 5 **adaptive** content questions chosen from the material — charts vs. tables, timelines, comparisons, emphasis slides, etc. See "Adaptive content questions" above. Ask only what materially changes the deck so version zero already matches how the user wants their information presented.

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
- **Only FLEXIBLE content slides are designed freely.** For FIXED chrome slides (` + "`title`" + `/` + "`section`" + `/` + "`agenda`" + `/` + "`closing`" + `) do **NOT** add your own background or accent shapes — reproduce the branded (or synthesized) chrome verbatim and edit only its ` + "`fillSlots`" + ` text; adding backgrounds/accents there is what produces an off-brand white slide with a stray shape.
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
- [ ] If the chosen template offers multiple variants for a role you need, asked the user which variant with the visual ` + "`ask_user`" + ` picker (thumbnails from ` + "`get_template_variant_previews`" + `), one question at a time, plus a ` + "`kind=\"yesno\"`" + ` ask for the agenda slide.
- [ ] Asked up to 5 **adaptive** content questions where they materially change the deck (chart vs. table, timeline vs. list, comparison layout, emphasis/closing slide) — and skipped them entirely when no strong signal was present (no fatigue).
- [ ] For on-brand slides, started from a variant chosen by its label (the real PowerPoint layout name) rather than hand-building.
- [ ] For chrome slides (` + "`title`" + `/` + "`section`" + `/` + "`agenda`" + `/` + "`closing`" + `), reproduced the branded/synthesized variant verbatim and edited only its ` + "`fillSlots`" + ` text (did not rebuild it or add backgrounds/accents).
- [ ] Called ` + "`create_deck`" + ` **with** the ` + "`template`" + ` argument.
- [ ] Built a title slide, section transitions, and content slides from the archetypes.
- [ ] Replaced all ` + "`{{TITLE}}`" + `/` + "`{{BODY}}`" + ` placeholders; readable contrast throughout.
- [ ] Added speaker notes via ` + "`write_slide`" + `'s ` + "`notes`" + `.
- [ ] Ran ` + "`validate_deck`" + ` and fixed all errors.
`
