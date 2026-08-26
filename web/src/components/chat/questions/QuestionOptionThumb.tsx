import { useState } from 'react'

import { templateThumbnailUrl } from '@/api/slides'

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
    // assetRef is `thumb/<kind>`; the endpoint's <kind> path param is the
    // archetype kind, so strip a leading `thumb/` prefix.
    const kind = thumbnail.assetRef!.replace(/^thumb\//, '')
    return (
      <img
        src={templateThumbnailUrl(thumbnail.template!, kind)}
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
