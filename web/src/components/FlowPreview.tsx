import { useEffect, useState, useMemo, useCallback } from 'react'
import { createPortal } from 'react-dom'
import { ReactFlow, ReactFlowProvider, Background, Controls } from '@xyflow/react'
import type { Node, Edge } from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { Maximize2, Minimize2 } from 'lucide-react'
import * as yaml from 'js-yaml'

import StartNode from './nodes/StartNode'
import EndNode from './nodes/EndNode'
import InputNode from './nodes/InputNode'
import LlmNode from './nodes/LlmNode'
import ToolNode from './nodes/ToolNode'
import OutputNode from './nodes/OutputNode'
import UpdateStateNode from './nodes/UpdateStateNode'
import { yamlToFlowAsync } from '../utils/yamlToFlow'

interface FlowPreviewProps {
  yamlContent: string
  height?: number
}

const nodeTypes = {
  start: StartNode,
  end: EndNode,
  input: InputNode,
  llm: LlmNode,
  tool: ToolNode,
  output: OutputNode,
  updateState: UpdateStateNode,
}

const edgeStyle = {
  stroke: 'var(--brand)',
  strokeWidth: 2,
}

// Marks nodes that have outgoing edges so OverflowNode hides the "+" button.
// This mirrors what FlowCanvas does in its useEffect prop-sync.
function markOutgoingConnections(nodes: Node[], edges: Edge[]): Node[] {
  const sources = new Set(edges.map(e => e.source))
  return nodes.map(n => ({
    ...n,
    data: { ...n.data, hasOutgoingConnection: sources.has(n.id) },
  }))
}

// Fullscreen interactive view — gets its own ReactFlowProvider via portal
function FullscreenFlowView({ nodes, edges, onClose }: { nodes: Node[]; edges: Edge[]; onClose: () => void }) {
  const proOptions = useMemo(() => ({ hideAttribution: true }), [])

  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [onClose])

  return createPortal(
    <div
      style={{
        position: 'fixed',
        inset: 0,
        zIndex: 9999,
        background: 'var(--bg-primary)',
        display: 'flex',
        flexDirection: 'column',
      }}
    >
      {/* Toolbar */}
      <div className="flex items-center justify-between px-4 py-2" style={{ borderBottom: '1px solid var(--border-color)', background: 'var(--bg-secondary)' }}>
        <span className="text-sm font-medium" style={{ color: 'var(--brand)' }}>Flow Preview</span>
        <div className="flex items-center gap-3">
          <span className="text-[10px]" style={{ color: 'var(--text-muted)' }}>Scroll to zoom &middot; Drag to pan &middot; Esc to close</span>
          <button
            onClick={onClose}
            className="flex items-center gap-1.5 px-2.5 py-1 rounded-lg text-xs transition-colors cursor-pointer"
            style={{ background: 'var(--surface-muted)', border: '1px solid var(--border-color)', color: 'var(--text-secondary)' }}
          >
            <Minimize2 size={12} />
            Close
          </button>
        </div>
      </div>

      {/* Canvas — own ReactFlowProvider so it doesn't conflict with the inline one */}
      <div style={{ flex: 1 }}>
        <ReactFlowProvider>
          <ReactFlow
            nodes={nodes}
            edges={edges}
            nodeTypes={nodeTypes}
            fitView
            fitViewOptions={{ padding: 0.15 }}
            nodesDraggable={true}
            nodesConnectable={false}
            elementsSelectable={false}
            panOnDrag={true}
            zoomOnScroll={true}
            zoomOnPinch={true}
            zoomOnDoubleClick={true}
            preventScrolling={true}
            proOptions={proOptions}
            minZoom={0.05}
            maxZoom={4}
          >
            <Background color="var(--canvas-dot, rgba(128,90,213,0.15))" gap={20} size={1} />
            <Controls
              showInteractive={false}
              style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border-color)', borderRadius: 8 }}
            />
          </ReactFlow>
        </ReactFlowProvider>
      </div>
    </div>,
    document.body
  )
}

// Inline compact preview
function FlowPreviewInner({ yamlContent, height = 350 }: FlowPreviewProps) {
  const [nodes, setNodes] = useState<Node[]>([])
  const [edges, setEdges] = useState<Edge[]>([])
  const [expanded, setExpanded] = useState(false)

  useEffect(() => {
    if (!yamlContent) return

    const parseFlow = async () => {
      try {
        const parsed = yaml.load(yamlContent)
        if (!parsed || typeof parsed !== 'object') return
        const result = await yamlToFlowAsync(parsed as Record<string, unknown>)
        const styledEdges = (result.edges as Edge[]).map(e => ({
          ...e,
          style: edgeStyle,
          animated: true,
          type: 'default',
        }))
        // Mark nodes with outgoing connections to hide the "+" handle
        const markedNodes = markOutgoingConnections(result.nodes as Node[], styledEdges)
        setNodes(markedNodes)
        setEdges(styledEdges)
      } catch {
        // Silently fail for invalid YAML
      }
    }

    parseFlow()
  }, [yamlContent])

  const proOptions = useMemo(() => ({ hideAttribution: true }), [])
  const handleClose = useCallback(() => setExpanded(false), [])

  if (nodes.length === 0) {
    return (
      <div style={{ height, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--text-muted)' }}>
        Loading flow preview...
      </div>
    )
  }

  return (
    <>
      <div style={{ position: 'relative', height, width: '100%', borderRadius: 8, overflow: 'hidden', border: '1px solid var(--border-color)' }}>
        <ReactFlow
          nodes={nodes}
          edges={edges}
          nodeTypes={nodeTypes}
          fitView
          fitViewOptions={{ padding: 0.3 }}
          nodesDraggable={false}
          nodesConnectable={false}
          elementsSelectable={false}
          panOnDrag={false}
          zoomOnScroll={false}
          zoomOnPinch={false}
          zoomOnDoubleClick={false}
          preventScrolling={false}
          proOptions={proOptions}
          minZoom={0.1}
          maxZoom={1.5}
        >
          <Background color="var(--canvas-dot, rgba(128,90,213,0.15))" gap={20} size={1} />
        </ReactFlow>

        {/* Expand button — elevated surface so it stays readable on the dark canvas */}
        <button
          onClick={() => setExpanded(true)}
          className="absolute top-2 right-2 flex items-center gap-1 px-2 py-1 rounded-md text-[10px] font-medium transition-all cursor-pointer z-10 hover:opacity-90"
          style={{
            background: 'var(--card)',
            border: '1px solid var(--border-color)',
            color: 'var(--text-primary)',
            boxShadow: 'var(--shadow-soft)',
          }}
          title="Expand to fullscreen"
        >
          <Maximize2 size={11} />
          <span>Expand</span>
        </button>
      </div>

      {/* Fullscreen overlay — portaled to body with its own ReactFlowProvider */}
      {expanded && (
        <FullscreenFlowView nodes={nodes} edges={edges} onClose={handleClose} />
      )}
    </>
  )
}

export default function FlowPreview({ yamlContent, height = 350 }: FlowPreviewProps) {
  return (
    <ReactFlowProvider>
      <FlowPreviewInner yamlContent={yamlContent} height={height} />
    </ReactFlowProvider>
  )
}
