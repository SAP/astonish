import React from 'react'
import { Plus, Trash2, Sparkles } from 'lucide-react'
import type { RawToolOutputEditorProps, OutputModelEditorProps } from './nodeEditorTypes'

/**
 * Raw Tool Output Editor - Checkbox toggle with details
 */
export function RawToolOutputEditor({ value, onChange }: RawToolOutputEditorProps) {
  // raw_tool_output is a map, but we only support one key for now
  const existingKey = value ? (Object.keys(value)[0] || '') : ''
  const isEnabled = value !== undefined

  const handleToggle = (e: React.ChangeEvent<HTMLInputElement>) => {
    if (e.target.checked) {
      // Enable with empty map
      onChange({})
    } else {
      // Disable (remove field)
      onChange(undefined)
    }
  }

  const handleKeyChange = (newKey: string) => {
    if (!newKey) {
      onChange({})
    } else {
      // Preserve existing type if available, otherwise default to 'any'
      const currentType = value ? (Object.values(value)[0] || 'any') : 'any'
      onChange({ [newKey]: currentType })
    }
  }

  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2">
        <input 
          type="checkbox"
          checked={isEnabled}
          onChange={handleToggle}
          id="raw-tool-output-toggle"
          className="w-4 h-4 accent-primary rounded"
        />
        <label 
          htmlFor="raw-tool-output-toggle" 
          className="text-sm font-medium cursor-pointer" 
          style={{ color: 'var(--text-secondary)' }}
        >
          Raw Tool Output
        </label>
      </div>
      
      {isEnabled && (
        <div className="pl-6 space-y-2 animate-in fade-in slide-in-from-top-1 duration-200">
           <div className="text-xs p-3 rounded border border-blue-500/20" style={{ background: 'rgba(59, 130, 246, 0.05)', color: 'var(--text-muted)' }}>
            <p className="mb-1 font-medium text-blue-400 flex items-center gap-1">
              <Sparkles size={10} /> Context Optimization
            </p>
            <p>
              Store large tool outputs directly in state without sending them to the LLM. 
              The LLM will only see a success message.
            </p>
            <p className="mt-2 text-blue-400/80 italic">
              Use this when data is large and will be processed by subsequent nodes. 
              This isolates the data in state, avoiding unnecessary LLM round-trips and context overhead.
            </p>
          </div>

          <div className="flex gap-2">
            <input
              type="text"
              value={existingKey}
              onChange={(e) => handleKeyChange(e.target.value)}
              className="flex-1 px-3 py-2 text-sm rounded border"
              style={{ 
                background: 'var(--bg-primary)', 
                borderColor: 'var(--border-color)',
                color: 'var(--text-primary)'
              }}
              placeholder="Enter state variable name..."
            />
            <select
              value={value && existingKey ? value[existingKey] : 'any'}
              onChange={(e) => onChange({ [existingKey]: e.target.value })}
              className="px-3 py-2 text-sm rounded border w-24"
              style={{ 
                background: 'var(--bg-primary)', 
                borderColor: 'var(--border-color)',
                color: 'var(--text-primary)'
              }}
            >
              <option value="any">any</option>
              <option value="str">str</option>
              <option value="list">list</option>
              <option value="dict">dict</option>
              <option value="int">int</option>
              <option value="bool">bool</option>
            </select>
          </div>
        </div>
      )}
    </div>
  )
}

/**
 * Output Model Editor - key-value list for output_model field
 */
export function OutputModelEditor({ value, onChange, theme, hideLabel = false, singleField = false }: OutputModelEditorProps) {
  const entries = Object.entries(value || {})
  
  const handleAdd = () => {
    const newKey = `field_${Object.keys(value || {}).length + 1}`
    onChange({ ...value, [newKey]: 'str' })
  }
  
  const handleRemove = (key: string) => {
    const newValue = { ...value }
    delete newValue[key]
    onChange(newValue)
  }
  
  const handleKeyChange = (oldKey: string, newKey: string) => {
    if (oldKey === newKey) return
    const newValue: Record<string, string> = {}
    Object.entries(value || {}).forEach(([k, v]) => {
      newValue[k === oldKey ? newKey : k] = v
    })
    onChange(newValue)
  }
  
  const handleTypeChange = (key: string, type: string) => {
    onChange({ ...value, [key]: type })
  }
  
  return (
    <div className="space-y-2">
      {!hideLabel && (
        <div className="flex items-center justify-between">
          <label className="text-sm font-medium" style={{ color: 'var(--text-secondary)' }}>
            Output Model
          </label>
          {!singleField && (
            <button
              onClick={handleAdd}
              className="flex items-center gap-1 text-xs px-2 py-1 rounded bg-primary text-white hover:bg-primary/90"
            >
              <Plus size={12} /> Add
            </button>
          )}
        </div>
      )}
      
      {entries.length === 0 ? (
        <div className="text-xs py-2" style={{ color: 'var(--text-muted)' }}>
          {singleField ? 'Enter a variable name.' : 'No fields. Add at least one.'}
        </div>
      ) : (
        <div className="space-y-1">
          {entries.slice(0, singleField ? 1 : undefined).map(([key, type], idx) => (
            <div key={idx} className="flex items-center gap-1">
              <input
                type="text"
                value={key}
                onChange={(e) => handleKeyChange(key, e.target.value)}
                className="flex-1 px-2 py-1 text-xs rounded border"
                style={{ 
                  background: 'var(--bg-primary)', 
                  borderColor: 'var(--border-color)',
                  color: 'var(--text-primary)'
                }}
                placeholder="field"
              />
              <select
                value={type}
                onChange={(e) => handleTypeChange(key, e.target.value)}
                className="px-1 py-1 text-xs rounded border"
                style={{ 
                  background: 'var(--bg-primary)', 
                  borderColor: 'var(--border-color)',
                  color: 'var(--text-primary)'
                }}
              >
                <option value="str">str</option>
                <option value="int">int</option>
                <option value="list">list</option>
                <option value="any">any</option>
                <option value="bool">bool</option>
              </select>
              {!singleField && (
                <button
                  onClick={() => handleRemove(key)}
                  className="p-0.5 text-red-400 hover:text-red-300"
                >
                  <Trash2 size={12} />
                </button>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
