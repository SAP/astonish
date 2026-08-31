# Creating Presentations

Create presentations through Chat so Astonish can gather requirements, use your source material, generate the complete deck, and refine it with you.

## Start a Presentation

Use either entry point:

- Open **Slides** and select **Create Slide**. Studio takes you to Chat with Slides ready to use.
- Open **Chat** and ask for slides, a slide deck, a presentation, or a PowerPoint presentation.

For example:

```text
Create a 12-slide presentation for engineering leaders explaining our
migration proposal. Use the attached architecture brief, emphasize risk
reduction, and end with a decision request.
```

## Guided Intake

Astonish gathers the information needed to build a coherent deck. Depending on what your request already contains, it may ask about:

1. The audience and purpose.
2. The approximate length, commonly a short, medium, or detailed deck.
3. Whether you want to choose a template or let Astonish choose.
4. Template-specific options such as a cover, title treatment, palette, logo or image, and closing slide.

Information already stated in your request does not need to be supplied again. Include the audience, goal, desired length, sources, and preferred template up front for the shortest intake.

<!-- IMAGE: Chat showing the Slides template picker and a generating deck preview -->

## Use Source Material

### Start from a topic or brief

```text
Build an executive presentation about reducing customer onboarding time.
Use 10 slides, a concise tone, and include recommendations and next steps.
```

### Ground the deck in documents

Attach the relevant files to the current Chat message and identify how they should be used:

```text
Turn the attached quarterly report and customer interview summary into a
board presentation. Keep every numerical claim grounded in those files.
```

### Research before writing

```text
Research current enterprise AI adoption trends, then create a presentation
for our strategy workshop. Cite the important findings in the slide content.
```

The research tools and sources available depend on the agent configuration and network access in your deployment.

### Use connected systems

If the agent has access to connected tools, name the system and the data you want included:

```text
Create a sales review from the current-quarter opportunities in our CRM.
Show pipeline by stage, major risks, and the five deals that need attention.
```

The agent can only use systems, tools, and credentials available to the current user and team.

## Review Generation

The deck preview appears in Chat as soon as creation starts. It displays **Generating slides…** until slide content arrives. Use the preview controls to move through the available slides while the agent continues working.

Astonish validates and reviews the generated deck before treating it as complete. You can then refine it conversationally:

```text
Make slide 3 more visual and move the implementation detail to an appendix.
```

```text
Rewrite the titles as conclusions, reduce repetition, and make the final
recommendation more specific.
```

::: tip
Ask for changes by slide number and desired outcome. Explain what should remain unchanged when preserving layout or wording matters.
:::

## Add Images

Ask Chat to add an image to an existing slide. Slides can use:

- An image attached to the current Chat message.
- An image already stored with the deck or template.
- A public HTTP or HTTPS image URL.

For example:

```text
Add the attached product screenshot to slide 6. Preserve the current layout
and place it in the right-hand visual area.
```

```text
Use https://example.com/diagram.png on slide 4 with contain fitting. Do not
change the slide's text or layout.
```

Remote images can be up to 20 MiB. Private, loopback, and internal network URLs are rejected, as are unsafe image or SVG contents. Slides uses supplied or imported imagery; it does not generate new images itself.

## Direct Editing and Structural Changes

The canvas can move, resize, edit, or delete existing supported objects. Ask Chat to add new images, create new sections, change a slide's structure, or make broader narrative revisions.

See [Editing & Saving](./editing-and-saving.md) for direct canvas controls.

## Generation Tips

- State one clear presentation objective and audience.
- Attach or identify authoritative source material.
- Request an approximate length rather than packing too many ideas into each slide.
- Specify brand/template requirements before generation.
- Ask for takeaway-style titles when the conclusion of each slide should be immediately clear.
- Review factual claims and sensitive information before sharing or exporting.

## Next Steps

- [Choose or import a template](./templates.md)
- [Edit and save the generated deck](./editing-and-saving.md)
- [Present or export the result](./presenting-and-exporting.md)
