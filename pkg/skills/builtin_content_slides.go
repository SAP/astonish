package skills

// BuiltinSlides is loaded via skill_lookup("slides") when the agent authors a
// presentation. The always-on prompt keeps only a short pointer; this file is
// the working contract: pick a template, fill catalog slots, never reprint chrome.

const BuiltinSlides = "# Astonish Slides — Authoring\n" + `
Load this skill for a presentation, slide deck, PowerPoint, or ` + "`.pptx`" + `.

**Start from a template.** Never ship an unstyled deck.
**When a template is active, author with ` + "`fill_slides`" + ` (the whole deck in one call).** Do not copy ast-slide markup and do not call ` + "`write_slide`" + `. ` + "`fill_slide`" + ` is only for a later single-slide edit.
**The template choice belongs to the user.** Do not silently pick midnight, aurora, or any imported theme because it "fits the topic."

---

## Workflow (in order)

1. **Pick a template — the user picks, you do not.** Call ` + "`list_slide_templates`" + ` (not ` + "`list_templates`" + ` — that is an email MCP tool). Then apply EXACTLY one of these cases:
   - **The user named a template** (e.g. "use midnight", "our corporate template") → use it and proceed.
   - **The user explicitly delegated** with words like "you pick", "choose whatever", "your call", "surprise me" → pick, say which and why in one line, proceed.
   - **The user said nothing about the template** (the common case: "make a deck about X") → **STOP. Do not call ` + "`create_deck`" + `.** Call ` + "`ask_user`" + ` with ` + "`kind: \"select\"`" + ` and ` + "`slidesTemplatePicker: true`" + ` (omit ` + "`options`" + `). That shows a card with a live thumbnail of each template's cover. **End your turn.** The user's next message is the chosen template.

   Inferring a tone ("dark tribute", "professional", "colorful") is **not** permission to choose. When in doubt, treat it as "said nothing" and ask.

   **VIOLATION — never list templates in chat text.** Do not write "Here are the templates: 1. Light Corporate 2. Midnight 3. Aurora" or describe them in prose and ask the user to type a name. If you find yourself typing template names into the reply, delete that text and call ` + "`ask_user`" + ` instead.

2. **Create the deck.** ` + "`create_deck`" + ` with ` + "`template`" + `. It returns a slim catalog (kind, label, tier, fillSlots, slotHints, summary, thumbnailRef) and optional ` + "`styleGuide`" + `. It does not return markup.
   - **recipe-*** entries are the default cover and body layouts. They use the template's colors, fonts, logo, and legal line. Named slots (eyebrow, headline, body_1, item_1_title, …) — not ph-1.
   - pattern-* entries are sample-derived designed slides. Use only when their structure matches the same job as a recipe (same slot count).
   - fixed title / section / agenda / closing keep official brand chrome. Skip them unless the user asked for the official cover or an agenda slide.

3. **Do not stall on chrome pickers.** After create_deck, go straight to authoring with recipe-*. Only call ` + "`ask_user`" + ` for cover/agenda/section variants if the user asked to use the official title/section layouts. Do not re-call ` + "`list_slide_templates`" + `, ` + "`get_template_variant_previews`" + `, ` + "`get_deck`" + `, or ` + "`skill_lookup`" + `.

4. **Author with ` + "`fill_slides`" + ` — the whole deck in ONE call.** Do not emit one ` + "`fill_slide`" + ` per slide; that is one LLM round-trip per slide and makes a 18-slide deck take minutes. Plan the story as jobs, then pick a recipe whose slot count matches. Example:
` + "```json\n" + `{ "deck_slug": "q4-review", "slides": [
  { "position": 0, "kind": "recipe-cover", "fills": { "eyebrow": "FY26 BOARD PRE-READ", "headline": "Q3 missed plan by 4 pts", "headline_2": "on enterprise renewals", "dek": "Cut two product bets and move $11M to the renewal team by 21 May.", "meta_1_label": "Ask", "meta_1_value": "Approve the cut", "meta_2_label": "Owner", "meta_2_value": "CRO" } },
  { "position": 1, "kind": "recipe-split-narrative", "fills": { "eyebrow": "The miss", "date": "Q3", "headline": "Enterprise renewals", "headline_2": "drove 78% of the gap", "body_1": "A complete paragraph that states the evidence and the implication.", "item_1_title": "Cause", "item_1_body": "A complete sentence that fits the box." } },
  { "position": 2, "kind": "recipe-three-up", "fills": { "eyebrow": "The move", "headline": "Three cuts that fund the recovery", "item_1_kicker": "Bet A", "item_1_title": "Pause", "item_1_body": "A complete thought, 12–22 words." } }
] }` + "\n```" + `
   fills keys are fillSlots ids from the catalog. Optional slots (role optional) may be omitted.
   **Pick layout by job, never by richness:**
   - Cover → ` + "`recipe-cover`" + ` (thesis in the dek, not a topic label). Template logo/legal are applied by the server.
   - One story + 3 claims → ` + "`recipe-split-narrative`" + `
   - Quote + story → ` + "`recipe-quote-split`" + `
   - Pair / contrast → ` + "`recipe-two-up`" + `
   - Three items or a short timeline → ` + "`recipe-three-up`" + `
   - 3–4 metrics → ` + "`recipe-stat-row`" + `
   - Six principles → ` + "`recipe-numbered-grid`" + `
   - Argument + lesson → ` + "`recipe-callout-rail`" + `
   - Giant year + 3 points → ` + "`recipe-year-hero`" + `
   - Last slide → ` + "`recipe-closer`" + ` (thesis + 3 questions or takeaways)
   **A chapter is an eyebrow on a full content slide.** Do not insert empty section dividers. Prefer 8–16 dense slides over 18 sparse ones. Use at least 3 different recipe-* kinds in a deck longer than 6 slides.
   **Fill every required text slot.** The server rejects a slide that leaves a required slot empty. If you have 3 items, use three-up, not a 6-cell grid.
   **Titles are takeaways.** Complete sentence, or a two-line split headline that states the claim — not "Early life" or "Market Overview".
   **Density.** Headline + 2–4 content blocks. Body columns are short paragraphs (~40–70 words). Cards/items are a complete thought (~12–22 words). Empty canvas is a defect; 6×6 is a bullet cap, not permission to leave the rest blank.
   Never fill a slot with DRAFT, CONFIDENTIAL, yyyy-MM-dd, <date>, <initials>, "footnote", or "Contact information:".
   - ` + "`content-2`" + ` / Title and Text is last resort. Do not pick a photo-only layout for story content.
   After review, fill or rewrite the slides named — do not rebuild the whole deck.

5. **` + "`validate_deck`" + `** — fix every error.

6. **` + "`review_deck`" + `** — fix warning-level findings **on the slides they name**. Do not rebuild the whole deck. Then re-run until there are no warnings.

` + "`write_slide`" + ` is only for a blank canvas the user explicitly asked for. ` + "`get_archetype`" + ` is an escape hatch; do not use it to copy chrome.

---

## Catalog reading

- recipe-cover / recipe-split-narrative / recipe-quote-split / recipe-two-up / recipe-three-up / recipe-stat-row / recipe-numbered-grid / recipe-callout-rail / recipe-year-hero / recipe-closer — default layouts. Named slots. Themed from the style guide (colors, fonts, logo, legal).
- pattern / pattern-2 / … — designed example slides from the imported pptx. Optional, only when they match the same job.
- title / section / agenda / closing (and -2, -3 variants) — official brand chrome. Skip unless requested.
- slotHints name each region (eyebrow, headline, item_1_body). Do not invent ph-N keys on a recipe.

If styleGuide is present, follow its type scale, colors, and the Layout types (recipe-*) section first.

---

## Asking with ` + "`ask_user`" + `

HARD RULE: never enumerate templates or variants as a numbered/bulleted list or prose menu in chat. The visual picker is the only correct UI.

- **First question (template):** ` + "`kind: \"select\"`" + `, ` + "`slidesTemplatePicker: true`" + `, omit options. Then **end the turn**.
- Cover/divider/content variants: ` + "`slidesTemplate`" + ` + ` + "`slidesKind`" + ` (title|section|agenda|closing|content). content includes patterns.
- After every ` + "`ask_user`" + ` call, end the turn — do not create_deck or fill_slides until they answer.

---

## Imported corporate templates

A pptx import supplies the style guide (colors, fonts, logo, legal) and optional pattern-* samples. Author with recipe-* so every slide has a job and a full layout; the server paints template chrome onto the recipe. Do not pour a story into whichever pattern-* ranked richest.

---

## Canvas (blank-canvas only)

1920x1080 integer px. Tags: ast-slide, ast-text (+ ast-run), ast-shape, ast-image (asset-ref), ast-table/ast-chart (JSON data-ref). No Markdown inside ast-text. Adjacent ast-run elements concatenate with no inserted space. Prefer fill_slides; this vocabulary is for the rare no-template case.

---

## Checklist

- [ ] Template chosen by the user: named, explicitly delegated, or via ask_user slidesTemplatePicker — never auto-selected from inferred tone
- [ ] create_deck with template; catalog in hand
- [ ] Straight to authoring with recipe-* (no chrome-picker stall unless the user asked for official title/section)
- [ ] Whole deck via one fill_slides call; cover is recipe-cover; closer is recipe-closer; every required slot filled; chapters are eyebrows not empty dividers; at least 3 recipe kinds on a long deck
- [ ] validate_deck clean; review_deck warnings resolved
`
