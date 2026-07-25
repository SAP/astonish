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

// Node type badges — solid text colors only (never var(--accent): it's a translucent fill).
const nodeTypeColors: Record<string, { bg: string; text: string; border: string }> = {
  agent: { bg: 'var(--brand-muted)', text: 'var(--brand)', border: 'color-mix(in oklab, var(--brand) 35%, transparent)' },
  llm: { bg: 'var(--brand-muted)', text: 'var(--brand)', border: 'color-mix(in oklab, var(--brand) 35%, transparent)' },
  input: { bg: 'rgba(37, 99, 235, 0.15)', text: '#60a5fa', border: 'rgba(37, 99, 235, 0.35)' },
  output: { bg: 'rgba(16, 185, 129, 0.15)', text: '#34d399', border: 'rgba(16, 185, 129, 0.35)' },
  tool: { bg: 'rgba(245, 158, 11, 0.15)', text: '#fbbf24', border: 'rgba(245, 158, 11, 0.35)' },
  conditional: { bg: 'rgba(245, 158, 11, 0.15)', text: '#fbbf24', border: 'rgba(245, 158, 11, 0.35)' },
  loop: { bg: 'rgba(236, 72, 153, 0.15)', text: '#f472b6', border: 'rgba(236, 72, 153, 0.35)' },
}

function getTypeStyle(type: string) {
  const lower = type.toLowerCase()
  return nodeTypeColors[lower] || {
    bg: 'var(--bg-tertiary)',
    text: 'var(--text-primary)',
    border: 'var(--border-color)',
  }
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
      {/* Header — titles use text-primary for contrast; brand only for accents */}
      <div className="px-4 py-3 flex items-center justify-between gap-3" style={{ borderBottom: '1px solid var(--border-color)' }}>
        <div className="flex flex-col gap-0.5 min-w-0">
          <div className="flex items-center gap-2 min-w-0">
            <span className="text-sm font-semibold truncate" style={{ color: 'var(--text-primary)' }}>{data.flowName || 'Distilled Flow'}</span>
          </div>
          {data.description && (
            <span className="text-xs" style={{ color: 'var(--text-secondary)' }}>{data.description}</span>
          )}
        </div>
        {data.tags && data.tags.length > 0 && (
          <div className="flex items-center gap-1 flex-shrink-0 flex-wrap justify-end">
            <Tag size={11} style={{ color: 'var(--text-muted)' }} />
            {data.tags.map((tag, i) => (
              <span
                key={i}
                className="text-[10px] px-1.5 py-0.5 rounded"
                style={{
                  background: 'var(--bg-tertiary)',
                  color: 'var(--text-secondary)',
                  border: '1px solid var(--border-color)',
                }}
              >
                {tag}
              </span>
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
            className="flex items-center gap-1.5 w-full px-4 py-2.5 text-xs transition-colors cursor-pointer hover:opacity-90"
            style={{ color: 'var(--text-primary)' }}
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
                    <Workflow size={12} style={{ color: 'var(--text-secondary)' }} />
                    <span className="text-[11px] font-semibold uppercase tracking-wide" style={{ color: 'var(--text-secondary)' }}>Nodes</span>
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
                              color: 'var(--text-muted)',
                              borderTop: i > 0 ? '1px solid var(--border-color)' : undefined,
                            }}
                          >
                            {node.type || '\u00A0'}
                          </span>
                          <span
                            className="text-[11px] leading-relaxed py-1.5 pr-2.5"
                            style={{
                              color: 'var(--text-primary)',
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
                    <TerminalSquare size={12} style={{ color: 'var(--text-secondary)' }} />
                    <span className="text-[11px] font-semibold uppercase tracking-wide" style={{ color: 'var(--text-secondary)' }}>Input Parameters</span>
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
                            background: 'var(--brand-muted)',
                            color: 'var(--brand)',
                            borderTop: i > 0 ? '1px solid var(--border-color)' : undefined,
                          }}
                        >
                          {param.name}
                        </code>
                        <span
                          className="text-[11px] leading-relaxed px-2.5 py-1.5"
                          style={{
                            color: 'var(--text-primary)',
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
                  <p className="text-[11px] leading-relaxed" style={{ color: 'var(--text-primary)' }}>
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
          className="flex items-center gap-1.5 w-full px-4 py-2.5 text-xs transition-colors cursor-pointer hover:opacity-90"
          style={{ color: 'var(--text-primary)' }}
        >
          {showYaml ? <ChevronUp size={12} /> : <ChevronDown size={12} />}
          <span className="font-medium">View YAML</span>
        </button>
        {showYaml && (
          <div className="px-4 pb-3">
            <pre className="p-3 rounded-lg text-[11px] overflow-x-auto max-h-64 overflow-y-auto" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }}>
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
              style={{ background: 'var(--bg-tertiary)', border: '1px solid var(--border-color)', color: 'var(--text-secondary)' }}
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
