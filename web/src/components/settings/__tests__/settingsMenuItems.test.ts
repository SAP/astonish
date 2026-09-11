import { describe, expect, it } from 'vitest'

import { PERSONAL_ITEMS, PLATFORM_ITEMS } from '../settingsMenuItems'

describe('OAuth settings menu placement', () => {
  it('shows OAuth client management in Personal settings, never Platform administration', () => {
    expect(PERSONAL_ITEMS).toContainEqual(expect.objectContaining({
      id: 'oauth',
      label: 'OAuth',
    }))
    expect(PLATFORM_ITEMS).not.toContainEqual(expect.objectContaining({
      id: 'platform-oauth',
    }))
  })
})
