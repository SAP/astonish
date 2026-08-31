# Editing and Saving Slides

A generated presentation begins as a session draft in Chat. You can refine it with AI, edit supported objects directly, and explicitly save it as a permanent deck in the Slides library.

## Session Drafts and Saved Decks

A **session draft** belongs to the current Chat session. It remains available for conversational refinement and shows a **Save** action.

A **saved deck** is a named, permanent copy in the Slides library. Saving does not remove or replace the source session draft, so you can continue experimenting in Chat after saving.

## Edit Existing Objects

Select supported objects directly on the main deck canvas.

<!-- IMAGE: Slides canvas with a selected image, alignment guides, and Apply and Discard controls -->

| Action | How to use it |
|--------|---------------|
| Move an object | Select and drag it. Alignment guides appear near sibling edges and centers. |
| Resize an image | Drag a corner handle. The image keeps its aspect ratio. |
| Edit plain text | Double-click the text, click it again while selected, or press Enter. |
| Add a line break | Press Shift+Enter while editing text. |
| Cancel text editing | Press Escape. |
| Delete an object | Select it, then press Delete/Backspace or use the Delete control. |
| Revert pending edits | Select **Discard**. |
| Persist pending edits | Select **Apply**. |

Decorative backgrounds and objects without editing support are not selectable. Direct editing modifies existing objects; ask Chat to insert new images or make structural changes.

While direct edits are pending, **Discard** and **Apply** replace **Save**. Apply or discard the draft edits before saving the deck.

## Save a Deck

1. Select **Save** in the deck toolbar.
2. Enter a nonblank deck name. The current title is suggested.
3. Confirm the save.
4. Open **Slides** to find the permanent deck.

Saving under a new name creates a new permanent deck at version 1. Saving again over an existing deck with the same permanent identity creates a new version and preserves the previous state in version history.

::: tip
Use a new name when you want a separate deck. Use the existing name when the new result should become the latest version of that deck.
:::

## Use the Slides Library

The library displays saved decks with their title, scope, last update time, and a thumbnail of the first slide. In platform mode, decks are grouped into **Personal** and **Team** sections.

From a library card, you can:

- Open the deck and move through its slides.
- Select **Enhance with AI**.
- Publish or fork it when the action is available.
- Delete it from its current scope.

If a static thumbnail is unavailable, the card shows a presentation placeholder. Open the deck to view the live-rendered presentation.

## Enhance with AI

**Enhance with AI** opens Chat and creates a session working copy of the saved deck. Astonish asks what you want to change, then updates that draft rather than immediately modifying the permanent deck.

When the enhancement is ready, select **Save** and choose whether to:

- Save over the existing deck to create a new version.
- Use a different name to create a separate permanent deck.

This explicit save step lets you review AI changes before they affect the library copy.

## Version History

When a saved deck is overwritten, Astonish snapshots the previous state. The newest five prior snapshots are retained for personal and team decks.

To restore a version:

1. Open the saved deck from the Slides library.
2. Open **version history**.
3. Review the version number, title, and date.
4. Select **Restore** for the desired snapshot.

Restoring first archives the current deck, then makes the selected content the new current state. The deck receives a new version number; its number does not reset to the restored snapshot's original number.

Version history is created by overwrite-saving. A deck that has never been overwritten has no previous versions to restore.

## Next Steps

- [Share personal and team decks](./sharing.md)
- [Present and export a deck](./presenting-and-exporting.md)
- [Return to the Slides overview](./index.md)
