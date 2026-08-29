import type { SlidesTemplate, SlidesTemplateArchetype, SlidesTemplateCover } from '@/api/slides'
import type { ChatQuestionOption } from '@/components/chat/chatTypes'

export type TemplateCoverThumbnail = NonNullable<ChatQuestionOption['thumbnail']>

function templateBaseKind(kind: string): string {
  const i = kind.lastIndexOf('-')
  if (i <= 0) return kind
  return /^\d+$/.test(kind.slice(i + 1)) ? kind.slice(0, i) : kind
}

function pickCoverFromArchetypes(tpl: SlidesTemplate): SlidesTemplateCover | null {
  const variants: SlidesTemplateArchetype[] =
    tpl.archetypes && tpl.archetypes.length > 0
      ? tpl.archetypes
      : (tpl.archetypeKinds || []).map((kind) => ({ kind }))
  const title = variants.find((variant) => templateBaseKind(variant.kind) === 'title')
  const cover = title ?? variants[0]
  if (!cover) return null
  return {
    kind: cover.kind,
    thumbnailRef: cover.thumbnailRef,
    markup: cover.markup,
  }
}

/** Thumbnail payload for the Templates library card — same shape the chat
 * template picker feeds QuestionOptionThumb. */
export function templateCoverThumbnail(tpl: SlidesTemplate): TemplateCoverThumbnail | null {
  const cover = tpl.cover ?? pickCoverFromArchetypes(tpl)
  if (!cover) return null
  if (cover.thumbnailRef) {
    return { kind: 'image', template: tpl.name, assetRef: cover.thumbnailRef }
  }
  if (cover.markup) {
    return {
      kind: 'slides-archetype',
      markup: cover.markup,
      theme: tpl.tokens,
      template: tpl.name,
    }
  }
  return null
}
