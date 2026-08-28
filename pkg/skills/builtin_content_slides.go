package skills

// BuiltinSlides is loaded via skill_lookup("slides") when the agent authors a
// presentation. The always-on prompt keeps only a short pointer; this file is
// the working contract: intake, official bookends, recipe body, never reprint chrome.

const BuiltinSlides = "# Astonish Slides — Authoring\n" + `
Load this skill for a presentation, slide deck, PowerPoint, or ` + "`.pptx`" + `.

**Start from a template.** Never ship an unstyled deck.
**When a template is active, author with ` + "`fill_slides`" + ` (the whole deck in one call).** Do not copy ast-slide markup and do not call ` + "`write_slide`" + `. ` + "`fill_slide`" + ` is only for a later single-slide edit.
**Intake is iterative.** Ask ONE question per turn with ` + "`ask_user`" + `, then end the turn. Never enumerate templates or variants as a numbered list in chat.

---

## Workflow (in order)

### 1. Intake — one ` + "`ask_user`" + ` per turn

After loading this skill, the **next tool call is ` + "`ask_user`" + `** for the first unanswered intake question. Do not call ` + "`create_deck`" + `, ` + "`list_slide_templates`" + `, or ` + "`fill_slides`" + ` first, and do not write a justification for skipping.

Skip a question **only** when that question's answer is already explicit in the user message (they named the audience, a slide count, a template, or said "you pick" / "your call" / "surprise me" / "don't ask, just make it"). Do not re-ask a named template ("use GCO"). After every ` + "`ask_user`" + `, **end the turn**.

A request to use existing knowledge, skip the web, or not look anything up is a **research constraint** — still ask. Inferring a tone ("dark tribute", "professional") is not a template choice and not an intake skip.

1. **Audience and purpose** (always unless they already said who it is for). ` + "`kind: \"select\"`" + `. Write 4–5 options **derived from this brief** (biography vs board vs classroom vs launch), plus Other. Do not reuse a generic four-option list that ignores the prompt.
2. **Deck length** (always unless they already gave a count). ` + "`kind: \"select\"`" + `. Options: Short ~8 / Standard ~12 / Full ~16–18 / Other.
3. **Who picks the template?** (always unless they named a template or said "you pick"). ` + "`kind: \"select\"`" + ` with explicit options:
   - ` + "`show-templates`" + ` — "Show me the templates"
   - ` + "`you-pick`" + ` — "You pick what fits"
4. **Which template** — only if they chose "Show me the templates". ` + "`kind: \"select\"`" + `, ` + "`slidesTemplatePicker: true`" + `, omit options. **VIOLATION — never list templates in chat text.** Inferring a tone ("dark tribute", "professional") is **not** permission to choose.
5. If they chose **"You pick what fits"** (or said "you pick" / "your call" / "surprise me"): pick using audience + length, **say which and why in one line**, continue. Do not open the visual picker.
6. After the template is known, call ` + "`list_slide_templates`" + ` if you do not already have its variant list and ` + "`palettes`" + `. Then:
   - **Title variant** — only if that template has **2+** ` + "`title`" + ` / ` + "`title-N`" + `. ` + "`slidesTemplate`" + ` + ` + "`slidesKind: \"title\"`" + `, omit options (layout chrome only — sample people/bike photos are stripped; includes "Use the default").
   - **Cover photo** — only if the chosen title kind has a ` + "`ph-pic-*`" + ` well. First ` + "`kind: \"yesno\"`" + `: "This cover has room for one photo from the template. Want to pick one?" If **yes**: ` + "`slidesImagePicker: true`" + ` + ` + "`slidesTemplate`" + `, omit options (unique photos from the example title slides). Pass the chosen option id (` + "`sha256-…`" + `) as ` + "`titleImage`" + ` on ` + "`create_deck`" + `. If **no** / "Use the default": omit ` + "`titleImage`" + ` — the server leaves the well empty and does **not** keep sample photos. A second photo later is ` + "`add_deck_image`" + `, not another title variant. Skip this pair when the cover has no image well or the template has no example photos.
   - **Color palette** — only if ` + "`palettes`" + ` is non-empty (Product Deck / ` + "`product`" + `). ` + "`slidesPalettePicker: true`" + ` + ` + "`slidesTemplate`" + `, omit options. Do **not** invent palettes for imported brand templates (GCO's blue vs pink covers are title layouts, not palettes).
   - **Closing variant** — only if **2+** ` + "`closing`" + ` / ` + "`closing-N`" + `. ` + "`slidesKind: \"closing\"`" + `.
   If there is exactly one title or closing, use it — no question. Still no chrome-picker stall for section/agenda unless the user asked.

### 2. Create the deck

Do **not** call ` + "`create_deck`" + ` until title / cover-photo / palette / closing questions that still apply have been answered. Pass ` + "`template`" + `. Pass a human ` + "`title`" + ` and a short ` + "`description`" + ` (the chat card shows the description, never the persist slug). ` + "`slug`" + ` is a hint only: in chat the server stores a unique per-session slug so two chats on the same topic cannot overwrite each other — use the slug ` + "`create_deck`" + ` returns; if you pass the hint to ` + "`fill_slides`" + `, the server remaps it to this session's deck. Pass ` + "`palette`" + ` (palette id, not hex) when they chose a colorway. Pass ` + "`titleKind`" + ` / ` + "`closingKind`" + ` as the **option id** from ` + "`ask_user`" + ` (the catalog kind: ` + "`title`" + `, ` + "`title-2`" + `, …). Pass ` + "`titleImage`" + ` as the photo picker's option id when they chose a template photo. "Use the default" → omit, or pass the first title/closing. It returns a slim catalog (kind, label, tier, fillSlots, slotHints, summary) plus optional ` + "`palettes`" + ` and ` + "`styleGuide`" + `. It does not return markup.

- **recipe-*** entries are the default **body** layouts. Named slots (eyebrow, headline, body_1, item_1_title, …) — not ph-1.
- **title** / **title-N** and **closing** / **closing-N** are official brand bookends when present in the catalog. Slide 0 **must** be the **chosen** kind (the ` + "`ask_user`" + ` option id, e.g. ` + "`title`" + ` or ` + "`title-12`" + `), not a different variant and not ` + "`recipe-cover`" + `. Fill **those** catalog fillSlots (often ` + "`ph-*`" + `). ` + "`headline`" + ` maps to the title slot and ` + "`dek`" + ` to the subtitle/body slot when the cover does not use recipe ids. Cover photos are **opt-in**: only the photo they picked (` + "`titleImage`" + ` / the matching ` + "`ph-pic-*`" + ` fill). Do **not** keep the template's sample people or bikes. Empty ` + "`ph-pic-*`" + ` wells stay empty. This engine does not generate images.
- Built-in ` + "`product`" + ` / ` + "`midnight`" + ` / ` + "`aurora`" + ` / ` + "`light-corporate`" + ` have **no** branded title/closing in the catalog — use ` + "`recipe-cover`" + ` / ` + "`recipe-closer`" + `.
- The template owns a **skin**: ` + "`light-corporate`" + ` / imported pptx use the corporate language (logo, legal, accent rule). The built-in ` + "`product`" + ` template (label **Product Deck**) uses the product language (mono rails, one accent, panels, terminals) and ships colorways. Same jobs, different furniture.
- section / agenda / pattern-* stay fetchable via ` + "`get_archetype`" + ` but are **not** in the default catalog. Do not stall on them unless the user asked.

### 3. Author with ` + "`fill_slides`" + ` — the whole deck in ONE call

Do not emit one ` + "`fill_slide`" + ` per slide; that is one LLM round-trip per slide. Plan the story as jobs, then pick a recipe whose slot count matches. Example (built-in product; imported decks replace position 0 / last with official title/closing kinds and those slots):
` + "```json\n" + `{ "deck_slug": "q4-review", "slides": [
  { "position": 0, "kind": "recipe-cover", "fills": { "eyebrow": "FY26 BOARD PRE-READ", "headline": "Q3 missed plan by 4 pts", "headline_2": "on enterprise renewals", "dek": "Cut two product bets and move $11M to the renewal team by 21 May.", "meta_1_label": "Ask", "meta_1_value": "Approve the cut", "meta_2_label": "Owner", "meta_2_value": "CRO" } },
  { "position": 1, "kind": "recipe-split-narrative", "fills": { "eyebrow": "The miss", "date": "Q3", "headline": "Enterprise renewals", "headline_2": "drove 78% of the gap", "body_1": "A complete paragraph that states the evidence and the implication.", "item_1_title": "Cause", "item_1_body": "A complete sentence that fits the box." } },
  { "position": 2, "kind": "recipe-three-up", "fills": { "eyebrow": "The move", "headline": "Three cuts that fund the recovery", "item_1_kicker": "Bet A", "item_1_title": "Pause", "item_1_body": "A complete thought, 12–22 words." } }
] }` + "\n```" + `
   fills keys are fillSlots ids from the catalog for this template's skin. Optional slots (role optional) may be omitted. Extra keys the skin does not emit (e.g. ` + "`meta_4_*`" + ` on product cover) are ignored — do not retry the whole deck because of them.
   **Pick layout by job, never by richness:**
   - Cover → official ` + "`title`" + ` / ` + "`title-N`" + ` when listed; otherwise ` + "`recipe-cover`" + ` (thesis in the dek, not a topic label). Template logo/legal are applied by the server **only when the template actually has them**. Never invent a logo, mark, or decorative circle.
   - One story + 3 claims → ` + "`recipe-split-narrative`" + `
   - Quote + story → ` + "`recipe-quote-split`" + `
   - Pair / contrast → ` + "`recipe-two-up`" + `
   - Three items or a short timeline → ` + "`recipe-three-up`" + `
   - 3–4 metrics → ` + "`recipe-stat-row`" + `
   - Six principles → ` + "`recipe-numbered-grid`" + `
   - Argument + lesson → ` + "`recipe-callout-rail`" + `
   - Giant year + 3 points → ` + "`recipe-year-hero`" + `
   - Last slide → official ` + "`closing`" + ` / ` + "`closing-N`" + ` when listed; otherwise ` + "`recipe-closer`" + `. Corporate closer: thesis + 3 takeaways. Product closer: quote (` + "`headline`" + `/` + "`headline_2`" + `) + one-line ` + "`thesis`" + ` + 3 takeaway chips. Eyebrow is LEGACY / THE ASK, never a date range. Never ship a closer that is only two words on empty canvas.
   - Problem / why-now → ` + "`recipe-statement-evidence`" + `
   - Comparison table → ` + "`recipe-data-table`" + `
   - Architecture / layers → ` + "`recipe-layer-stack`" + `
   - How it works → ` + "`recipe-process-terminal`" + `
   **Structured variation (do this):** put ` + "`headline_accent`" + ` as the exact phrase in the headline to paint in accent (one phrase). Set ` + "`emphasis`" + ` to ` + "`\"1\"`" + `, ` + "`\"2\"`" + `, or ` + "`\"3\"`" + ` so exactly one card/column/step is the recommended path. Fill ` + "`detail_1_*`" + ` / ` + "`detail_2_*`" + ` on stat-row when you have supporting points. On ` + "`product`" + `, fill ` + "`prompt`" + ` on the cover and ` + "`cta_body`" + ` on the closer; use ` + "`date`" + ` as the ` + "`// kicker`" + ` line.
   **A chapter is an eyebrow on a full content slide.** Do not insert empty section dividers. Prefer 8–16 dense slides over 18 sparse ones (honor the length they picked). Use at least 3 different recipe-* kinds in a deck longer than 6 slides — mix card layouts with table / stack / terminal / statement-evidence so pages are not all the same boxes.
   **Fill every required text slot.** The server rejects a slide that leaves a required slot empty. If you have 3 items, use three-up, not a 6-cell grid.
   **Titles are takeaways.** Complete sentence, or a two-line split headline that states the claim — not "Early life" or "Market Overview".
   **Density.** Headline + 2–4 content blocks. Body columns are short paragraphs (~40–70 words). Cards/items are a complete thought (~12–22 words). Empty canvas is a defect; 6×6 is a bullet cap, not permission to leave the rest blank.
   Never fill a slot with DRAFT, CONFIDENTIAL, yyyy-MM-dd, <date>, <initials>, "footnote", or "Contact information:".
   - ` + "`content-2`" + ` / Title and Text is last resort. Do not pick a photo-only layout for story content.
   After review, fill or rewrite the slides named — do not rebuild the whole deck.

### 4. ` + "`validate_deck`" + ` — fix every error.

### 5. ` + "`review_deck`" + ` — fix warning-level findings **on the slides they name**. Do not rebuild the whole deck. Then re-run until there are no warnings.

` + "`write_slide`" + ` is only for a blank canvas the user explicitly asked for. ` + "`get_archetype`" + ` is an escape hatch; do not use it to copy chrome.

---

## Catalog reading

- recipe-cover / recipe-split-narrative / recipe-quote-split / recipe-two-up / recipe-three-up / recipe-stat-row / recipe-numbered-grid / recipe-callout-rail / recipe-year-hero / recipe-closer / recipe-statement-evidence / recipe-data-table / recipe-layer-stack / recipe-process-terminal — default **body** (and built-in cover/closer). Named slots. Themed from the template **skin** (corporate vs product) and the chosen **palette**.
- title / title-2 / … and closing / closing-2 / … — official brand bookends when listed. Use them for slide 0 and last. Fill their fillSlots, not recipe ids.
- section / agenda / pattern-* — not in the default catalog; ` + "`get_archetype`" + ` only if the user asked.
- slotHints name each region. Do not invent ph-N keys on a recipe; do not invent eyebrow/dek on an official cover that lists ph-*.

If styleGuide is present, follow its type scale, colors, and the Layout types (recipe-*) section first.

---

## Asking with ` + "`ask_user`" + `

HARD RULE: never enumerate templates or variants as a numbered/bulleted list or prose menu in chat. The visual picker is the only correct UI for those.

- Audience / length / who-picks: ` + "`kind: \"select\"`" + ` with your options (include "You pick what fits" on ownership). Then **end the turn**. Do **not** ask to generate images — this engine cannot generate them. Cover photos come only from ` + "`slidesImagePicker`" + ` when the chosen title has a ` + "`ph-pic-*`" + ` well.
- Template: ` + "`slidesTemplatePicker: true`" + `, omit options. Then **end the turn**.
- Cover/end variants: ` + "`slidesTemplate`" + ` + ` + "`slidesKind`" + ` (` + "`title`" + ` or ` + "`closing`" + `). Title tiles are layouts, not sample photos.
- Cover photo: yes/no, then ` + "`slidesImagePicker: true`" + ` + ` + "`slidesTemplate`" + ` when the chosen title has a ` + "`ph-pic-*`" + ` well.
- Product colorways: ` + "`slidesPalettePicker: true`" + ` + ` + "`slidesTemplate`" + `.
- After every ` + "`ask_user`" + ` call, end the turn — do not create_deck or fill_slides until they answer.

---

## Imported corporate templates

A pptx import supplies the style guide (colors, fonts, logo, legal), official title/closing layouts, and optional pattern-* samples (stored, not in the default catalog). Author the **middle** with recipe-* so every slide has a job and a full layout; put the **real imported cover and end page** at the ends. Do not pour a story into whichever pattern-* ranked richest.

---

## Canvas (blank-canvas only)

1920x1080 integer px. Tags: ast-slide, ast-text (+ ast-run), ast-shape, ast-image (asset-ref), ast-table/ast-chart (JSON data-ref). No Markdown inside ast-text. Adjacent ast-run elements concatenate with no inserted space. Prefer fill_slides; this vocabulary is for the rare no-template case.

---

## Checklist

- [ ] First tool after skill_lookup is ask_user unless that question's answer is already explicit
- [ ] Research-only constraints (use what you know / don't search) did not skip intake
- [ ] Template: named, "you pick" with one-line why, or slidesTemplatePicker — never auto-selected from inferred tone
- [ ] Title variant asked only when 2+ title* (before create_deck); cover photo asked only when that title has a ph-pic well; palette asked only when palettes exist (Product Deck); closing asked only when 2+ closing*
- [ ] Did not ask to generate images; cover photo is slidesImagePicker from template examples or omitted
- [ ] create_deck with template (+ title, description, palette / titleKind / titleImage / closingKind as chosen — titleKind is the ask_user option id). slug is a hint; persist slug is session-unique
- [ ] Slide 0 is the chosen official title kind when listed (not a different variant); last is official closing when listed, else recipe-closer; body is recipe-*; imported ph-pic photos left in place
- [ ] Whole deck via one fill_slides call; official covers use catalog fillSlots; every required slot filled; chapters are eyebrows not empty dividers; at least 3 recipe kinds on a long deck
- [ ] validate_deck clean; review_deck warnings resolved
`
