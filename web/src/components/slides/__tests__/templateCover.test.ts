import { describe, expect, it } from 'vitest'

import { templateCoverThumbnail } from '../templateCover'
import type { SlidesTemplate } from '@/api/slides'

describe('templateCoverThumbnail', () => {
  it('prefers a baked PNG cover from the list DTO', () => {
    const tpl: SlidesTemplate = {
      name: 'gco',
      cover: { kind: 'title', thumbnailRef: 'thumb/title' },
    }
    expect(templateCoverThumbnail(tpl)).toEqual({
      kind: 'image',
      template: 'gco',
      assetRef: 'thumb/title',
    })
  })

  it('live-renders cover markup when no thumbnail ref is set', () => {
    const tpl: SlidesTemplate = {
      name: 'midnight',
      tokens: { surface: '#0B1220', ink: '#E2E8F0', accent: '#F59E0B' },
      cover: { kind: 'title', markup: '<ast-slide id="title"></ast-slide>' },
    }
    expect(templateCoverThumbnail(tpl)).toEqual({
      kind: 'slides-archetype',
      markup: '<ast-slide id="title"></ast-slide>',
      theme: tpl.tokens,
      template: 'midnight',
    })
  })

  it('falls back to the first title* archetype when cover is omitted', () => {
    const tpl: SlidesTemplate = {
      name: 'brand',
      archetypes: [
        { kind: 'section', thumbnailRef: 'thumb/section' },
        { kind: 'title-2', thumbnailRef: 'thumb/title-2' },
        { kind: 'title', thumbnailRef: 'thumb/title' },
      ],
    }
    expect(templateCoverThumbnail(tpl)).toEqual({
      kind: 'image',
      template: 'brand',
      assetRef: 'thumb/title-2',
    })
  })

  it('returns null when nothing can be previewed', () => {
    expect(templateCoverThumbnail({ name: 'empty' })).toBeNull()
  })
})
