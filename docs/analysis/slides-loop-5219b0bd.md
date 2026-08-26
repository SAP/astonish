# Slides authoring loop — root-cause analysis

**Session:** `5219b0bd-7df3-4f6d-8873-336d4481cb5e` ("Create a slide show about Apple history, do not do web search…")
**Store:** `~/.local/share/astonish/orgs/my-org/personal/f623f304-12ab-4ea2-8c29-079e7a973df3.db` (personal scope)
**Window:** 2026-08-25 19:56:34 → 19:59:31 (~3 min), 65 session_events
**Symptom:** the agent keeps calling read-only tools in a loop and never (reliably) reaches `write_slide`.

---

## What the session actually did

| Events | Phase | Notes |
|---|---|---|
| 1–8 | Kickoff | `skill_lookup`, `list_templates`, `search_tools`. OK. |
| 9–46 | Questionnaire | 5× `ask_user` (template, cover, agenda, divider, content layout) — **all answered**. OK. |
| **49–63** | **🔁 Loop** | Repeats `skill_lookup` + `get_deck` + `get_template_variant_previews` and restates "I'll use Title and Text…" without writing. |
| 65 | Escapes | 4× `write_slide` finally land. |

The loop cycle:
```
skill_lookup + get_deck + get_template_variant_previews
  → TEXT "Got it — Title and Text…"  (restates an already-made decision)
  → repeat
```

## The real cause: oversized tool responses → context thrash

Measuring the **tool response payloads** in this session:

| Event | Tool | Bytes | `ast-slide` | `ast-run` | `data:image` |
|---|---|---:|---:|---:|---:|
| ev17 | `create_deck` | **350,302** | 66 | 510 | 0 |
| ev44 | `ask_user` (template picker) | **293,182** | 30 | 366 | 0 |
| ev59 | `get_template_variant_previews` (no `kind`) | **344,862** | 66 | 510 | 0 |
| ev63 | `get_template_variant_previews` (no `kind`) | **281,552** | 30 | 366 | 0 |
| ev63 | `get_template_variant_previews` (×2 more) | 21,959 / 2,241 | 2 / 2 | 26 / 8 | 0 |

Four responses alone total **~1.3 MB** of ASD markup. There are **no `data:` image bytes** (the earlier plan's rule held), but each response embeds the **full archetype markup for every variant** of the imported `2026 GCO IPED` template (66 slides / 510 runs).

**Why this loops:**
1. `get_template_variant_previews` returns **every archetype's full `Markup`** whenever the model omits the `kind` filter (`pkg/docs/slides/tools.go:436–454` — empty `wantKind` skips the filter). The model calls it with no `kind` (ev59, ev63), getting ~340 KB each time.
2. Combined with the 350 KB `create_deck` and 293 KB `ask_user` picker, prior tool results overflow the context window and get compacted/evicted.
3. Once the archetype markup it needs is evicted, the model "forgets" it and **re-fetches** `get_template_variant_previews` / `get_deck` / `skill_lookup` — another ~340 KB — which triggers more eviction. That is the loop.
4. It only escapes when enough context is freed for a `write_slide` batch to squeeze through (ev65).

## What changed ("something you changed broke it")

Commit **`a51f9c0f feat(slides): add template-choice picker to ask_user questionnaire`** made `ask_user` enumerate **every template with a full cover-markup thumbnail** — that is the new 293 KB `ask_user` response (ev44). It added a second heavyweight payload to a flow that already had the 350 KB `create_deck` and the unbounded `get_template_variant_previews`, pushing the session over the context budget and turning "re-gather" into a hard loop.

The `review_deck`/skill changes are **not** the trigger — the loop is entirely before `review_deck` runs.

---

## Fixes (in priority order)

1. **Bound `get_template_variant_previews` output.** It must not return the whole template's markup:
   - Require (or strongly default) a `kind` filter; when omitted, return **one representative variant per role** (or just `{kind,label,tier,fillSlots}` metadata **without** `markup`), and let the caller request a single variant's markup by kind+label.
   - The picker/thumbnail path (`ask_user`) should fetch markup **one role at a time**, never all roles at once.
   - File: `pkg/docs/slides/tools.go` `getTemplateVariantPreviews` (~L423) and the `ask_user` picker (~L670).

2. **Shrink the `ask_user` template picker payload (`a51f9c0f`).** Do not embed full cover markup for *every* template in one response. Send only the chosen/needed cover markup, or resolve thumbnails lazily on the client (it already resolves asset-refs client-side).

3. **Trim `create_deck` (350 KB).** It returns all archetypes' full markup. Consider returning archetype **metadata** by default and fetching a specific archetype's markup on demand at `write_slide` time.

4. **Skill anti-loop guidance.** After the questionnaire is answered and the layout is chosen, the model should go straight to `write_slide` and must NOT re-call `skill_lookup` / `get_deck` / `get_template_variant_previews` to "re-confirm" a decision already made. Add a "commit and author; do not re-gather read-only context" rule to `pkg/skills/builtin_content_slides.go`.

**Guardrail unchanged by design:** none of these should reintroduce `data:` image/font bytes into tool results (keeps `TestSlidesResponsesOmitHeavyManifestFields` green).
