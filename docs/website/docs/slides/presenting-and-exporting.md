# Presenting and Exporting Slides

Run a presentation directly from Studio or export it for PowerPoint, print-ready distribution, and offline browser viewing.

<!-- IMAGE: Slides toolbar showing Present, Full screen, PPTX, PDF, and HTML controls -->

## Present in the Browser

The deck toolbar provides two viewing modes:

- **Present** opens a dedicated presenter window and attempts to enter browser full screen.
- **Full screen** opens a view-only presentation overlay inside Studio.

Use these controls while presenting:

| Input | Action |
|-------|--------|
| Right Arrow, Page Down, or Space | Advance a fragment or slide |
| Left Arrow or Page Up | Return to the previous fragment or slide |
| Home | Go to the first slide |
| End | Go to the last slide |
| Click or tap navigation zones | Move backward or forward |
| Touch gesture | Navigate on a touch device |
| Escape | Exit presenter mode or browser full screen |

Slides with progressive reveals show their fragments in sequence. Exported static formats use the final revealed state.

## Export a Deck

Select **PPTX**, **PDF**, or **HTML** in the deck toolbar. The browser downloads a file named from the deck identifier. While one export is running, the other export controls remain disabled.

| Format | Best for | Behavior |
|--------|----------|----------|
| **PPTX** | Continued editing in PowerPoint | Supported text, shapes, images, tables, charts, groups, and code remain native where possible. Some icons, SVG content, or unsupported web elements may be exported as images. |
| **PDF** | Distribution and visual fidelity | Produces one landscape page per slide and aims to match the browser rendering. |
| **HTML** | Interactive or offline browser viewing | Produces a self-contained presentation with its runtime and assets embedded. |

### PPTX

Choose PPTX when recipients need to edit the presentation. Astonish maps supported slide elements to PowerPoint objects rather than flattening every slide into one screenshot.

Some browser effects do not have an exact PowerPoint equivalent. Complex content can become a picture, and progressive reveal sequencing is omitted in favor of the final state. Missing or unsupported imported media may also be skipped rather than blocking the entire export.

### PDF

Choose PDF when the appearance is more important than object editability. Astonish renders the 16:9 presentation and prints each slide as a separate landscape page.

PDF generation requires browser-rendering support on the Astonish server. In a platform deployment, this can depend on the configured sandbox and its browser availability.

### HTML

Choose HTML for a portable browser presentation. The default export includes slide content, assets, styles, and navigation in one self-contained file and does not require the Astonish server when opened later.

## Fidelity Expectations

Browser, PDF, and PowerPoint use different rendering engines. Differences can occur in:

- Font availability and substitution.
- Line wrapping and text measurement.
- Gradients, clipping, masks, and filters.
- Transforms and advanced web effects.
- Unsupported or unusual imported media.

Review the exported file before distribution. Use PDF when matching the Studio appearance is the priority, and PPTX when downstream editing is the priority.

## Troubleshooting

### Present does not open or enter full screen

Allow pop-ups and full-screen access for the Studio site, then select **Present** again. Use **Full screen** when browser policy prevents a separate presenter window.

### A PPTX element differs from Studio

Open the exported file with the expected fonts installed. If the element still differs, simplify the slide in Chat or distribute the PDF version for greater visual consistency.

### PDF export reports a browser or sandbox error

Retry once. In platform deployments, contact an administrator to verify that the configured execution sandbox can start its browser renderer. Platform PDF rendering does not silently fall back to an unrelated browser on the host.

### Imported fonts look different

Use a broadly available font or re-import a template containing a supported embedded font. Unsupported and oversized embedded fonts use fallback fonts.

### A template will not import

Verify that the file is a valid, nonempty `.pptx` no larger than 75 MiB. See [Slides Templates](./templates.md) for import behavior.

For other deployment and browser issues, see [Troubleshooting](../reference/troubleshooting.md).

## Next Steps

- [Edit and save a deck](./editing-and-saving.md)
- [Share a deck with your team](./sharing.md)
- [Return to the Slides overview](./index.md)
