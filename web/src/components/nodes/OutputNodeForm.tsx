import React from 'react'
import { Plus, Trash2 } from 'lucide-react'
import { VariablePanel } from './NodeEditorWidgets'
import type { OutputNodeFormProps } from './nodeEditorTypes'

/**
 * Output Node Form - with user_message array editor
 */
export function OutputNodeForm({ data, onChange, theme, availableVariables = [] }: OutputNodeFormProps) {
  const userMessage: string[] = data.user_message || []
  
  // Get flat list of all valid variable names
  const allValidVars = availableVariables.flatMap(g => g.variables || [])
  
  const handleAddItem = (initialValue: string = '') => {
    onChange({ ...data, user_message: [...userMessage, initialValue] })
  }
  
  const handleRemoveItem = (index: number) => {
    const newItems = userMessage.filter((_: string, i: number) => i !== index)
    onChange({ ...data, user_message: newItems })
  }
  
  const handleItemChange = (index: number, value: string) => {
    const newItems = [...userMessage]
    newItems[index] = value
    onChange({ ...data, user_message: newItems })
  }
  
  // Check if a value matches a known state variable
  const isVariable = (value: string) => {
    const trimmed = value.trim()
    return trimmed && allValidVars.includes(trimmed)
  }
  
  return (
    <div className="flex gap-6 h-full">
      {/* Left column - Info */}
      {/* Left column - Variable Selector */}
      <VariablePanel
        variableGroups={availableVariables}
        onVariableClick={(v) => handleAddItem(v)}
      />
      
      {/* Right column - User Message Editor */}
      <div className="flex-1">
        <div className="flex justify-between items-center mb-2">
          <label className="text-sm font-medium" style={{ color: 'var(--text-secondary)' }}>
            User Message
          </label>
          <button
            type="button"
            onClick={() => handleAddItem()}
            className="flex items-center gap-1 px-2 py-1 rounded text-xs transition-colors hover:opacity-80"
            style={{ background: 'var(--accent-color)', color: 'white' }}
          >
            <Plus size={12} />
            Add Item
          </button>
        </div>
        
        {userMessage.length === 0 ? (
          <div 
            className="text-sm p-4 rounded border border-dashed text-center"
            style={{ borderColor: 'var(--border-color)', color: 'var(--text-muted)' }}
          >
            No items yet. Click "Add Item" to add a message part.
          </div>
        ) : (
          <div className="space-y-3 max-h-64 overflow-y-auto">
            {userMessage.map((item: string, index: number) => (
              <div key={index} className="flex items-start gap-2">
                <span 
                  className="text-xs px-2 py-1 rounded mt-2" 
                  style={{ background: 'var(--bg-tertiary)', color: 'var(--text-muted)' }}
                >
                  {index + 1}
                </span>
                <div className="flex-1 relative">
                  <textarea
                    value={item}
                    onChange={(e) => handleItemChange(index, e.target.value)}
                    className="w-full px-3 py-2 rounded border text-sm resize-none font-mono"
                    style={{ 
                      background: 'var(--bg-primary)', 
                      borderColor: isVariable(item) ? 'var(--brand)' : 'var(--border-color)', 
                      color: 'var(--text-primary)',
                      minHeight: '60px'
                    }}
                    placeholder="Enter literal text or a state variable name..."
                    rows={2}
                  />
                  {/* Variable indicator badge */}
                  {item.trim() && (
                    <span 
                      className="absolute top-1 right-1 text-xs px-1.5 py-0.5 rounded"
                      style={{ 
                        background: isVariable(item) ? 'color-mix(in oklab, var(--brand) 20%, transparent)' : 'rgba(100, 100, 100, 0.2)',
                        color: isVariable(item) ? 'var(--brand)' : 'var(--text-muted)',
                        border: isVariable(item) ? '1px solid color-mix(in oklab, var(--brand) 30%, transparent)' : '1px solid transparent'
                      }}
                    >
                      {isVariable(item) ? '📌 Variable' : '📝 Literal'}
                    </span>
                  )}
                </div>
                <button
                  type="button"
                  onClick={() => handleRemoveItem(index)}
                  className="p-1.5 rounded hover:bg-red-500/20 text-red-500 mt-2"
                >
                  <Trash2 size={14} />
                </button>
              </div>
            ))}
          </div>
        )}
        
        <p className="text-xs mt-2" style={{ color: 'var(--text-muted)' }}>
          Multi-line text is supported. Variables are resolved at runtime.
        </p>
      </div>
    </div>
  )
}
