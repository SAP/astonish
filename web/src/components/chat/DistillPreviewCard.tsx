import { useState, useMemo } from 'react'
import { Save, RefreshCw, X, Tag, ChevronDown, ChevronUp, Workflow, TerminalSquare, Lightbulb } from 'lucide-react'
import FlowPreview from '../FlowPreview'
import type { DistillPreviewMessage } from './chatTypes'

interface DistillPreviewCardProps {
  data: DistillPreviewMessage
  isActive?: boolean
  /** When true, use full parent width (harness panel) instead of max-w-2xl. */
  fillWidth?: boolean
  onSave: () => void
  onRequestChanges: () => void
  onCancel: () => void
}

// Parsed explanation structure
interface ExplanationData {
  summary: string
  nodes: { name: string; type: string; description: string }[]
  params: { name: string; description: string }[]
  notes: string
}

// Parse the structured explanation markdown into typed sections
function parseExplanation(text: string): ExplanationData {
  const result: ExplanationData = { summary: '', nodes: [], params: [], notes: '' }
  if (!text) return result

  // Split by ## headers
  const sections: Record<string, string> = {}
  let currentKey = '_preamble'
  const lines = text.split('\n')

  for (const line of lines) {
    const headerMatch = line.match(/^##\s+(.+)/)
    if (headerMatch) {
      currentKey = headerMatch[1].trim().toLowerCase()
      sections[currentKey] = ''
    } else {
      sections[currentKey] = (sections[currentKey] || '') + line + '\n'
    }
  }

  // Summary
  result.summary = (sections['summary'] || '').trim()

  // Nodes: parse "- **node_name** (type): description"
  const nodesText = sections['nodes'] || ''
  const nodeLines = nodesText.split('\n').filter(l => l.trim().startsWith('-'))
  for (const nl of nodeLines) {
    const match = nl.match(/^-\s+\*\*(.+?)\*\*\s*\((.+?)\)\s*:\s*(.+)/)
    if (match) {
      result.nodes.push({ name: match[1], type: match[2].trim(), description: match[3].trim() })
    } else {
      // Fallback: just bold name + description
      const fallback = nl.match(/^-\s+\*\*(.+?)\*\*\s*:?\s*(.*)/)
      if (fallback) {
        result.nodes.push({ name: fallback[1], type: '', description: fallback[2].trim() })
      }
    }
  }

  // Input Parameters: parse "- **param_name**: description"
  const paramsText = sections['input parameters'] || ''
  const paramLines = paramsText.split('\n').filter(l => l.trim().startsWith('-'))
  for (const pl of paramLines) {
    const match = pl.match(/^-\s+\*\*(.+?)\*\*\s*:?\s*(.+)/)
    if (match) {
      result.params.push({ name: match[1], description: match[2].trim() })
    }
  }

  // Notes
  result.notes = (sections['notes'] || '').trim()

  return result
}

// Color map for node types
const nodeTypeColors: Record<string, { bg: string; text: string; border: string }> = {
  agent: { bg: 'var(--accent-soft)', text: 'var(--accent)', border: 'var(--accent)' },
  input: { bg: 'rgba(59, 130, 246, 0.1)', text: 'rgb(59, 130, 246)', border: 'rgb(59, 130, 246)' },
  output: { bg: 'rgba(16, 185, 129, 0.1)', text: 'rgb(16, 185, 129)', border: 'rgb(16, 185, 129)' },
  conditional: { bg: 'rgba(245, 158, 11, 0.1)', text: 'rgb(245, 158, 11)', border: 'rgb(245, 158, 11)' },
  loop: { bg: 'rgba(236, 72, 153, 0.1)', text: 'rgb(236, 72, 153)', border: 'rgb(236, 72, 153)' },
}

function getTypeStyle(type: string) {
  const lower = type.toLowerCase()
  return nodeTypeColors[lower] || { bg: 'var(--surface-muted)', text: 'var(--text-secondary)', border: 'var(--border-color)' }
}

export default function DistillPreviewCard({ data, isActive = false, fillWidth = false, onSave, onRequestChanges, onCancel }: DistillPreviewCardProps) {
  const [showYaml, setShowYaml] = useState(false)
  const [explanationExpanded, setExplanationExpanded] = useState(true)

  const explanation = useMemo(() => parseExplanation(data.explanation), [data.explanation])
  const hasExplanation = data.explanation && (explanation.summary || explanation.nodes.length > 0)

  return (
    <div
      className={fillWidth ? 'rounded-xl overflow-hidden w-full' : 'my-3 rounded-xl overflow-hidden w-full max-w-2xl'}
      style={{
        border: '1px solid var(--border-color)',
        background: 'var(--bg-secondary)',
        boxShadow: 'var(--shadow-soft)',
      }}
    >
      {/* Header */}
      <div className="px-4 py-3 flex items-center justify-between" style={{ borderBottom: '1px solid var(--border-color)' }}>
        <div className="flex flex-col gap-0.5">
          <div className="flex items-center gap-2">
            <span className="text-sm font-semibold" style={{ color: 'var(--brand)' }}>{data.flowName || 'Distilled Flow'}</span>
          </div>
          <span className="text-xs" style={{ color: 'var(--text-secondary)' }}>{data.description}</span>
        </div>
        {data.tags && data.tags.length > 0 && (
          <div className="flex items-center gap-1 flex-shrink-0">
            <Tag size={11} style={{ color: 'var(--brand)' }} />
            {data.tags.map((tag, i) => (
              <span key={i} className="text-[10px] px-1.5 py-0.5 rounded" style={{ background: 'var(--accent-soft)', color: 'var(--brand)' }}>{tag}</span>
            ))}
          </div>
        )}
      </div>

      {/* Flow Canvas Preview */}
      <div className="px-3 py-2">
        <FlowPreview yamlContent={data.yaml} height={300} />
      </div>

      {/* Explanation */}
      {hasExplanation && (
        <div style={{ borderTop: '1px solid var(--border-color)' }}>
          <button
            onClick={() => setExplanationExpanded(!explanationExpanded)}
            className="flex items-center gap-1.5 w-full px-4 py-2.5 text-xs transition-colors cursor-pointer"
            style={{ color: 'var(--brand)' }}
          >
            {explanationExpanded ? <ChevronUp size={12} /> : <ChevronDown size={12} />}
            <span className="font-medium">Explanation</span>
          </button>

          {explanationExpanded && (
            <div className="px-4 pb-3 space-y-3">
              {/* Summary */}
              {explanation.summary && (
                <p className="text-xs leading-relaxed" style={{ color: 'var(--text-primary)' }}>
                  {explanation.summary}
                </p>
              )}

              {/* Nodes */}
              {explanation.nodes.length > 0 && (
                <div>
                  <div className="flex items-center gap-1.5 mb-2">
                    <Workflow size={12} style={{ color: 'var(--text-muted)' }} />
                    <span className="text-[11px] font-semibold uppercase tracking-wide" style={{ color: 'var(--text-muted)' }}>Nodes</span>
                  </div>
                  <div
                    className="rounded-lg overflow-hidden"
                    style={{
                      display: 'grid',
                      gridTemplateColumns: 'auto auto 1fr',
                      background: 'var(--bg-tertiary)',
                    }}
                  >
                    {explanation.nodes.map((node, i) => {
                      const typeStyle = getTypeStyle(node.type)
                      return (
                        <div key={i} className="contents">
                          <code
                            className="text-[11px] font-semibold px-1.5 py-1.5 whitespace-nowrap"
                            style={{
                              background: typeStyle.bg,
                              color: typeStyle.text,
                              borderTop: i > 0 ? '1px solid var(--border-color)' : undefined,
                              paddingLeft: '10px',
                            }}
                          >
                            {node.name}
                          </code>
                          <span
                            className="text-[10px] px-2 py-1.5 whitespace-nowrap"
                            style={{
                              color: typeStyle.text,
                              opacity: 0.8,
                              borderTop: i > 0 ? '1px solid var(--border-color)' : undefined,
                            }}
                          >
                            {node.type || '\u00A0'}
                          </span>
                          <span
                            className="text-[11px] leading-relaxed py-1.5 pr-2.5"
                            style={{
                              color: 'var(--text-secondary)',
                              borderTop: i > 0 ? '1px solid var(--border-color)' : undefined,
                            }}
                          >
                            {node.description}
                          </span>
                        </div>
                      )
                    })}
                  </div>
                </div>
              )}

              {/* Input Parameters */}
              {explanation.params.length > 0 && (
                <div>
                  <div className="flex items-center gap-1.5 mb-2">
                    <TerminalSquare size={12} style={{ color: 'var(--text-muted)' }} />
                    <span className="text-[11px] font-semibold uppercase tracking-wide" style={{ color: 'var(--text-muted)' }}>Input Parameters</span>
                  </div>
                  <div
                    className="rounded-lg overflow-hidden"
                    style={{
                      display: 'grid',
                      gridTemplateColumns: 'auto 1fr',
                      background: 'var(--bg-tertiary)',
                    }}
                  >
                    {explanation.params.map((param, i) => (
                      <div key={i} className="contents">
                        <code
                          className="text-[11px] font-semibold px-2.5 py-1.5 whitespace-nowrap"
                          style={{
                            background: 'rgba(59, 130, 246, 0.1)',
                            color: 'rgb(59, 130, 246)',
                            borderTop: i > 0 ? '1px solid var(--border-color)' : undefined,
                          }}
                        >
                          {param.name}
                        </code>
                        <span
                          className="text-[11px] leading-relaxed px-2.5 py-1.5"
                          style={{
                            color: 'var(--text-secondary)',
                            borderTop: i > 0 ? '1px solid var(--border-color)' : undefined,
                          }}
                        >
                          {param.description}
                        </span>
                      </div>
                    ))}
                  </div>
                </div>
              )}

              {/* Notes */}
              {explanation.notes && (
                <div className="flex items-start gap-2 rounded-lg px-3 py-2" style={{ background: 'var(--accent-soft)' }}>
                  <Lightbulb size={12} className="flex-shrink-0 mt-0.5" style={{ color: 'var(--brand)' }} />
                  <p className="text-[11px] leading-relaxed" style={{ color: 'var(--text-secondary)' }}>
                    {explanation.notes}
                  </p>
                </div>
              )}
            </div>
          )}
        </div>
      )}

      {/* YAML toggle */}
      <div style={{ borderTop: '1px solid var(--border-color)' }}>
        <button
          onClick={() => setShowYaml(!showYaml)}
          className="flex items-center gap-1.5 w-full px-4 py-2.5 text-xs transition-colors cursor-pointer"
          style={{ color: 'var(--brand)' }}
        >
          {showYaml ? <ChevronUp size={12} /> : <ChevronDown size={12} />}
          <span className="font-medium">View YAML</span>
        </button>
        {showYaml && (
          <div className="px-4 pb-3">
            <pre className="p-3 rounded-lg text-[11px] overflow-x-auto max-h-64 overflow-y-auto" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}>
              <code>{data.yaml}</code>
            </pre>
          </div>
        )}
      </div>

      {/* Actions — only show when this is the active review */}
      {isActive && (
        <>
          <div className="px-4 py-3 flex items-center gap-2" style={{ borderTop: '1px solid var(--border-color)' }}>
            <button
              onClick={onSave}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium text-white transition-colors cursor-pointer"
              style={{ background: 'var(--brand)', border: '1px solid var(--accent-strong)' }}
            >
              <Save size={13} />
              Save Flow
            </button>
            <button
              onClick={onRequestChanges}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-colors cursor-pointer"
              style={{ background: 'var(--accent-soft)', border: '1px solid var(--border-color)', color: 'var(--brand)' }}
            >
              <RefreshCw size={13} />
              Request Changes
            </button>
            <button
              onClick={onCancel}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-colors cursor-pointer ml-auto"
              style={{ background: 'var(--surface-muted)', border: '1px solid var(--border-color)', color: 'var(--text-muted)' }}
            >
              <X size={13} />
              Cancel
            </button>
          </div>

          {/* Help text */}
          <div className="px-4 pb-3">
            <p className="text-[10px]" style={{ color: 'var(--text-muted)' }}>
              Type your changes in the chat, say &quot;test it&quot; to run a verification test, or click &quot;Save Flow&quot; when you&apos;re satisfied.
            </p>
          </div>
        </>
      )}
    </div>
  )
}
