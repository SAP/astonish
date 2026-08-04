/**
 * Shared credential form model + payload builder used by Credentials settings
 * and MCP env bind "create credential" flows.
 */

export interface CredForm {
  name: string
  type: string
  header: string
  value: string
  token: string
  username: string
  password: string
  auth_url: string
  client_id: string
  client_secret: string
  scope: string
  token_url: string
  access_token: string
  refresh_token: string
  keystone_method: string
  user_domain: string
  project_id: string
  project_name: string
  project_domain: string
  application_credential_id: string
  application_credential_secret: string
  content: string
  content_type: string
}

export const emptyCredForm = (): CredForm => ({
  name: '',
  type: 'api_key',
  header: 'Authorization',
  value: '',
  token: '',
  username: '',
  password: '',
  auth_url: '',
  client_id: '',
  client_secret: '',
  scope: '',
  token_url: '',
  access_token: '',
  refresh_token: '',
  keystone_method: 'application_credential',
  user_domain: 'Default',
  project_id: '',
  project_name: '',
  project_domain: 'Default',
  application_credential_id: '',
  application_credential_secret: '',
  content: '',
  content_type: 'text/plain',
})

/** Build the API credential body from the form (same mapping as Credentials settings). */
export function buildCredentialPayload(form: CredForm): Record<string, unknown> {
  const cred: Record<string, unknown> = { type: form.type }
  switch (form.type) {
    case 'api_key':
      cred.header = form.header
      cred.value = form.value
      break
    case 'bearer':
      cred.token = form.token
      break
    case 'basic':
    case 'password':
      cred.username = form.username
      cred.password = form.password
      break
    case 'oauth_client_credentials':
      cred.auth_url = form.auth_url
      cred.client_id = form.client_id
      cred.client_secret = form.client_secret
      cred.scope = form.scope
      break
    case 'oauth_authorization_code':
      cred.token_url = form.token_url
      cred.client_id = form.client_id
      cred.client_secret = form.client_secret
      cred.access_token = form.access_token
      cred.refresh_token = form.refresh_token
      cred.scope = form.scope
      break
    case 'raw_content':
      cred.content = form.content
      cred.content_type = form.content_type
      break
    case 'openstack_keystone':
      cred.auth_url = form.auth_url
      if (form.keystone_method === 'application_credential') {
        cred.application_credential_id = form.application_credential_id
        cred.application_credential_secret = form.application_credential_secret
      } else {
        cred.username = form.username
        cred.password = form.password
        cred.user_domain = form.user_domain
        cred.project_id = form.project_id
        cred.project_name = form.project_name
        cred.project_domain = form.project_domain
      }
      break
  }
  return cred
}

export function validateCredForm(form: CredForm): string | null {
  if (!form.name.trim()) return 'Name is required'
  switch (form.type) {
    case 'api_key':
      if (!form.value) return 'Key value is required'
      break
    case 'bearer':
      if (!form.token) return 'Token is required'
      break
    case 'basic':
    case 'password':
      if (!form.password) return 'Password is required'
      break
    case 'oauth_client_credentials':
      if (!form.auth_url || !form.client_id || !form.client_secret) {
        return 'Auth URL, client ID, and client secret are required'
      }
      break
    case 'oauth_authorization_code':
      if (!form.token_url || !form.client_id) {
        return 'Token URL and client ID are required'
      }
      break
    case 'raw_content':
      if (!form.content) return 'Content is required'
      break
    case 'openstack_keystone':
      if (!form.auth_url) return 'Auth URL is required'
      if (form.keystone_method === 'application_credential') {
        if (!form.application_credential_id || !form.application_credential_secret) {
          return 'Application credential ID and secret are required'
        }
      } else if (!form.username || !form.password) {
        return 'Username and password are required'
      }
      break
  }
  return null
}
