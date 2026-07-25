import { useEffect, useState } from 'react'
import { Loader } from 'lucide-react'

import { teamFetch } from '../../api/teamContext'
import type { SetupField } from '../../api/fleetChat'
import { renderSetupField } from './setupFieldRenderers'

interface CredentialEntry {
  name: string
  logical_name?: string
}

interface SetupCredentialsPanelProps {
  fields: SetupField[]
  stepId: string
  collected: Record<string, Record<string, unknown>>
  onFieldChange: (fieldId: string, value: unknown) => void
}

export default function SetupCredentialsPanel({ fields, stepId, collected, onFieldChange }: SetupCredentialsPanelProps) {
  const [credentials, setCredentials] = useState<CredentialEntry[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    teamFetch('/api/credentials')
      .then(r => r.ok ? r.json() : { credentials: [] })
      .then(data => setCredentials((data.credentials || data.items || []) as CredentialEntry[]))
      .catch(() => setCredentials([]))
      .finally(() => setLoading(false))
  }, [])

  if (loading) {
    return <Loader size={16} className="animate-spin text-primary" />
  }

  return (
    <div className="space-y-4">
      {credentials.length > 0 && (
        <div className="space-y-2">
          <p className="text-xs font-medium" style={{ color: 'var(--text-secondary)' }}>Stored credentials</p>
          <div className="flex flex-wrap gap-2">
            {credentials.map(c => (
              <button
                key={c.name}
                type="button"
                onClick={() => {
                  const field = fields.find(f => f.type === 'credential_ref')
                  if (field) onFieldChange(field.id, c.name)
                }}
                className="text-xs px-2 py-1 rounded-lg hover:bg-primary/10"
                style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)', border: '1px solid var(--border-color)' }}
              >
                {c.logical_name || c.name}
              </button>
            ))}
          </div>
        </div>
      )}
      {fields.map(field => (
        <div key={field.id}>
          {renderSetupField(field, collected[stepId]?.[field.id], v => onFieldChange(field.id, v))}
        </div>
      ))}
    </div>
  )
}
