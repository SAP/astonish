import { useState } from 'react'
import { ExternalLink } from 'lucide-react'

import type { SetupStep } from '../../api/fleetChat'

interface SetupProvisionPanelProps {
  step: SetupStep
  draftId: string
  templateKey: string
  collected: Record<string, Record<string, unknown>>
  onOpenChat?: () => void
  onProvisioned?: (template: string, containerWorkspaceDir: string) => void
}

export default function SetupProvisionPanel({
  step,
  collected,
  onOpenChat,
  onProvisioned,
}: SetupProvisionPanelProps) {
  const [template, setTemplate] = useState(String(collected.provisioning?.template || ''))
  const [containerDir, setContainerDir] = useState(String(collected.provisioning?.container_workspace_dir || ''))

  const repo = String(collected.project_source?.repo || collected.channel?.repo || '')

  return (
    <div className="space-y-4 rounded-lg p-4" style={{ background: 'var(--brand-muted)', border: '1px solid color-mix(in oklab, var(--brand) 20%, transparent)' }}>
      <div>
        <h3 className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>{step.title}</h3>
        <p className="text-xs mt-1" style={{ color: 'var(--text-muted)' }}>
          {step.guidance || 'Clone the repository, install dependencies, verify the build, and save a sandbox template.'}
        </p>
      </div>
      {repo && (
        <p className="text-xs" style={{ color: 'var(--text-secondary)' }}>Repository: <span className="font-mono text-primary">{repo}</span></p>
      )}
      {onOpenChat && (
        <button
          onClick={onOpenChat}
          className="flex items-center gap-1.5 px-3 py-2 text-xs font-medium rounded-lg bg-primary hover:bg-primary/90 text-white"
        >
          <ExternalLink size={12} /> Continue in guided setup (chat)
        </button>
      )}
      <div className="space-y-3 pt-2" style={{ borderTop: '1px solid var(--border-color)' }}>
        <p className="text-xs" style={{ color: 'var(--text-muted)' }}>Or enter provisioning outputs manually after completing setup:</p>
        <label className="block text-xs">
          <span style={{ color: 'var(--text-secondary)' }}>Sandbox template name</span>
          <input
            value={template}
            onChange={e => {
              setTemplate(e.target.value)
              onProvisioned?.(e.target.value, containerDir)
            }}
            className="w-full mt-1 px-3 py-2 rounded-lg font-mono text-sm"
            style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }}
          />
        </label>
        <label className="block text-xs">
          <span style={{ color: 'var(--text-secondary)' }}>Container workspace dir</span>
          <input
            value={containerDir}
            onChange={e => {
              setContainerDir(e.target.value)
              onProvisioned?.(template, e.target.value)
            }}
            placeholder="/root/my-project"
            className="w-full mt-1 px-3 py-2 rounded-lg font-mono text-sm"
            style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }}
          />
        </label>
      </div>
    </div>
  )
}
