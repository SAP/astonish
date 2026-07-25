import { useState, useEffect, useMemo, useRef } from 'react'
import { X, Plus, Trash2, GitBranch, Code, Eye, AlertCircle } from 'lucide-react'
import { generateLambda, parseLambda, createEmptyRule, OPERATORS, LOGIC_OPERATORS } from '../utils/conditionGenerator'
import type { ConditionRule } from '../utils/conditionGenerator'

interface VariableGroup {
  variables?: string[]
}

interface EdgeData {
  condition?: string
  label?: string
}

interface EdgeItem {
  id: string
  source: string
  target: string
  data?: EdgeData
}

interface NodeItem {
  data?: { label?: string }
}

interface EdgeEditorProps {
  edge: EdgeItem
  sourceNode?: NodeItem | null
  targetNode?: NodeItem | null
  onSave: (edgeId: string, data: { condition: string }) => void
  onDelete?: (edgeId: string) => void
  onClose: () => void
  theme?: string
  availableVariables?: VariableGroup[]
  readOnly?: boolean
}

/**
 * EdgeEditor - Visual editor for edge conditions (branching logic)
 */
export default function EdgeEditor({
  edge,
  sourceNode,
  targetNode,
  onSave,
  onDelete,
  onClose,
  theme,
  availableVariables = [],
  readOnly = false
}: EdgeEditorProps) {
  // Mode: 'visual' or 'advanced'
  const [mode, setMode] = useState('visual')
  
  // Visual builder state
  const [rules, setRules] = useState<ConditionRule[]>([createEmptyRule()])
  const [logic, setLogic] = useState('and')
  
  // Advanced mode state
  const [rawCondition, setRawCondition] = useState('')
  
  // Track if the current condition is too complex for Visual mode
  const [isUnparseable, setIsUnparseable] = useState(false)
  
  // Track if initial load has completed (to prevent auto-save on mount)
  const hasInitialized = useRef(false)
  
  // Parse existing condition on mount
  useEffect(() => {
    hasInitialized.current = false // Reset on edge change
    const existingCondition = edge?.data?.condition || ''
    
    // Always set rawCondition first to preserve it
    setRawCondition(existingCondition)
    
    if (existingCondition) {
      const parsed = parseLambda(existingCondition)
      if (parsed && parsed.rules.length > 0) {
        setRules(parsed.rules)
        setLogic(parsed.logic)
        setIsUnparseable(false)
        setMode('visual')
      } else {
        // Unparseable - use advanced mode
        setRules([createEmptyRule()])
        setIsUnparseable(true)
        setMode('advanced')
      }
    } else {
      setRules([createEmptyRule()])
      setLogic('and')
      setIsUnparseable(false)
      setMode('visual')
    }
    
    // Mark as initialized after a short delay to let state settle
    setTimeout(() => {
      hasInitialized.current = true
    }, 100)
  }, [edge])
  
  // Flatten available variables into a simple list
  const flatVariables = useMemo(() => {
    const vars: string[] = []
    availableVariables.forEach(group => {
      if (group.variables) {
        group.variables.forEach(v => {
          if (!vars.includes(v)) {
            vars.push(v)
          }
        })
      }
    })
    return vars.sort()
  }, [availableVariables])
  
  // Generate preview
  const preview = useMemo(() => {
    if (mode === 'advanced') {
      return rawCondition
    }
    return generateLambda(rules, logic)
  }, [mode, rules, logic, rawCondition])
  
  // Check if condition is valid
  const _isValid = useMemo(() => {
    if (mode === 'advanced') {
      return rawCondition.trim().length > 0
    }
    // Visual mode: at least one complete rule
    return rules.some(r => r.variable && r.operator && r.value !== '')
  }, [mode, rules, rawCondition])
  
  // Handlers
  const handleAddRule = () => {
    setRules([...rules, createEmptyRule()])
  }
  
  const handleRemoveRule = (index: number) => {
    if (rules.length > 1) {
      setRules(rules.filter((_, i) => i !== index))
    }
  }
  
  const handleRuleChange = (index: number, field: keyof ConditionRule, value: string) => {
    const updated = [...rules]
    updated[index] = { ...updated[index], [field]: value }
    setRules(updated)
  }
  
  // Auto-save is DISABLED - only save on close to prevent race conditions
  // The user must click Done to save changes
  
  const handleClose = () => {
    // Final save before closing
    // If condition is unparseable OR we're in advanced mode, use rawCondition
    // Otherwise generate from visual rules
    let condition
    if (isUnparseable || mode === 'advanced') {
      condition = rawCondition
    } else {
      condition = generateLambda(rules, logic)
    }
    onSave(edge.id, { condition })
    onClose()
  }
  
  const handleClearCondition = () => {
    setRules([createEmptyRule()])
    setRawCondition('')
    onSave(edge.id, { condition: '' })
  }
  
  return (
    <div 
      className="h-full flex flex-col overflow-hidden"
      style={{ background: 'var(--bg-secondary)' }}
    >
      {/* Header */}
      <div 
        className="flex items-center justify-between px-4 py-3 border-b"
        style={{ borderColor: 'var(--border-color)' }}
      >
        <div className="flex items-center gap-3">
          <div 
            className="w-8 h-8 rounded-lg flex items-center justify-center"
            style={{ background: 'linear-gradient(135deg, var(--brand) 0%, var(--brand-strong) 100%)' }}
          >
            <GitBranch size={16} className="text-white" />
          </div>
          <div>
            <h3 className="font-semibold" style={{ color: 'var(--text-primary)' }}>
              Edit Connection
            </h3>
            <p className="text-xs" style={{ color: 'var(--text-muted)' }}>
              {sourceNode?.data?.label || edge?.source} → {targetNode?.data?.label || edge?.target}
            </p>
          </div>
        </div>
        
        <div className="flex items-center gap-2">
          {/* Mode Toggle */}
          <div 
            className="flex rounded-lg p-0.5"
            style={{ background: 'var(--bg-primary)' }}
          >
            <button
              onClick={() => {
                // When switching from Advanced to Visual, try to parse the rawCondition
                if (mode === 'advanced' && rawCondition) {
                  const parsed = parseLambda(rawCondition)
                  if (parsed && parsed.rules.length > 0) {
                    setRules(parsed.rules)
                    setLogic(parsed.logic)
                    setIsUnparseable(false)
                  } else {
                    // Can't parse - mark as unparseable but still switch to Visual to show message
                    setIsUnparseable(true)
                  }
                }
                setMode('visual')
              }}
              className={`px-3 py-1.5 rounded-md text-xs font-medium transition-colors ${
                mode === 'visual' ? 'bg-primary text-white' : ''
              }`}
              style={mode !== 'visual' ? { color: 'var(--text-muted)' } : {}}
              disabled={readOnly}
            >
              Visual
            </button>
            <button
              onClick={() => {
                // Only sync rawCondition from visual rules if not working with an unparseable condition
                if (mode === 'visual' && !isUnparseable) {
                  const generated = generateLambda(rules, logic)
                  if (generated) {
                    setRawCondition(generated)
                  }
                  // If generated is empty and rawCondition has content, keep rawCondition
                }
                setMode('advanced')
              }}
              className={`px-3 py-1.5 rounded-md text-xs font-medium transition-colors ${
                mode === 'advanced' ? 'bg-primary text-white' : ''
              }`}
              style={mode !== 'advanced' ? { color: 'var(--text-muted)' } : {}}
              disabled={readOnly}
            >
              Advanced
            </button>
          </div>
          
          {/* Actions */}
          <div className="flex items-center gap-2">
            {!readOnly && preview && (
              <button
                onClick={handleClearCondition}
                className="px-3 py-1.5 rounded text-sm font-medium transition-colors hover:bg-red-500/20 text-red-400"
              >
                Clear
              </button>
            )}
            <button
              onClick={handleClose}
              className="px-3 py-1.5 rounded text-sm font-medium text-white bg-primary hover:bg-primary/90 transition-colors"
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
      </div>
      
      {/* Content */}
      <div className="flex-1 overflow-y-auto p-4">
        {mode === 'visual' ? (
          <div className="space-y-4">
            {/* Unparseable condition fallback */}
            {isUnparseable && rawCondition ? (
              <div 
                className="p-4 rounded-lg border"
                style={{ 
                  background: 'var(--bg-primary)', 
                  borderColor: 'var(--border-color)' 
                }}
              >
                <div className="flex items-start gap-3">
                  <AlertCircle size={18} className="text-amber-500 flex-shrink-0 mt-0.5" />
                  <div className="flex-1">
                    <p className="text-sm font-medium mb-2" style={{ color: 'var(--text-primary)' }}>
                      Complex Condition
                    </p>
                    <p className="text-xs mb-3" style={{ color: 'var(--text-muted)' }}>
                      This condition uses advanced logic that can only be edited in Advanced mode.
                    </p>
                    <code 
                      className="text-xs font-mono block p-2 rounded overflow-x-auto"
                      style={{ background: 'var(--bg-secondary)', color: 'var(--brand)' }}
                    >
                      {rawCondition}
                    </code>
                  </div>
                </div>
              </div>
            ) : (
              <>
                {/* Logic selector (only show if multiple rules) */}
                {rules.length > 1 && (
                  <div className="flex items-center gap-2 mb-4">
                    <span className="text-sm" style={{ color: 'var(--text-muted)' }}>
                      Match
                    </span>
                    <select
                      value={logic}
                      onChange={(e) => setLogic(e.target.value)}
                      className="px-3 py-1.5 rounded-lg border text-sm"
                      style={{ 
                        background: 'var(--bg-primary)', 
                        borderColor: 'var(--border-color)', 
                        color: 'var(--text-primary)' 
                      }}
                      disabled={readOnly}
                    >
                      {LOGIC_OPERATORS.map(op => (
                        <option key={op.value} value={op.value}>{op.label} of the following</option>
                      ))}
                    </select>
                  </div>
                )}
            
            {/* Rules */}
            <div className="space-y-3">
              {rules.map((rule, index) => (
                <div 
                  key={index}
                  className="flex items-center gap-2 p-3 rounded-lg border"
                  style={{ 
                    background: 'var(--bg-primary)', 
                    borderColor: 'var(--border-color)' 
                  }}
                >
                  {/* Variable */}
                  <select
                    value={rule.variable}
                    onChange={(e) => handleRuleChange(index, 'variable', e.target.value)}
                    className="flex-1 px-3 py-2 rounded-lg border text-sm"
                    style={{ 
                      background: 'var(--bg-secondary)', 
                      borderColor: 'var(--border-color)', 
                      color: 'var(--text-primary)' 
                    }}
                    disabled={readOnly}
                  >
                    <option value="">Select variable...</option>
                    {flatVariables.map(v => (
                      <option key={v} value={v}>{v}</option>
                    ))}
                  </select>
                  
                  {/* Operator */}
                  <select
                    value={rule.operator}
                    onChange={(e) => handleRuleChange(index, 'operator', e.target.value)}
                    className="w-40 px-3 py-2 rounded-lg border text-sm"
                    style={{ 
                      background: 'var(--bg-secondary)', 
                      borderColor: 'var(--border-color)', 
                      color: 'var(--text-primary)' 
                    }}
                    disabled={readOnly}
                  >
                    {OPERATORS.map(op => (
                      <option key={op.value} value={op.value}>{op.label}</option>
                    ))}
                  </select>
                  
                  {/* Value */}
                  <input
                    type="text"
                    value={rule.value}
                    onChange={(e) => handleRuleChange(index, 'value', e.target.value)}
                    placeholder="Value"
                    className="flex-1 px-3 py-2 rounded-lg border text-sm"
                    style={{ 
                      background: 'var(--bg-secondary)', 
                      borderColor: 'var(--border-color)', 
                      color: 'var(--text-primary)' 
                    }}
                    disabled={readOnly}
                  />
                  
                  {/* Remove button */}
                  {rules.length > 1 && !readOnly && (
                    <button
                      onClick={() => handleRemoveRule(index)}
                      className="p-2 rounded-lg transition-colors hover:bg-red-500/20 text-red-400"
                    >
                      <Trash2 size={16} />
                    </button>
                  )}
                </div>
              ))}
            </div>
            
            {/* Add Rule button */}
            {!readOnly && (
              <button
                onClick={handleAddRule}
                className="flex items-center gap-2 px-4 py-2 rounded-lg border border-dashed transition-colors hover:border-primary/50 hover:bg-primary/5"
                style={{ 
                  borderColor: 'var(--border-color)', 
                  color: 'var(--text-muted)' 
                }}
              >
                <Plus size={16} />
                <span className="text-sm">Add condition</span>
              </button>
            )}
              </>
            )}
          </div>
        ) : (
          /* Advanced Mode */
          <div className="space-y-3">
            <div className="flex items-center gap-2 text-sm" style={{ color: 'var(--text-muted)' }}>
              <Code size={14} />
              <span>Python lambda expression</span>
            </div>
            <textarea
              value={rawCondition}
              onChange={(e) => setRawCondition(e.target.value)}
              placeholder="lambda x: x.get('variable') == 'value'"
              className="w-full h-24 px-3 py-2 rounded-lg border text-sm font-mono resize-none"
              style={{ 
                background: 'var(--bg-primary)', 
                borderColor: 'var(--border-color)', 
                color: 'var(--text-primary)' 
              }}
              disabled={readOnly}
            />
            <div className="flex items-start gap-2 text-xs p-2 rounded-lg" style={{ background: 'var(--bg-primary)', color: 'var(--text-muted)' }}>
              <AlertCircle size={12} className="mt-0.5 flex-shrink-0" />
              <div>
                <strong>Tip:</strong> The variable <code className="px-1 py-0.5 rounded" style={{ background: 'var(--bg-secondary)' }}>x</code> contains the current flow state.
                Access values using <code className="px-1 py-0.5 rounded" style={{ background: 'var(--bg-secondary)' }}>x.get('variable_name')</code>.
              </div>
            </div>
          </div>
        )}
        
        {/* Preview */}
        {preview && (
          <div className="mt-6 p-3 rounded-lg border" style={{ 
            background: 'var(--bg-primary)', 
            borderColor: 'var(--border-color)' 
          }}>
            <div className="flex items-center gap-2 mb-2 text-xs" style={{ color: 'var(--text-muted)' }}>
              <Eye size={12} />
              <span>Generated condition</span>
            </div>
            <code 
              className="text-xs font-mono block overflow-x-auto"
              style={{ color: 'var(--brand)' }}
            >
              {preview}
            </code>
          </div>
        )}
      </div>
    </div>
  )
}
