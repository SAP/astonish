import { useState, useEffect, useCallback, useRef } from 'react'
import { Loader2, Play, AlertTriangle, CheckCircle2, ChevronDown, ChevronRight, Info, Save } from 'lucide-react'
import CodeMirror from '@uiw/react-codemirror'
import { StreamLanguage } from '@codemirror/language'
import { dockerFile } from '@codemirror/legacy-modes/mode/dockerfile'
import { search, searchKeymap, highlightSelectionMatches } from '@codemirror/search'
import { keymap, EditorView } from '@codemirror/view'
import { InlineError, InlineSuccess } from './shared'
import { inputStyle } from './sharedStyles'
import * as api from '../../api/platformSandbox'
import type { BaseConfig, BaseConfigSummary, OptionalTool, ConfigureBuildResult, UnsupportedBackendInfo, OpenShellBackendInfo } from '../../api/platformSandbox'

// ---------------------------------------------------------------------------
// SandboxBaseTab — Platform Admin: Configure Base Sandbox
// ---------------------------------------------------------------------------

export default function SandboxBaseTab() {
  const [loading, setLoading] = useState(true)
  const [summary, setSummary] = useState<BaseConfigSummary | null>(null)
  const [unsupported, setUnsupported] = useState<UnsupportedBackendInfo | null>(null)
  const [openshell, setOpenshell] = useState<OpenShellBackendInfo | null>(null)
  const [tools, setTools] = useState<OptionalTool[]>([])
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')

  // Form state
  const [core, setCore] = useState(true)
  const [selectedTools, setSelectedTools] = useState<string[]>([])
  const [browserEngine, setBrowserEngine] = useState<'none' | 'default' | 'cloakbrowser'>('none')
  const [extraSteps, setExtraSteps] = useState('')
  const [showAdvanced, setShowAdvanced] = useState(false)

  // OpenShell image state
  const [imageInput, setImageInput] = useState('')
  const [savingImage, setSavingImage] = useState(false)
  const [dockerfileBody, setDockerfileBody] = useState('')
  const [savedDockerfileBody, setSavedDockerfileBody] = useState('')
  const [savingDockerfile, setSavingDockerfile] = useState(false)

  // Build state
  const [building, setBuilding] = useState(false)
  const [buildLog, setBuildLog] = useState<string[]>([])
  const abortRef = useRef<(() => void) | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    setUnsupported(null)
    setOpenshell(null)
    try {
      const baseData = await api.getBaseConfig().catch(() => null)

      // Check if backend doesn't support base configuration
      if (baseData && 'unsupported_backend' in baseData && baseData.unsupported_backend) {
        setUnsupported(baseData as UnsupportedBackendInfo)
        setLoading(false)
        return
      }

      // Check if backend is openshell (image-only mode)
      if (baseData && 'backend' in baseData && baseData.backend === 'openshell') {
        const info = baseData as OpenShellBackendInfo
        setOpenshell(info)
        setImageInput(info.sandbox_image || '')
        // Load saved Dockerfile body
        api.fetchPlatformDockerfile().then(body => {
          setDockerfileBody(body)
          setSavedDockerfileBody(body)
        })
        setLoading(false)
        return
      }

      const toolsData = await api.listOptionalTools().catch(() => [])
      setSummary(baseData as BaseConfigSummary | null)
      setTools(toolsData)

      // Populate form from existing config if available
      const summaryData = baseData as BaseConfigSummary | null
      if (summaryData?.config) {
        const cfg = summaryData.config
        setCore(cfg.core)
        setSelectedTools(cfg.optional_tools || [])
        setBrowserEngine(cfg.browser?.engine || 'none')
        setExtraSteps((cfg.extra_steps || []).join('\n'))
        if (cfg.extra_steps && cfg.extra_steps.length > 0) {
          setShowAdvanced(true)
        }
      }
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { load() }, [load])

  // Cleanup abort on unmount
  useEffect(() => {
    return () => { abortRef.current?.() }
  }, [])

  const handleBuild = () => {
    setError('')
    setSuccess('')
    setBuildLog([])
    setBuilding(true)

    const config: BaseConfig = {
      core,
      optional_tools: selectedTools,
      browser: { engine: browserEngine },
      extra_steps: extraSteps.trim() ? extraSteps.trim().split('\n').filter(l => l.trim()) : undefined,
    }

    const { abort } = api.configureBase({
      config,
      onProgress: (msg: string) => {
        setBuildLog(prev => [...prev, msg])
      },
      onDone: (result: ConfigureBuildResult) => {
        setBuilding(false)
        setSuccess(`Build complete. Layer: ${result.layer_id} (${formatBytes(result.size_bytes)})`)
        load() // Refresh summary
      },
      onError: (err: string) => {
        setBuilding(false)
        setError(err)
      },
    })

    abortRef.current = abort
  }

  const handleCancel = () => {
    if (!confirm('Cancel the build? The in-progress build will be aborted and no changes will be applied.')) return
    abortRef.current?.()
    setBuilding(false)
    setBuildLog(prev => [...prev, '--- Build cancelled by user ---'])
  }

  const toggleTool = (id: string) => {
    setSelectedTools(prev =>
      prev.includes(id) ? prev.filter(t => t !== id) : [...prev, id]
    )
  }

  const handleSaveImage = async () => {
    setError('')
    setSuccess('')
    setSavingImage(true)
    try {
      await api.setBaseImage(imageInput.trim())
      setSuccess(imageInput.trim() ? `Image set to: ${imageInput.trim()}` : 'Custom image cleared. Using default image.')
      load()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setSavingImage(false)
    }
  }

  if (loading) {
    return (
      <div className="flex items-center justify-center py-12">
        <Loader2 size={24} className="animate-spin" style={{ color: 'var(--brand)' }} />
      </div>
    )
  }

  if (unsupported) {
    return (
      <div className="p-6 space-y-4">
        <div>
          <h3 className="text-sm font-semibold" style={{ color: 'var(--text-primary)' }}>Base Sandbox Configuration</h3>
        </div>
        <div className="flex items-start gap-3 p-4 rounded-lg" style={{ backgroundColor: 'var(--bg-secondary)', border: '1px solid var(--border)' }}>
          <Info size={18} className="mt-0.5 shrink-0" style={{ color: 'var(--brand)' }} />
          <div className="space-y-2">
            <p className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>
              Not available with the {unsupported.backend} backend
            </p>
            <p className="text-xs" style={{ color: 'var(--text-muted)' }}>
              {unsupported.message}
            </p>
          </div>
        </div>
      </div>
    )
  }

  if (openshell) {
    return (
      <div className="p-6 space-y-6 overflow-y-auto" style={{ maxHeight: 'calc(100vh - 120px)' }}>
        {/* Header */}
        <div>
          <h3 className="text-sm font-semibold" style={{ color: 'var(--text-primary)' }}>Base Sandbox Image</h3>
          <p className="text-xs mt-1" style={{ color: 'var(--text-muted)' }}>
            Configure the container image for the OpenShell sandbox backend. All sandboxes use this image unless overridden by a team template.
          </p>
        </div>

        <InlineError msg={error} />
        <InlineSuccess msg={success} />

        {/* Current status */}
        <div className="rounded-xl p-4 space-y-3" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium" style={{ color: 'var(--text-secondary)' }}>Current Image</span>
            {openshell.sandbox_image ? (
              <span className="text-xs px-2 py-0.5 rounded-full" style={{ background: 'rgba(34, 197, 94, 0.1)', color: '#22c55e' }}>
                Custom
              </span>
            ) : (
              <span className="text-xs px-2 py-0.5 rounded-full" style={{ background: 'rgba(107, 114, 128, 0.1)', color: 'var(--text-muted)' }}>
                Default
              </span>
            )}
          </div>
          <p className="text-xs font-mono break-all" style={{ color: 'var(--text-primary)' }}>
            {openshell.sandbox_image || openshell.default_image || 'Not configured'}
          </p>
          {openshell.default_image && openshell.sandbox_image && (
            <p className="text-[11px]" style={{ color: 'var(--text-muted)' }}>
              Default: <span className="font-mono">{openshell.default_image}</span>
            </p>
          )}
        </div>

        {/* Image input form */}
        <div className="rounded-xl p-4 space-y-4" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
          <h4 className="text-xs font-semibold" style={{ color: 'var(--text-primary)' }}>Custom Image</h4>

          <div className="space-y-2">
            <label className="block text-xs font-medium" style={{ color: 'var(--text-secondary)' }}>
              Container Image Reference
            </label>
            <input
              type="text"
              value={imageInput}
              onChange={e => setImageInput(e.target.value)}
              placeholder={openshell.default_image || 'ghcr.io/org/custom-sandbox:latest'}
              className="w-full px-3 py-2 rounded-lg text-xs outline-none font-mono"
              style={inputStyle}
            />
            <p className="text-[11px]" style={{ color: 'var(--text-muted)' }}>
              Leave empty to use the default image. The image must have the OpenShell supervisor installed and be compatible with the Landlock security policy.
            </p>
          </div>

          <div className="flex items-center justify-end gap-2 pt-2" style={{ borderTop: '1px solid var(--border-color)' }}>
            {imageInput !== (openshell.sandbox_image || '') && (
              <button
                onClick={() => setImageInput(openshell.sandbox_image || '')}
                className="px-3 py-1.5 rounded-lg text-xs font-medium"
                style={{ color: 'var(--text-muted)', border: '1px solid var(--border-color)' }}
              >
                Reset
              </button>
            )}
            <button
              onClick={handleSaveImage}
              disabled={savingImage || imageInput === (openshell.sandbox_image || '')}
              className="flex items-center gap-1.5 px-4 py-2 rounded-lg text-xs font-medium text-white transition-opacity"
              style={{ background: 'var(--brand)', opacity: (savingImage || imageInput === (openshell.sandbox_image || '')) ? 0.5 : 1 }}
            >
              {savingImage ? <Loader2 size={12} className="animate-spin" /> : <Save size={12} />}
              {savingImage ? 'Saving...' : 'Save Image'}
            </button>
          </div>
        </div>

        {/* Dockerfile-based image build */}
        <div className="rounded-xl p-4 space-y-4" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
          <div className="flex items-center justify-between">
            <div>
              <h4 className="text-xs font-semibold" style={{ color: 'var(--text-primary)' }}>Dockerfile Recipe</h4>
              <p className="text-[11px] mt-0.5" style={{ color: 'var(--text-muted)' }}>
                Write Dockerfile instructions to customize the base sandbox image. <code>FROM</code> is added automatically.
              </p>
            </div>
            {dockerfileBody !== savedDockerfileBody && (
              <span className="text-[10px] px-2 py-0.5 rounded-full" style={{ background: 'rgba(234, 179, 8, 0.1)', color: '#eab308', border: '1px solid rgba(234, 179, 8, 0.3)' }}>
                Unsaved
              </span>
            )}
          </div>

          <div className="rounded-lg overflow-hidden" style={{ border: '1px solid var(--border-color)' }}>
            <CodeMirror
              value={dockerfileBody}
              onChange={setDockerfileBody}
              height="240px"
              extensions={[
                StreamLanguage.define(dockerFile),
                search({ scrollToMatch: (range) => EditorView.scrollIntoView(range, { y: 'center', yMargin: 100 }) }),
                highlightSelectionMatches(),
                keymap.of(searchKeymap),
              ]}
              theme="dark"
              editable={!building}
              className="text-xs"
              placeholder={"# Example:\nRUN apt-get update && apt-get install -y curl git\nRUN pip install requests\nENV MY_VAR=value"}
              basicSetup={{
                lineNumbers: true,
                highlightActiveLineGutter: true,
                highlightActiveLine: true,
                foldGutter: false,
              }}
            />
          </div>

          <div className="flex items-center gap-2 p-2 rounded-lg" style={{ background: 'rgba(59, 130, 246, 0.05)', border: '1px solid rgba(59, 130, 246, 0.15)' }}>
            <Info size={12} style={{ color: '#3b82f6', flexShrink: 0 }} />
            <span className="text-[11px]" style={{ color: 'var(--text-muted)' }}>
              Allowed: RUN, ENV, ARG, WORKDIR, COPY --from=, LABEL. Forbidden: FROM, ENTRYPOINT, CMD, EXPOSE (controlled by platform).
            </span>
          </div>

          {/* Build log */}
          {buildLog.length > 0 && (
            <div className="font-mono text-[11px] space-y-0.5 max-h-48 overflow-y-auto p-3 rounded-lg"
              style={{ background: 'var(--bg-tertiary)', color: 'var(--text-muted)' }}>
              {buildLog.map((line, i) => (
                <div key={i}>{line}</div>
              ))}
              {building && (
                <div className="flex items-center gap-2 mt-1">
                  <Loader2 size={10} className="animate-spin" /> Building...
                </div>
              )}
            </div>
          )}

          <div className="flex items-center justify-end gap-2 pt-2" style={{ borderTop: '1px solid var(--border-color)' }}>
            {dockerfileBody !== savedDockerfileBody && (
              <button
                onClick={async () => {
                  setSavingDockerfile(true)
                  setError('')
                  const result = await api.savePlatformDockerfile(dockerfileBody)
                  if (result.ok) {
                    setSavedDockerfileBody(dockerfileBody)
                    setSuccess('Dockerfile saved')
                  } else {
                    setError(result.error || 'Failed to save Dockerfile')
                  }
                  setSavingDockerfile(false)
                }}
                disabled={savingDockerfile}
                className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium"
                style={{ color: 'var(--text-primary)', border: '1px solid var(--border-color)' }}
              >
                {savingDockerfile ? <Loader2 size={12} className="animate-spin" /> : <Save size={12} />}
                Save
              </button>
            )}
            {building && (
              <button
                onClick={() => { abortRef.current?.(); setBuilding(false); setBuildLog(prev => [...prev, '--- Build cancelled ---']) }}
                className="px-3 py-1.5 rounded-lg text-xs font-medium"
                style={{ color: '#ef4444', border: '1px solid rgba(239, 68, 68, 0.3)' }}
              >
                Cancel
              </button>
            )}
            <button
              onClick={() => {
                if (!dockerfileBody.trim()) return
                setError('')
                setSuccess('')
                setBuildLog([])
                setBuilding(true)
                // Save first, then build
                api.savePlatformDockerfile(dockerfileBody).then(saveResult => {
                  if (!saveResult.ok) {
                    setError(saveResult.error || 'Failed to save Dockerfile before build')
                    setBuilding(false)
                    return
                  }
                  setSavedDockerfileBody(dockerfileBody)
                  const { abort } = api.buildBaseImage({
                    dockerfileBody,
                    onProgress: (msg) => setBuildLog(prev => [...prev, msg]),
                    onDone: (result) => {
                      setBuilding(false)
                      setSuccess(`Build complete! Image: ${result.image}`)
                      setImageInput(result.image)
                      load()
                    },
                    onError: (err) => {
                      setBuilding(false)
                      setError(err)
                    },
                  })
                  abortRef.current = abort
                })
              }}
              disabled={building || !dockerfileBody.trim()}
              className="flex items-center gap-1.5 px-4 py-2 rounded-lg text-xs font-medium text-white transition-opacity"
              style={{ background: 'var(--brand)', opacity: (building || !dockerfileBody.trim()) ? 0.5 : 1 }}
            >
              {building ? <Loader2 size={12} className="animate-spin" /> : <Play size={12} />}
              {building ? 'Building...' : 'Build Image'}
            </button>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="p-6 space-y-6 overflow-y-auto" style={{ maxHeight: 'calc(100vh - 120px)' }}>
      {/* Header */}
      <div>
        <h3 className="text-sm font-semibold" style={{ color: 'var(--text-primary)' }}>Base Sandbox Configuration</h3>
        <p className="text-xs mt-1" style={{ color: 'var(--text-muted)' }}>
          Configure what gets installed in the @base sandbox template. All user sandboxes inherit from this layer.
        </p>
      </div>

      <InlineError msg={error} />
      <InlineSuccess msg={success} />

      {/* Current status */}
      {summary && (
        <div className="rounded-xl p-4 space-y-2" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium" style={{ color: 'var(--text-secondary)' }}>Current State</span>
            {summary.config ? (
              <span className="text-xs px-2 py-0.5 rounded-full" style={{ background: 'rgba(34, 197, 94, 0.1)', color: '#22c55e' }}>
                Configured
              </span>
            ) : (
              <span className="text-xs px-2 py-0.5 rounded-full" style={{ background: 'rgba(245, 158, 11, 0.1)', color: '#f59e0b' }}>
                Unconfigured
              </span>
            )}
          </div>
          {summary.config && (
            <div className="grid grid-cols-3 gap-4 text-xs" style={{ color: 'var(--text-muted)' }}>
              <div>
                <span className="block font-medium" style={{ color: 'var(--text-secondary)' }}>Layer</span>
                <span className="font-mono">{summary.layer_id?.slice(0, 12) || 'none'}...</span>
              </div>
              <div>
                <span className="block font-medium" style={{ color: 'var(--text-secondary)' }}>Size</span>
                {formatBytes(summary.size_bytes)}
              </div>
              <div>
                <span className="block font-medium" style={{ color: 'var(--text-secondary)' }}>Last Built</span>
                {summary.configured_at ? new Date(summary.configured_at).toLocaleString() : 'never'}
              </div>
            </div>
          )}
          {!summary.config && (
            <p className="text-xs" style={{ color: 'var(--text-muted)' }}>
              No base configuration has been applied yet. User sandboxes are running on the bare image.
            </p>
          )}
        </div>
      )}

      {/* Configuration form */}
      <div className="rounded-xl p-4 space-y-5" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
        <h4 className="text-xs font-semibold" style={{ color: 'var(--text-primary)' }}>Configuration</h4>

        {/* Core tools toggle */}
        <div className="flex items-center justify-between">
          <div>
            <span className="text-xs font-medium" style={{ color: 'var(--text-secondary)' }}>Core Tools</span>
            <p className="text-xs mt-0.5" style={{ color: 'var(--text-muted)' }}>
              Essential packages: git, curl, Node.js 22, Python 3, uv, Docker CLI, jq, ripgrep, etc.
            </p>
          </div>
          <button
            onClick={() => setCore(!core)}
            className="px-3 py-1.5 rounded-lg text-xs font-medium transition-colors"
            style={{
              background: core ? 'rgba(34, 197, 94, 0.15)' : 'rgba(107, 114, 128, 0.15)',
              color: core ? '#22c55e' : 'var(--text-muted)',
            }}
          >
            {core ? 'Enabled' : 'Disabled'}
          </button>
        </div>

        {/* Optional tools */}
        {tools.length > 0 && (
          <div>
            <span className="text-xs font-medium" style={{ color: 'var(--text-secondary)' }}>Optional Tools</span>
            <div className="mt-2 grid grid-cols-2 gap-2">
              {tools.map(tool => (
                <label
                  key={tool.id}
                  className="flex items-start gap-2 p-2.5 rounded-lg cursor-pointer transition-colors"
                  style={{
                    background: selectedTools.includes(tool.id) ? 'var(--accent-soft)' : 'var(--bg-tertiary)',
                    border: selectedTools.includes(tool.id) ? '1px solid var(--accent)' : '1px solid transparent',
                  }}
                >
                  <input
                    type="checkbox"
                    checked={selectedTools.includes(tool.id)}
                    onChange={() => toggleTool(tool.id)}
                    className="mt-0.5"
                  />
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-1.5">
                      <span className="text-xs font-medium" style={{ color: 'var(--text-primary)' }}>{tool.name}</span>
                      {tool.recommended && (
                        <span className="text-[10px] px-1.5 py-0.5 rounded-full" style={{ background: 'rgba(59, 130, 246, 0.15)', color: '#3b82f6' }}>
                          recommended
                        </span>
                      )}
                    </div>
                    <p className="text-[11px] mt-0.5 truncate" style={{ color: 'var(--text-muted)' }}>{tool.description}</p>
                  </div>
                </label>
              ))}
            </div>
          </div>
        )}

        {/* Browser engine */}
        <div>
          <span className="text-xs font-medium" style={{ color: 'var(--text-secondary)' }}>Browser Engine</span>
          <p className="text-xs mt-0.5 mb-2" style={{ color: 'var(--text-muted)' }}>
            Pre-install a browser into the base layer for web automation tasks.
          </p>
          <select
            value={browserEngine}
            onChange={e => setBrowserEngine(e.target.value as 'none' | 'default' | 'cloakbrowser')}
            className="w-full px-3 py-2 rounded-lg text-xs outline-none"
            style={inputStyle}
          >
            <option value="none">None</option>
            <option value="default">Chromium (headless, standard)</option>
            <option value="cloakbrowser">CloakBrowser + KasmVNC (anti-detection)</option>
          </select>
        </div>

        {/* Advanced section */}
        <div>
          <button
            onClick={() => setShowAdvanced(!showAdvanced)}
            className="flex items-center gap-1 text-xs font-medium"
            style={{ color: 'var(--text-muted)' }}
          >
            {showAdvanced ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
            Advanced
          </button>
          {showAdvanced && (
            <div className="mt-3 space-y-2">
              <div className="flex items-start gap-2 p-2.5 rounded-lg" style={{ background: 'rgba(245, 158, 11, 0.08)', border: '1px solid rgba(245, 158, 11, 0.2)' }}>
                <AlertTriangle size={13} className="mt-0.5 flex-shrink-0" style={{ color: '#f59e0b' }} />
                <p className="text-[11px]" style={{ color: '#f59e0b' }}>
                  Extra steps run as root inside the build pod. Incorrect commands can break the base layer
                  and require a full rebuild. Use with caution.
                </p>
              </div>
              <label className="block text-xs font-medium" style={{ color: 'var(--text-secondary)' }}>
                Extra Steps (one shell command per line)
              </label>
              <textarea
                value={extraSteps}
                onChange={e => setExtraSteps(e.target.value)}
                placeholder="apt-get install -y mypackage&#10;pip install mytool"
                rows={4}
                className="w-full px-3 py-2 rounded-lg text-xs outline-none font-mono resize-y"
                style={inputStyle}
              />
            </div>
          )}
        </div>
      </div>

      {/* Build log */}
      {buildLog.length > 0 && (
        <div className="rounded-xl p-4" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
          <span className="text-xs font-medium block mb-2" style={{ color: 'var(--text-secondary)' }}>Build Log</span>
          <div
            className="font-mono text-[11px] space-y-0.5 max-h-48 overflow-y-auto p-3 rounded-lg"
            style={{ background: 'var(--bg-tertiary)', color: 'var(--text-muted)' }}
          >
            {buildLog.map((line, i) => (
              <div key={i}>{line}</div>
            ))}
            {building && (
              <div className="flex items-center gap-2 mt-1">
                <Loader2 size={10} className="animate-spin" /> Running...
              </div>
            )}
          </div>
        </div>
      )}

      {/* Actions */}
      <div className="flex items-center justify-between pt-2" style={{ borderTop: '1px solid var(--border-color)' }}>
        <div className="text-xs" style={{ color: 'var(--text-muted)' }}>
          {building ? 'Build in progress...' : 'Ready to configure'}
        </div>
        <div className="flex items-center gap-2">
          {building && (
            <button
              onClick={handleCancel}
              className="px-4 py-2 rounded-lg text-xs font-medium"
              style={{ color: '#ef4444', border: '1px solid rgba(239, 68, 68, 0.3)' }}
            >
              Cancel
            </button>
          )}
          <button
            onClick={handleBuild}
            disabled={building}
            className="flex items-center gap-1.5 px-4 py-2 rounded-lg text-xs font-medium text-white transition-opacity"
            style={{ background: 'var(--brand)', opacity: building ? 0.5 : 1 }}
          >
            {building ? <Loader2 size={12} className="animate-spin" /> : <Play size={12} />}
            {building ? 'Building...' : 'Build Base Layer'}
          </button>
        </div>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function formatBytes(bytes: number): string {
  if (!bytes || bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  return `${(bytes / Math.pow(1024, i)).toFixed(1)} ${units[i]}`
}
