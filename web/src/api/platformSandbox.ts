/**
 * API client for Platform Base Sandbox Configuration.
 * Used by the SandboxBaseTab component (superadmins only).
 */

const BASE = '/api/platform/admin/sandbox/base'

async function adminFetch(input: string, init?: Parameters<typeof fetch>[1]): Promise<Response> {
  const headers = new Headers(init?.headers)
  if (!headers.has('X-Requested-With')) {
    headers.set('X-Requested-With', 'XMLHttpRequest')
  }
  return fetch(input, { credentials: 'include', ...init, headers })
}

// --- Types ---

export interface BaseConfig {
  core: boolean
  optional_tools: string[]
  browser: {
    engine: 'none' | 'default' | 'cloakbrowser'
    fingerprint_platform?: string
    fingerprint_seed?: string
  }
  extra_steps?: string[]
  architecture?: string
}

export interface BaseConfigSummary {
  layer_id: string
  size_bytes: number
  config: BaseConfig | null
  configured_by: string
  configured_at: string | null
  updated_at: string
  backend?: string
  overlay_ready?: boolean
  legacy_config?: boolean
  message?: string
}

/** True when Docker OverlayFS actually has a customized @base layer. */
export function baseSandboxIsLive(summary: BaseConfigSummary | null | undefined): boolean {
  if (!summary) return false
  if (summary.legacy_config) return false
  if (summary.overlay_ready === false) return false
  const layer = (summary.layer_id || '').trim()
  if (!layer || layer === '@base' || layer === 'none') return false
  return true
}

export interface BaseConfigStatus {
  in_progress: boolean
}

export interface OptionalTool {
  id: string
  name: string
  description: string
  url: string
  recommended: boolean
}

export interface ConfigureBuildResult {
  layer_id: string
  size_bytes: number
}

export interface UnsupportedBackendInfo {
  unsupported_backend: true
  backend: string
  message: string
}

export interface OpenShellBackendInfo {
  backend: 'openshell'
  build_supported: false
  sandbox_image: string
  default_image: string
  message: string
}

// --- API functions ---

export async function getBaseConfig(): Promise<BaseConfigSummary | OpenShellBackendInfo | UnsupportedBackendInfo> {
  const res = await adminFetch(BASE)
  if (!res.ok) {
    const body = await res.json().catch(() => ({})) as Record<string, unknown>
    throw new Error((body.error as string) || `Failed to fetch base config: ${res.statusText}`)
  }
  const data = await res.json()
  if (data.unsupported_backend) {
    return data as UnsupportedBackendInfo
  }
  if (data.backend === 'openshell') {
    return data as OpenShellBackendInfo
  }
  return data as BaseConfigSummary
}

export async function setBaseImage(image: string): Promise<void> {
  const res = await adminFetch(`${BASE}/image`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ image }),
  })
  if (!res.ok) {
    const body = await res.json().catch(() => ({})) as Record<string, unknown>
    throw new Error((body.error as string) || `Failed to set image: ${res.statusText}`)
  }
}

export async function getBaseStatus(): Promise<BaseConfigStatus> {
  const res = await adminFetch(`${BASE}/status`)
  if (!res.ok) {
    throw new Error(`Failed to fetch build status: ${res.statusText}`)
  }
  return res.json()
}

export async function listOptionalTools(): Promise<OptionalTool[]> {
  const res = await adminFetch(`${BASE}/tools`)
  if (!res.ok) {
    throw new Error(`Failed to fetch optional tools: ${res.statusText}`)
  }
  return res.json()
}

export interface ConfigureBaseCallbacks {
  config: BaseConfig
  onProgress: (msg: string) => void
  onDone: (result: ConfigureBuildResult) => void
  onError: (err: string) => void
}

export async function cancelBaseConfigure(): Promise<void> {
  await adminFetch(`${BASE}/configure/cancel`, { method: 'POST' })
}

export async function waitForBaseBuild(opts: {
  onProgress: (msg: string) => void
  onDone: (result: ConfigureBuildResult) => void
  onError: (err: string) => void
  signal?: AbortSignal
}): Promise<void> {
  const { onProgress, onDone, onError, signal } = opts
  onProgress('Studio lost the live log; checking whether the server is still building...')
  const started = Date.now()
  while (!signal?.aborted) {
    let status: BaseConfigStatus
    try {
      status = await getBaseStatus()
    } catch (err) {
      onError((err as Error).message || 'Failed to check build status')
      return
    }
    if (status.in_progress) {
      const elapsed = Math.round((Date.now() - started) / 1000)
      onProgress(`Server is still building (${elapsed}s since the log dropped). Leave this page open.`)
      await new Promise((r) => setTimeout(r, 3000))
      continue
    }
    const summary = await getBaseConfig()
    if ('unsupported_backend' in summary || (summary as OpenShellBackendInfo).build_supported === false) {
      onError('Build stream ended without a result.')
      return
    }
    const live = summary as BaseConfigSummary
    if (baseSandboxIsLive(live)) {
      onDone({ layer_id: live.layer_id, size_bytes: live.size_bytes })
      return
    }
    onError('Build stream ended and no new base layer was saved.')
    return
  }
}

