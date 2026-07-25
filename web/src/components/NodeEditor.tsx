import React, { useState, useEffect, useCallback } from 'react'
import { X, Brain, Sparkles } from 'lucide-react'
import { NODE_ICONS } from './nodes/nodeEditorTypes'
import type { NodeEditorProps } from './nodes/nodeEditorTypes'
import { OutputModelEditor } from './nodes/NodeEditorFields'
import { UpdateStateForm } from './nodes/UpdateStateForm'
import { InputNodeForm } from './nodes/InputNodeForm'
import { LlmNodeForm } from './nodes/LlmNodeForm'
import { ToolNodeForm } from './nodes/ToolNodeForm'
import { OutputNodeForm } from './nodes/OutputNodeForm'


/**
 * Main Node Editor Component - Horizontal Bottom Layout
 */
export default function NodeEditor({ node, onSave, onClose, theme, availableTools = [], availableVariables = [], readOnly, onAIAssist }: NodeEditorProps) {
  const [editedData, setEditedData] = useState<Record<string, any>>({})
  const [nodeName, setNodeName] = useState('')
  
  // Initialize state when node changes
  useEffect(() => {
    if (node) {
      setNodeName(node.data?.label || node.id)
      setEditedData(node.data?.yaml || {})
    }
  }, [node])
  
  // Save and close - called when user clicks Done, X, or clicks outside
  const handleClose = useCallback(() => {
    if (node) {
      const nodeType = node.data?.nodeType || node.type
      const savedData = {
        ...editedData,
        name: nodeName,
        type: nodeType === 'updateState' ? 'update_state' : nodeType,
      }
      onSave(node.id, savedData)
    }
    onClose()
  }, [node, editedData, nodeName, onSave, onClose])
  
  if (!node) return null
  
  const nodeType = node.data?.nodeType || node.type
  const Icon = NODE_ICONS[nodeType || ''] || Brain
  // Enforce stable purple color for all nodes as requested
  const color = '#7c3aed'
  
  // Render type-specific form
  const renderForm = () => {
    const props = { data: editedData, onChange: setEditedData, theme, availableVariables }
    
    switch (nodeType) {
      case 'update_state':
      case 'updateState':
        return <UpdateStateForm {...props} />
      case 'input':
        return <InputNodeForm {...props} />
      case 'llm':
        return <LlmNodeForm {...props} availableTools={availableTools} />
      case 'tool':
        return <ToolNodeForm {...props} availableTools={availableTools} />
      case 'output':
        return <OutputNodeForm {...props} />
      default:
        return <LlmNodeForm {...props} availableTools={availableTools} />
    }
  }
  
  return (
    <div 
      className="h-full flex flex-col overflow-hidden"
      style={{ background: 'var(--bg-secondary)' }}
    >
      {/* Header */}
      <div 
        className="flex items-center justify-between px-4 py-2 shrink-0"
        style={{ borderBottom: '1px solid var(--border-color)' }}
      >
        <div className="flex items-center gap-4">
          <div className="flex items-center gap-2">
            <div 
              className="w-7 h-7 rounded-lg flex items-center justify-center"
              style={{ background: color }}
            >
              <Icon size={14} className="text-white" />
            </div>
            <span className="font-semibold text-sm" style={{ color: 'var(--text-primary)' }}>
              {readOnly ? 'View Node' : 'Edit Node'}
            </span>
          </div>
          
          {/* Node Name */}
          <div className="flex items-center gap-2">
            <label className="text-xs" style={{ color: 'var(--text-muted)' }}>Name:</label>
            <input
              type="text"
              value={nodeName}
              onChange={(e) => setNodeName(e.target.value)}
              readOnly={readOnly}
              className="px-2 py-1 rounded border font-mono text-sm w-40"
              style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)', opacity: readOnly ? 0.7 : 1 }}
            />
          </div>
          
          {/* Type Badge */}
          <span 
            className="px-2 py-0.5 rounded text-xs font-medium text-white"
            style={{ background: color }}
          >
            {nodeType}
          </span>
          
          {/* Output Model - inline in header for non-LLM nodes (LLM has it in Output tab) */}
          {nodeType !== 'output' && nodeType !== 'llm' && (
            <div className="flex items-center gap-2 pl-4" style={{ borderLeft: '1px solid var(--border-color)' }}>
              <OutputModelEditor
                value={editedData.output_model}
                onChange={(newModel) => setEditedData({ ...editedData, output_model: newModel })}
                theme={theme}
                singleField={nodeType === 'input'}
              />
            </div>
          )}
        </div>
        
        {/* Actions */}
        <div className="flex items-center gap-2">
          {/* AI Assist - hidden in read-only mode */}
          {onAIAssist && !readOnly && (
            <button
              onClick={() => onAIAssist(node, nodeName, editedData)}
              className="send-gradient flex items-center gap-1.5 rounded px-3 py-1.5 text-sm font-medium text-white transition-all hover:opacity-90"
              title="Get AI suggestions for this node"
            >
              <Sparkles size={14} />
              AI Assist
            </button>
          )}
          <button
            onClick={handleClose}
            className="rounded bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90"
          >
            Done
          </button>
          <button
            onClick={handleClose}
            className="p-1 rounded hover:bg-gray-500/20 ml-2"
            style={{ color: 'var(--text-muted)' }}
          >
            <X size={18} />
          </button>
        </div>
      </div>
      
      {/* Content - Type-specific form */}
      <div className="flex-1 overflow-auto p-4">
        {renderForm()}
      </div>
    </div>
  )
}
