import React from 'react'
import { Plus, Trash2, AlertCircle } from 'lucide-react'
import ToolSelector from '../ToolSelector'
import type { ToolNodeFormProps } from './nodeEditorTypes'

/**
 * Tool Node Form - Horizontal layout with dropdown
 */
export function ToolNodeForm({ data, onChange, theme, availableTools = [] }: ToolNodeFormProps) {
  const currentTools: string[] = data.tools_selection || []
  const toolNames = availableTools.map(t => t.name)
  
  // Check if any selected tool is not in the available list
  const hasInvalidTool = currentTools.length > 0 && currentTools.some(t => !toolNames.includes(t))
  
  const handleAddTool = (toolName: string) => {
    if (toolName && !currentTools.includes(toolName)) {
      onChange({ ...data, tools_selection: [...currentTools, toolName] })
    }
  }
  
  const handleRemoveTool = (toolName: string) => {
    onChange({ ...data, tools_selection: currentTools.filter(t => t !== toolName) })
  }
  
  return (
    <div className="flex gap-6 h-full">
      {/* Left column - Settings */}
      <div className="w-80 space-y-4">
        <div>
          <label className="text-sm font-medium block mb-1" style={{ color: 'var(--text-secondary)' }}>
            Tools Selection
          </label>
          
          <ToolSelector
            availableTools={availableTools}
            selectedTools={currentTools}
            onAddTool={handleAddTool}
            onRemoveTool={handleRemoveTool}
            placeholder="Search tools..."
          />
          
          {/* Show invalid tools warning */}
          {hasInvalidTool && (
            <div className="mt-2 p-2 rounded border border-red-500 bg-red-500/10">
              <div className="flex items-center gap-2 text-xs text-red-400">
                <AlertCircle size={12} />
                <span>Some selected tools are not available</span>
              </div>
            </div>
          )}
        </div>
        

        
        <div className="flex items-center justify-between">
          <label className="text-sm font-medium" style={{ color: 'var(--text-secondary)' }}>
            Auto-approve
          </label>
          <input
            type="checkbox"
            checked={data.tools_auto_approval === true}
            onChange={(e) => onChange({ ...data, tools_auto_approval: e.target.checked || undefined })}
            className="w-4 h-4 accent-primary"
          />
        </div>
        
        <div className="flex items-center justify-between">
          <div>
            <label className="text-sm font-medium" style={{ color: 'var(--text-secondary)' }}>
              Continue on Error
            </label>
            <p className="text-xs" style={{ color: 'var(--text-muted)' }}>
              Capture errors instead of stopping
            </p>
          </div>
          <input
            type="checkbox"
            checked={data.continue_on_error === true}
            onChange={(e) => onChange({ ...data, continue_on_error: e.target.checked || undefined })}
            className="w-4 h-4 accent-primary"
          />
        </div>
        
        {/* Silent Mode Toggle */}
        <div className="flex items-center justify-between">
          <label className="text-sm font-medium" style={{ color: 'var(--text-secondary)' }}>
            Silent mode
          </label>
          <input
            type="checkbox"
            checked={data.silent || false}
            onChange={(e) => onChange({ ...data, silent: e.target.checked })}
            className="w-4 h-4 accent-primary"
          />
        </div>

      </div>
      
      {/* Right column - Args */}
      <div className="flex-1">
        <div className="flex items-center justify-between mb-2">
          <div>
            <label className="text-sm font-medium block" style={{ color: 'var(--text-secondary)' }}>
              Arguments
            </label>
            <p className="text-xs" style={{ color: 'var(--text-muted)' }}>
              Key-value pairs passed to the tool
            </p>
          </div>
          <button
            onClick={() => {
              const newKey = `arg_${Object.keys(data.args || {}).length + 1}`
              onChange({ ...data, args: { ...(data.args || {}), [newKey]: '{variable}' } })
            }}
            className="flex items-center gap-1 px-2.5 py-1 text-xs rounded bg-primary hover:bg-primary/90 text-white transition-colors"
          >
            <Plus size={12} />
            Add
          </button>
        </div>
        
        <div className="space-y-2">
          {Object.entries(data.args || {}).map(([key, value]: [string, any], idx: number) => (
            <div key={idx} className="flex items-center gap-2">
              <input
                type="text"
                value={key}
                onChange={(e) => {
                  const newArgs: Record<string, any> = {}
                  Object.entries(data.args || {}).forEach(([k, v]) => {
                    newArgs[k === key ? e.target.value : k] = v
                  })
                  onChange({ ...data, args: newArgs })
                }}
                className="w-32 px-2 py-1.5 rounded border font-mono text-sm"
                style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }}
                placeholder="key"
              />
              <span className="text-xs" style={{ color: 'var(--text-muted)' }}>=</span>
              <input
                type="text"
                value={typeof value === 'string' ? value : JSON.stringify(value)}
                onChange={(e) => {
                  onChange({ ...data, args: { ...(data.args || {}), [key]: e.target.value } })
                }}
                className="flex-1 px-2 py-1.5 rounded border font-mono text-sm"
                style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }}
                placeholder="{variable} or value"
              />
              <button
                onClick={() => {
                  const newArgs = { ...data.args }
                  delete newArgs[key]
                  onChange({ ...data, args: Object.keys(newArgs).length > 0 ? newArgs : undefined })
                }}
                className="p-1.5 text-red-400 hover:text-red-300 hover:bg-red-500/20 rounded"
              >
                <Trash2 size={14} />
              </button>
            </div>
          ))}
          {(!data.args || Object.keys(data.args).length === 0) && (
            <p className="text-xs italic py-2" style={{ color: 'var(--text-muted)' }}>
              No arguments. Click "Add" to add key-value pairs.
            </p>
          )}
        </div>
      </div>
    </div>
  )
}
