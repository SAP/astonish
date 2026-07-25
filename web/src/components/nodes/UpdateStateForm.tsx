import React from 'react'
import type { UpdateStateFormProps } from './nodeEditorTypes'

/**
 * Update State Form - Horizontal layout with source_variable support
 */
export function UpdateStateForm({ data, onChange, theme }: UpdateStateFormProps) {
  // Determine if using source_variable or value
  const useSourceVariable = data.source_variable !== undefined
  
  return (
    <div className="flex gap-6 h-full">
      {/* Left column - Settings */}
      <div className="w-64 space-y-4">
        <div>
          <label className="text-sm font-medium block mb-1" style={{ color: 'var(--text-secondary)' }}>
            Action
          </label>
          <select
            value={data.action || 'overwrite'}
            onChange={(e) => onChange({ ...data, action: e.target.value })}
            className="w-full px-3 py-2 rounded border"
            style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }}
          >
            <option value="overwrite">overwrite</option>
            <option value="append">append</option>
            <option value="increment">increment</option>
          </select>
        </div>
        
        <div>
          <label className="text-sm font-medium block mb-1" style={{ color: 'var(--text-secondary)' }}>
            Source Type
          </label>
          <select
            value={useSourceVariable ? 'variable' : 'value'}
            onChange={(e) => {
              if (e.target.value === 'variable') {
                // Switch to source_variable mode
                const newData: Record<string, any> = { ...data, source_variable: data.value || '' }
                delete newData.value
                onChange(newData)
              } else {
                // Switch to value mode
                const newData: Record<string, any> = { ...data, value: data.source_variable || '' }
                delete newData.source_variable
                onChange(newData)
              }
            }}
            className="w-full px-3 py-2 rounded border"
            style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }}
          >
            <option value="variable">From Variable</option>
            <option value="value">Literal Value</option>
          </select>
        </div>
        
        {/* Silent Mode Toggle */}
        <div className="flex items-center gap-2">
          <input
            type="checkbox"
            id="silent"
            checked={data.silent || false}
            onChange={(e) => onChange({ ...data, silent: e.target.checked })}
            className="w-4 h-4 rounded border accent-primary"
          />
          <label htmlFor="silent" className="text-sm" style={{ color: 'var(--text-secondary)' }}>
            Silent mode
          </label>
        </div>
      </div>
      
      {/* Right column - Source Variable or Value */}
      <div className="flex-1">
        {useSourceVariable ? (
          <>
            <label className="text-sm font-medium block mb-1" style={{ color: 'var(--text-secondary)' }}>
              Source Variable
            </label>
            <input
              type="text"
              value={data.source_variable || ''}
              onChange={(e) => onChange({ ...data, source_variable: e.target.value })}
              className="w-full px-3 py-2 rounded border font-mono text-sm"
              style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }}
              placeholder="Enter state variable name..."
            />
            <p className="text-xs mt-1" style={{ color: 'var(--text-muted)' }}>
              The value from this state variable will be used
            </p>
          </>
        ) : (
          <>
            <label className="text-sm font-medium block mb-1" style={{ color: 'var(--text-secondary)' }}>
              Value
            </label>
            <textarea
              value={data.value || ''}
              onChange={(e) => onChange({ ...data, value: e.target.value })}
              className="w-full h-32 px-3 py-2 rounded border font-mono text-sm resize-none"
              style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }}
              placeholder="Enter value or expression..."
            />
          </>
        )}
      </div>
    </div>
  )
}
