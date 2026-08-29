import { useState } from 'react'

import { templateMediaUrl, templateThumbnailUrl, type DocsScope } from '@/api/slides'

import SlidesArchetypeThumb from './SlidesArchetypeThumb'
import type { ChatQuestionOption } from '../chatTypes'

export interface QuestionOptionThumbProps {
  thumbnail: NonNullable<ChatQuestionOption['thumbnail']>
  /** Accessible label for the rendered image (usually the option label). */
  label: string
}

/**
 * QuestionOptionThumb renders a questionnaire option's thumbnail. For
 * `kind: 'image'` (pre-baked template archetype PNG) it renders an `<img>`
 * pointing at the backend thumbnail endpoint, falling back to the live
 * SlidesArchetypeThumb render on error when markup is available. For
 * `kind: 'slides-archetype'` (built-in templates) it live-renders the markup.
 */
export default function QuestionOptionThumb({ thumbnail, label }: QuestionOptionThumbProps) {
  const [imageFailed, setImageFailed] = useState(false)

  const canRenderImage =
    thumbnail.kind === 'image' &&
    !!thumbnail.template &&
    !!thumbnail.assetRef &&
    !imageFailed

  if (canRenderImage) {
    const assetRef = thumbnail.assetRef!
    const scope = thumbnail.templateScope && thumbnail.templateScope !== 'builtin'
      ? thumbnail.templateScope as DocsScope
      : undefined
    const src = assetRef.startsWith('sha256-')
      ? templateMediaUrl(thumbnail.template!, assetRef, scope)
      : templateThumbnailUrl(thumbnail.template!, assetRef.replace(/^thumb\//, ''), assetRef, scope)
    return (
      <img
        src={src}
        className="h-full w-full object-cover"
        alt={label}
        loading="lazy"
        onError={() => setImageFailed(true)}
      />
    )
  }

  // Live-rendered fallback: either the built-in `slides-archetype` path, or an
  // `image` thumbnail whose PNG failed to load but still carries markup.
  if (thumbnail.markup) {
    return (
      <SlidesArchetypeThumb
        markup={thumbnail.markup}
        theme={thumbnail.theme}
        template={thumbnail.template}
      />
    )
  }

  return null
}
