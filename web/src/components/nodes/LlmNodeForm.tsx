import React, { useState, useRef } from 'react'
import { Plus, Trash2, AlertCircle } from 'lucide-react'
import ToolSelector from '../ToolSelector'
import { VariablePanel, HighlightedTextarea } from './NodeEditorWidgets'
import { RawToolOutputEditor, OutputModelEditor } from './NodeEditorFields'
import type { LlmNodeFormProps } from './nodeEditorTypes'

/**
 * LLM Node Form - Horizontal layout with tabs
 */
export function LlmNodeForm({ data, onChange, theme, availableTools = [], availableVariables = [] }: LlmNodeFormProps) {
  const [activeTab, setActiveTab] = useState('prompts')
  const [activeField, setActiveField] = useState('prompt') // 'system' or 'prompt'
  const systemPromptRef = useRef<HTMLTextAreaElement>(null)
  const userPromptRef = useRef<HTMLTextAreaElement>(null)
  
  const currentTools: string[] = data.tools_selection || []
  const toolNames = availableTools.map(t => t.name)
  
  const handleAddTool = (toolName: string) => {
    if (toolName && !currentTools.includes(toolName)) {
      onChange({ ...data, tools_selection: [...currentTools, toolName] })
    }
  }
  
  const handleRemoveTool = (toolName: string) => {
    onChange({ ...data, tools_selection: currentTools.filter(t => t !== toolName) })
  }
  
  // Get the currently active textarea ref based on focus tracking
  const getActiveRef = () => activeField === 'system' ? systemPromptRef : userPromptRef
  const getActiveValue = () => activeField === 'system' ? (data.system || '') : (data.prompt || '')
  const setActiveValue = (val: string) => {
    if (activeField === 'system') {
      onChange({ ...data, system: val || undefined })
    } else {
      onChange({ ...data, prompt: val })
    }
  }
  
  const tabs = [
    { id: 'prompts', label: 'Prompts' },
    { id: 'output', label: 'Output' },
    { id: 'tools', label: 'Tools' },
    { id: 'advanced', label: 'Advanced' },
  ]
  
  return (
    <div className="h-full flex flex-col">
      {/* Tabs */}
      <div className="flex gap-1 mb-3">
        {tabs.map(tab => (
          <button
            key={tab.id}
            onClick={() => setActiveTab(tab.id)}
            className={`px-3 py-1.5 text-sm font-medium rounded-t transition-colors ${
              activeTab === tab.id ? 'bg-primary text-white' : 'bg-gray-600/30'
            }`}
            style={{ color: activeTab !== tab.id ? 'var(--text-muted)' : undefined }}
          >
            {tab.label}
          </button>
        ))}
      </div>
      
      {/* Tab Content */}
      <div className="flex-1 overflow-hidden">
        {activeTab === 'prompts' && (
          <div className="flex h-full overflow-hidden">
            {/* Variable Panel - Left Sidebar */}
            <VariablePanel
              variableGroups={availableVariables}
              activeTextareaRef={getActiveRef()}
              getValue={getActiveValue}
              setValue={setActiveValue}
            />
            
            {/* Prompts - Right side */}
            <div className="flex-1 flex gap-4">
              {/* System Prompt */}
              <div className="flex-1 flex flex-col">
                <label className="text-sm font-medium block mb-1" style={{ color: 'var(--text-secondary)' }}>
                  System Prompt (optional)
                </label>
                <HighlightedTextarea
                  ref={systemPromptRef}
                  value={data.system || ''}
                  onChange={(e) => onChange({ ...data, system: e.target.value || undefined })}
                  onFocus={() => setActiveField('system')}
                  isActive={activeField === 'system'}
                  placeholder="Enter system instructions..."
                  validVariables={availableVariables}
                />
              </div>
              
              {/* User Prompt */}
              <div className="flex-1 flex flex-col">
                <label className="text-sm font-medium block mb-1" style={{ color: 'var(--text-secondary)' }}>
                  Prompt
                </label>
                <HighlightedTextarea
                  ref={userPromptRef}
                  value={data.prompt || ''}
                  onChange={(e) => onChange({ ...data, prompt: e.target.value })}
                  onFocus={() => setActiveField('prompt')}
                  isActive={activeField === 'prompt'}
                  placeholder="Enter the LLM prompt. Use {variable} for state references..."
                  validVariables={availableVariables}
                />
              </div>
            </div>
          </div>
        )}
        
        {activeTab === 'output' && (
          <div className="flex gap-8 h-full">
            {/* Left column - Output Model */}
            <div className="flex-1">
              <div className="flex items-center justify-between mb-3">
                <div>
                  <label className="text-sm font-medium" style={{ color: 'var(--text-secondary)' }}>
                    Output Model
                  </label>
                  <p className="text-xs mt-0.5" style={{ color: 'var(--text-muted)' }}>
                    Variables saved to state
                  </p>
                </div>
                <button
                  onClick={() => {
                    const newKey = `field_${Object.keys(data.output_model || {}).length + 1}`
                    onChange({ ...data, output_model: { ...(data.output_model || {}), [newKey]: 'str' } })
                  }}
                  className="flex items-center gap-1 px-2.5 py-1 text-xs rounded bg-primary hover:bg-primary/90 text-white transition-colors"
                >
                  <Plus size={12} />
                  Add
                </button>
              </div>
              <div className="space-y-6">
                <OutputModelEditor
                  value={data.output_model}
                  onChange={(newModel) => onChange({ ...data, output_model: newModel })}
                  theme={theme}
                  hideLabel={true}
                />
                

              </div>
            </div>
            
            <div className="w-px self-stretch bg-[var(--border-color)] mx-2"></div>

            {/* Right column - User Message */}
            <div className="flex-1">
              <div className="flex items-center justify-between mb-3">
                <div>
                  <label className="text-sm font-medium" style={{ color: 'var(--text-secondary)' }}>
                    User Message
                  </label>
                  <p className="text-xs mt-0.5" style={{ color: 'var(--text-muted)' }}>
                    Display to user
                  </p>
                </div>
                <button
                  onClick={() => {
                    const newItems = [...(data.user_message || []), '']
                    onChange({ ...data, user_message: newItems })
                  }}
                  className="flex items-center gap-1 px-2.5 py-1 text-xs rounded bg-primary hover:bg-primary/90 text-white transition-colors"
                >
                  <Plus size={12} />
                  Add
                </button>
              </div>
              
              <div className="space-y-2">
                {(data.user_message || []).map((item: string, idx: number) => (
                  <div key={idx} className="flex items-center gap-2">
                    <input
                      type="text"
                      value={item}
                      onChange={(e) => {
                        const newItems = [...(data.user_message || [])]
                        newItems[idx] = e.target.value
                        onChange({ ...data, user_message: newItems })
                      }}
                      className="flex-1 px-3 py-1.5 rounded border font-mono text-sm"
                      style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }}
                      placeholder="Variable name..."
                    />
                    <button
                      onClick={() => {
                        const newItems = (data.user_message || []).filter((_: string, i: number) => i !== idx)
                        onChange({ ...data, user_message: newItems.length > 0 ? newItems : undefined })
                      }}
                      className="p-1.5 text-red-400 hover:text-red-300 hover:bg-red-500/20 rounded"
                    >
                      <Trash2 size={14} />
                    </button>
                  </div>
                ))}
                {(!data.user_message || data.user_message.length === 0) && (
                  <p className="text-xs italic py-2" style={{ color: 'var(--text-muted)' }}>
                    No items. Add output_model variables to show results.
                  </p>
                )}
              </div>
            </div>
          </div>
        )}
        
        {activeTab === 'tools' && (
          <div className="space-y-6 overflow-y-auto max-h-[calc(100vh-300px)]">
            {/* Row 1: Enable Tools */}
            <div className="flex items-center gap-2">
              <label className="text-sm font-medium" style={{ color: 'var(--text-secondary)' }}>
                Enable Tools
              </label>
              <input
                type="checkbox"
                checked={data.tools === true}
                onChange={(e) => onChange({ ...data, tools: e.target.checked || undefined })}
                className="w-4 h-4 accent-primary"
              />
            </div>

            {data.tools && (
              <div className="space-y-6 animate-in fade-in slide-in-from-top-2 duration-200">
                {/* Row 2: Tool Selection + Auto Approve */}
                <div className="flex gap-6">
                  <div className="flex-1 max-w-2xl">
                    <label className="text-sm font-medium block mb-1" style={{ color: 'var(--text-secondary)' }}>
                      Tools Selection
                    </label>
                    <ToolSelector
                      availableTools={availableTools}
                      selectedTools={currentTools}
                      onAddTool={handleAddTool}
                      onRemoveTool={handleRemoveTool}
                      placeholder="Select tools..."
                    />
                    {currentTools.some(t => !toolNames.includes(t)) && (
                      <div className="mt-2 p-2 rounded border border-red-500 bg-red-500/10">
                        <div className="flex items-center gap-2 text-xs text-red-400">
                          <AlertCircle size={12} />
                          <span>Some selected tools are not available</span>
                        </div>
                      </div>
                    )}
                  </div>

                  <div className="w-40 pt-6"> {/* pt-6 aligns checkbox with input field */}
                    <div className="flex items-center gap-2">
                      <label className="text-sm font-medium whitespace-nowrap" style={{ color: 'var(--text-secondary)' }}>
                        Auto-approve
                      </label>
                      <input
                        type="checkbox"
                        checked={data.tools_auto_approval === true}
                        onChange={(e) => onChange({ ...data, tools_auto_approval: e.target.checked || undefined })}
                        className="w-4 h-4 accent-primary"
                      />
                    </div>
                  </div>
                </div>

                {/* Row 3: Raw Tool Output */}
                <div className="max-w-2xl">
                  <RawToolOutputEditor 
                    value={data.raw_tool_output}
                    onChange={(val) => onChange({ ...data, raw_tool_output: val })}
                  />
                </div>
              </div>
            )}
          </div>
        )}
        
        {activeTab === 'advanced' && (
          <div className="flex gap-6 flex-wrap">
            <div className="w-40">
              <label className="text-sm font-medium block mb-1" style={{ color: 'var(--text-secondary)' }}>
                Output Action
              </label>
              <select
                value={data.output_action || 'overwrite'}
                onChange={(e) => onChange({ ...data, output_action: e.target.value === 'overwrite' ? undefined : e.target.value })}
                className="w-full px-3 py-2 rounded border"
                style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }}
              >
                <option value="overwrite">overwrite</option>
                <option value="append">append</option>
              </select>
            </div>
            
            <div className="w-32">
              <label className="text-sm font-medium block mb-1" style={{ color: 'var(--text-secondary)' }}>
                Max Retries
              </label>
              <input
                type="number"
                value={data.max_retries || ''}
                onChange={(e) => onChange({ ...data, max_retries: e.target.value ? parseInt(e.target.value) : undefined })}
                className="w-full px-3 py-2 rounded border"
                style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }}
                placeholder="3"
                min={0}
              />
            </div>
            
            {/* Silent Mode Toggle */}
            <div className="flex items-center gap-2 pt-6">
              <input
                type="checkbox"
                id="llm-silent"
                checked={data.silent || false}
                onChange={(e) => onChange({ ...data, silent: e.target.checked })}
                className="w-4 h-4 rounded border accent-primary"
              />
              <label htmlFor="llm-silent" className="text-sm" style={{ color: 'var(--text-secondary)' }}>
                Silent mode
              </label>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