export function configureBase({ config, onProgress, onDone, onError }: ConfigureBaseCallbacks): { abort: () => void } {
  const controller = new AbortController()

  adminFetch(`${BASE}/configure`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(config),
    signal: controller.signal,
  })
    .then(async (res) => {
      if (!res.ok) {
        const text = await res.text()
        try {
          const body = JSON.parse(text)
          onError(body.error || text || res.statusText)
        } catch {
          onError(text || res.statusText)
        }
        return
      }

      const reader = res.body!.getReader()
      const decoder = new TextDecoder()
      let buffer = ''
      let terminal = false

      while (true) {
        const { done, value } = await reader.read()
        if (done) break

        buffer += decoder.decode(value, { stream: true })
        const lines = buffer.split('\n')
        buffer = lines.pop()!

        let currentEvent = ''
        for (const line of lines) {
          if (line.startsWith('event: ')) {
            currentEvent = line.slice(7).trim()
          } else if (line.startsWith('data: ')) {
            const dataStr = line.slice(6)
            try {
              const data = JSON.parse(dataStr)
              if (currentEvent === 'progress') {
                onProgress(data.message || '')
              } else if (currentEvent === 'done') {
                terminal = true
                onDone({ layer_id: data.layer_id, size_bytes: data.size_bytes })
              } else if (currentEvent === 'error') {
                terminal = true
                onError(data.error || 'Unknown error')
              }
            } catch {
              // ignore parse errors for incomplete data
            }
            currentEvent = ''
          }
        }
      }
      if (!terminal && !controller.signal.aborted) {
        await waitForBaseBuild({ onProgress, onDone, onError, signal: controller.signal })
      }
    })
    .catch((err: Error) => {
      if (err.name !== 'AbortError') {
        onError(err.message || 'Connection failed')
      }
    })

  return { abort: () => controller.abort() }
}

// --- Image Build API (OpenShell / Kaniko) ---

export interface ImageBuildCallbacks {
  dockerfileBody: string
  onProgress: (msg: string) => void
  onDone: (result: { image: string }) => void
  onError: (err: string) => void
}

export function buildBaseImage({ dockerfileBody, onProgress, onDone, onError }: ImageBuildCallbacks): { abort: () => void } {
  const controller = new AbortController()

  adminFetch(`${BASE}/build`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ dockerfile_body: dockerfileBody }),
    signal: controller.signal,
  })
    .then(async (res) => {
      if (!res.ok) {
        const text = await res.text()
        try {
          const body = JSON.parse(text)
          onError(body.error || text || res.statusText)
        } catch {
          onError(text || res.statusText)
        }
        return
      }

      const reader = res.body!.getReader()
      const decoder = new TextDecoder()
      let buffer = ''

      while (true) {
        const { done, value } = await reader.read()
        if (done) break

        buffer += decoder.decode(value, { stream: true })
        const lines = buffer.split('\n')
        buffer = lines.pop()!

        let currentEvent = ''
        for (const line of lines) {
          if (line.startsWith('event: ')) {
            currentEvent = line.slice(7).trim()
          } else if (line.startsWith('data: ')) {
            const dataStr = line.slice(6)
            try {
              const data = JSON.parse(dataStr)
              if (currentEvent === 'progress') {
                onProgress(data.message || '')
              } else if (currentEvent === 'done') {
                onDone({ image: data.image })
              } else if (currentEvent === 'error') {
                onError(data.error || 'Unknown error')
              }
            } catch {
              // ignore parse errors for incomplete data
            }
            currentEvent = ''
          }
        }
      }
    })
    .catch((err: Error) => {
      if (err.name !== 'AbortError') {
        onError(err.message || 'Connection failed')
      }
    })

  return { abort: () => controller.abort() }
}

export interface ImageBuildStatus {
  in_progress: boolean
  status: string
  image: string
  error: string
  job_name: string
  started_at: string
}

export async function getBaseImageBuildStatus(): Promise<ImageBuildStatus> {
  const res = await adminFetch(`${BASE}/build/status`)
  if (!res.ok) {
    throw new Error(`Failed to fetch build status: ${res.statusText}`)
  }
  return res.json()
}

// --- Platform Dockerfile CRUD ---

export async function fetchPlatformDockerfile(): Promise<string> {
  const res = await adminFetch(`${BASE}/dockerfile`)
  if (!res.ok) return ''
  const data = await res.json()
  return data.dockerfile_body || ''
}

export async function savePlatformDockerfile(dockerfileBody: string): Promise<{ ok: boolean; error?: string }> {
  const res = await adminFetch(`${BASE}/dockerfile`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ dockerfile_body: dockerfileBody }),
  })
  if (!res.ok) {
    const data = await res.json().catch(() => ({}))
    return { ok: false, error: data.error || res.statusText }
  }
  return { ok: true }
}
