# Slides Templates

Templates define the visual identity and available presentation treatments used by Astonish Slides. Choose a built-in design, create a personal variant, or import an existing PowerPoint template for your brand.

## Template Scopes

The **Slides → Templates** catalog can include templates from several scopes:

| Badge | Availability | Management |
|-------|--------------|------------|
| **Built-in** | Included with Astonish | Immutable; duplicate to customize |
| **Platform** | Everyone on the platform | Managed by a platform administrator |
| **Organization** | Members of the organization | Managed by an organization administrator or owner |
| **Team** | Members of the current team | Managed by a team administrator |
| **Personal** | Only you | Import, duplicate, recolor, or delete in Slides |

In platform deployments, shared templates inherit downward. The catalog shows every template available to the current user and labels its owning scope. Templates with the same display name can appear at different scopes.

<!-- IMAGE: Slides Templates catalog showing scope badges and template actions -->

## Choose a Template

During presentation intake, choose a template from visual previews or let Astonish select one. Depending on the template, you may also choose:

- A palette or color treatment.
- A title or cover variant.
- A logo, cover image, or other brand media.
- A section, agenda, or closing treatment.

Built-in templates provide ready-to-use layouts and palettes. Imported templates can carry fonts, colors, logos, legal text, media, and representative PowerPoint layouts into the generated presentation.

## Import a Personal PowerPoint Template

1. Open **Slides**.
2. Open **Templates**.
3. Select the import action.
4. Choose a `.pptx` file.
5. Wait for processing to complete, then locate the new **Personal** template in the catalog.

An imported file must:

- Be a valid, nonempty `.pptx` file.
- Be no larger than 75 MiB.
- Represent one template identity.

Astonish extracts usable theme colors, fonts, logos and fixed brand elements, media, and representative layouts. Thumbnail generation is best effort, so a successful import can occasionally appear without every static preview.

::: warning Import Fidelity
Astonish treats the PowerPoint file as a source of brand identity and presentation patterns. It can reuse official cover and closing treatments while composing flexible body slides. The result is not guaranteed to reproduce every source slide or PowerPoint feature exactly.
:::

Embedded fonts are recovered when possible. Unsupported font formats and individual embedded fonts larger than 4 MiB may be skipped; a compatible fallback font is used instead.

## Customize Templates

### Duplicate

Any visible template can be duplicated into a personal copy. Use this before customizing a built-in or inherited shared template.

### Recolor

Personal templates expose color controls for the primary surface, text, and accent colors. Shared and built-in templates are read-only in the Slides catalog.

### Delete

Delete personal templates from **Slides → Templates**. Deleting a template does not delete decks that were previously created from it.

## Manage Shared Templates

Administrators import and delete shared templates in **Settings → Slides Templates** at the scope they manage:

- **Team** administrators manage templates for the current team.
- **Organization** administrators and owners manage templates inherited by teams in that organization.
- **Platform** administrators manage templates available across the platform.

Members can use inherited templates from the Slides catalog but cannot recolor or delete the shared source. To customize one privately, duplicate it to the personal scope. To remove a shared template, an authorized administrator must delete it from its owning Settings page.

Personal template import remains in **Slides → Templates**, not Personal Settings.

## Current Limits

- Built-in templates cannot be overwritten or deleted.
- Shared templates cannot be recolored from the personal catalog.
- Imported template placeholders cannot be directly redesigned in the browser.
- Some PowerPoint features, fonts, or media formats may be approximated or omitted.
- Body slides may use flexible layouts that apply the imported brand rather than copying a source layout exactly.

## Next Steps

- [Create a presentation with a template](./creating-presentations.md)
- [Edit and save a deck](./editing-and-saving.md)
- [Understand export fidelity](./presenting-and-exporting.md)
