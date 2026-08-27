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
   - pattern-* entries are sample-derived designed body slides (cards, colored boxes, icon rows). Prefer these for content.
   - fixed title / section / agenda / closing keep brand chrome — fill their text slots only.
   - Multi-column content layouts are structured grids. Title and Text is last resort.

3. **Visual chrome picks, one at a time.** ` + "`ask_user`" + ` with ` + "`slidesTemplate`" + ` + ` + "`slidesKind`" + `: title → agenda yes/no → section. Then at most 5 adaptive questions if they change what you build. Then commit — do not re-call ` + "`list_slide_templates`" + `, ` + "`get_template_variant_previews`" + `, ` + "`get_deck`" + `, or ` + "`skill_lookup`" + `.

4. **Author with ` + "`fill_slides`" + ` — the whole deck in ONE call.** Do not emit one ` + "`fill_slide`" + ` per slide; that is one LLM round-trip per slide and makes a 18-slide deck take minutes. Example arguments:
` + "```json\n" + `{ "deck_slug": "q4-review", "slides": [
  { "position": 0, "kind": "title", "fills": { "ph-1": "Q4 Business Review", "ph-2": "Revenue, retention, the road to Q1" } },
  { "position": 1, "kind": "agenda", "fills": { "ph-1": "Agenda", "ph-2": "Results", "ph-3": "Plan" } },
  { "position": 2, "kind": "pattern-2", "fills": { "ph-1": "Results", "ph-2": "Revenue +23%", "ph-3": "NPS 61" } }
] }` + "\n```" + `
   Use kind or label from the catalog. fills keys are fillSlots ids; values are text, or a sha256- asset-ref for ph-pic-* image slots.
   **Fit the box.** Read each slotHint. A hint like "card 2 of 6 (top-middle) — one short headline" is ONE phrase in that card, not a heading+body pair with the next slot. Icon-row items match the marker above them. Never treat adjacent slots as heading+body unless the hint says so.
   Card/bar slots get a short headline (about 6–12 words), not a paragraph. Text that does not fit will overflow the colored bar. Never fill a slot with DRAFT, CONFIDENTIAL, yyyy-MM-dd, <date>, <initials>, "footnote", or "Contact information:".
   Fill every text slot with real content. Do not leave dummy sample copy.
   - Slide 0 = unsuffixed ` + "`title`" + ` (the richest cover). If it still has a ` + "`ph-pic-*`" + ` slot, fill it from ` + "`list_deck_assets`" + ` / ` + "`add_deck_image`" + ` or pick a ` + "`title-*`" + ` that already has a photo. Do not ship an empty cyan panel.
   - Optional agenda: use the ` + "`agenda`" + ` catalog entry if one exists. Do not use a 3-bar body pattern as a table of contents (that leaves leftover slots as loose text under the bars).
   - At most ONE section divider (` + "`section`" + `, the image divider when the template has one). Do not insert a title-only divider before every chapter.
   - Body slides: only ` + "`pattern-*`" + ` unless the user asked for a two-column grid / quote / screenshot layout by name. ` + "`content-2`" + ` / Title and Text is last resort, not the default. Use at least 3 different pattern-* entries across the deck.
   - Closing last. Image slots: ` + "`list_deck_assets`" + ` / ` + "`add_deck_image`" + ` then pass the asset-ref.
   After review, fix individual slides with ` + "`fill_slide`" + `; do not rebuild the whole deck.

5. **` + "`validate_deck`" + `** — fix every error.

6. **` + "`review_deck`" + `** — fix warning-level findings **on the slides they name**. Do not rebuild the whole deck. Then re-run until there are no warnings.

` + "`write_slide`" + ` is only for a blank canvas the user explicitly asked for. ` + "`get_archetype`" + ` is an escape hatch; do not use it to copy chrome.

---

## Catalog reading

- pattern / pattern-2 / … — designed example slides from the imported pptx. These have the boxes and cards. Fill their slots; chrome stays.
- title / section / agenda / closing (and -2, -3 variants) — brand chrome. Fill text only.
- content / 2 Columns / 3 Columns — placeholder grids. Use when the pattern catalog has no better match.
- slotHints tell you what ph-2 vs ph-3 is (title, card heading, image).

If styleGuide is present, follow its type scale, colors, and Content Layout Guide.

---

## Asking with ` + "`ask_user`" + `

HARD RULE: never enumerate templates or variants as a numbered/bulleted list or prose menu in chat. The visual picker is the only correct UI.

- **First question (template):** ` + "`kind: \"select\"`" + `, ` + "`slidesTemplatePicker: true`" + `, omit options. Then **end the turn**.
- Cover/divider/content variants: ` + "`slidesTemplate`" + ` + ` + "`slidesKind`" + ` (title|section|agenda|closing|content). content includes patterns.
- After every ` + "`ask_user`" + ` call, end the turn — do not create_deck or fill_slides until they answer.

---

## Imported corporate templates

A pptx import is one template (one master, one theme). Covers are fixed layouts. Body design lives on the example slides, surfaced as pattern-* catalog entries — not as empty Title and Text holes. Filling a pattern is how you use the elements in the example slides. Re-import an old template if it has no pattern-* entries.

---

## Canvas (blank-canvas only)

1920x1080 integer px. Tags: ast-slide, ast-text (+ ast-run), ast-shape, ast-image (asset-ref), ast-table/ast-chart (JSON data-ref). No Markdown inside ast-text. Adjacent ast-run elements concatenate with no inserted space. Prefer fill_slides; this vocabulary is for the rare no-template case.

---

## Checklist

- [ ] Template chosen by the user: named, explicitly delegated, or via ask_user slidesTemplatePicker — never auto-selected from inferred tone
- [ ] create_deck with template; catalog in hand
- [ ] Cover / agenda / divider picks via ask_user, then straight to authoring
- [ ] Whole deck via one fill_slides call; body slides use pattern-* (variety), not Title and Text; at most one section divider
- [ ] validate_deck clean; review_deck warnings resolved
`
