/**
 * Helpers for MCP env / config values that reference the credential store.
 * Format matches pkg/credentials/substitute.go: {{CREDENTIAL:name:field}}
 */

export const CREDENTIAL_PLACEHOLDER_RE = /\{\{CREDENTIAL:([^:}]+):([^}]+)\}\}/

export type CredentialRef = {
  name: string
  field: string
}

/** Build a store placeholder token. */
export function formatCredentialPlaceholder(name: string, field: string): string {
  return `{{CREDENTIAL:${name}:${field}}}`
}

/** Parse a full-string placeholder; returns null if the value is not exactly one placeholder. */
export function parseCredentialPlaceholder(value: string | undefined | null): CredentialRef | null {
  if (!value) return null
  const trimmed = value.trim()
  const m = trimmed.match(/^\{\{CREDENTIAL:([^:}]+):([^}]+)\}\}$/)
  if (!m) return null
  return { name: m[1], field: m[2] }
}

/** True if the string contains any credential placeholder. */
export function containsCredentialPlaceholder(value: string | undefined | null): boolean {
  if (!value) return false
  return CREDENTIAL_PLACEHOLDER_RE.test(value)
}

/** Default secret field for a credential type (UI auto-pick). */
export function defaultFieldForType(type: string): string {
  switch (type) {
    case 'api_key':
      return 'value'
    case 'bearer':
      return 'token'
    case 'basic':
    case 'password':
      return 'password'
    case 'oauth_client_credentials':
      return 'client_secret'
    case 'oauth_authorization_code':
      return 'access_token'
    case 'openstack_keystone':
      return 'token'
    case 'raw_content':
      return 'content'
    default:
      return 'value'
  }
}

/** Fields offered in the picker for a credential type. */
export function fieldsForType(type: string): string[] {
  switch (type) {
    case 'api_key':
      return ['value', 'header']
    case 'bearer':
      return ['token']
    case 'basic':
      return ['username', 'password']
    case 'password':
      return ['username', 'password']
    case 'oauth_client_credentials':
      return ['client_id', 'client_secret', 'access_token']
    case 'oauth_authorization_code':
      return ['client_id', 'client_secret', 'access_token', 'refresh_token']
    case 'openstack_keystone':
      return ['token', 'password', 'username', 'application_credential_id', 'application_credential_secret']
    case 'raw_content':
      return ['content', 'content_type']
    default:
      return ['value', 'token', 'password']
  }
}

/** Guess api_key vs bearer from an env var name. */
export function guessCredentialTypeFromEnvKey(envKey: string): 'api_key' | 'bearer' {
  if (/TOKEN|BEARER|AUTH/i.test(envKey)) return 'bearer'
  return 'api_key'
}

export function isSensitiveEnvKey(key: string): boolean {
  return /TOKEN|KEY|SECRET|PASSWORD|PASSWD|PWD|AUTH/i.test(key)
}
