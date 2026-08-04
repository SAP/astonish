import { describe, expect, it } from 'vitest'
import {
  containsCredentialPlaceholder,
  defaultFieldForType,
  formatCredentialPlaceholder,
  guessCredentialTypeFromEnvKey,
  isSensitiveEnvKey,
  omitEmptySensitiveEnv,
  parseCredentialPlaceholder,
} from '../credentialPlaceholders'

describe('credentialPlaceholders', () => {
  it('formats and parses placeholders', () => {
    const token = formatCredentialPlaceholder('github', 'token')
    expect(token).toBe('{{CREDENTIAL:github:token}}')
    expect(parseCredentialPlaceholder(token)).toEqual({ name: 'github', field: 'token' })
  })

  it('rejects plain values and partial placeholders', () => {
    expect(parseCredentialPlaceholder('ghp_xxx')).toBeNull()
    expect(parseCredentialPlaceholder('prefix {{CREDENTIAL:a:b}}')).toBeNull()
    expect(containsCredentialPlaceholder('prefix {{CREDENTIAL:a:b}}')).toBe(true)
  })

  it('defaults fields and types from heuristics', () => {
    expect(defaultFieldForType('bearer')).toBe('token')
    expect(defaultFieldForType('api_key')).toBe('value')
    expect(guessCredentialTypeFromEnvKey('GITHUB_TOKEN')).toBe('bearer')
    expect(guessCredentialTypeFromEnvKey('API_KEY')).toBe('api_key')
    expect(isSensitiveEnvKey('GITHUB_TOKEN')).toBe(true)
    expect(isSensitiveEnvKey('DEBUG')).toBe(false)
  })

  it('omits blank sensitive env values', () => {
    expect(omitEmptySensitiveEnv({ GITHUB_TOKEN: '', API_SECRET: '  ', LOG_LEVEL: '', API_KEY: '{{CREDENTIAL:api:value}}' })).toEqual({
      LOG_LEVEL: '',
      API_KEY: '{{CREDENTIAL:api:value}}',
    })
  })
})
