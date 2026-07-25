
export interface UserDefaultModel {
  defaultProvider: string;
  defaultModel: string;
}

export async function fetchUserDefaultModel(): Promise<UserDefaultModel> {
  const response = await fetch('/api/user-settings/default-model');
  if (!response.ok) {
    const errorData = await response.json().catch(() => ({ message: 'Failed to fetch user default model settings' }));
    throw new Error(errorData.message || 'Failed to fetch user default model settings');
  }
  return response.json();
}

export async function patchUserDefaultModel(provider: string, model: string): Promise<void> {
  const response = await fetch('/api/user-settings/default-model', {
    method: 'PATCH',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ defaultProvider: provider, defaultModel: model }),
  });

  if (!response.ok) {
    try {
      const errorData = await response.json();
      throw new Error(errorData.message || 'Failed to update default model');
    } catch (e) {
      throw new Error('An unknown error occurred while updating the default model', { cause: e });
    }
  }
}

// --- Brand theme (user preference + cascade) ---

export interface UserBrandThemeResponse {
  /** Stored preference; empty means inherit platform default. */
  brand_theme: string
  /** Resolved pack after cascade. */
  effective: string
  source?: 'user' | 'platform' | 'default'
  platform_default?: string
}

export interface PublicBrandThemeResponse {
  brand_theme: string
  source: 'platform' | 'default'
}

/** Auth-exempt: platform default (or product default) for login / first paint. */
export async function fetchPublicBrandTheme(): Promise<PublicBrandThemeResponse> {
  const response = await fetch('/api/brand-theme')
  if (!response.ok) {
    throw new Error('Failed to fetch brand theme')
  }
  return response.json()
}

export async function fetchUserBrandTheme(): Promise<UserBrandThemeResponse> {
  const response = await fetch('/api/user-settings/brand-theme', { credentials: 'include' })
  if (!response.ok) {
    const errorData = await response.json().catch(() => ({ message: 'Failed to fetch brand theme' }))
    throw new Error(errorData.message || 'Failed to fetch brand theme')
  }
  return response.json()
}

/** Empty brandTheme clears preference (inherit platform). */
export async function patchUserBrandTheme(brandTheme: string): Promise<UserBrandThemeResponse> {
  const response = await fetch('/api/user-settings/brand-theme', {
    method: 'PATCH',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ brand_theme: brandTheme }),
  })
  if (!response.ok) {
    const errorData = await response.json().catch(() => ({ message: 'Failed to update brand theme' }))
    throw new Error(errorData.message || errorData.error || 'Failed to update brand theme')
  }
  return response.json()
}
