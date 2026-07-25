import { useCallback, useMemo, useEffect, useRef, useState } from 'react'
import {
  ReactFlow,
  Background,
  Controls,
  MiniMap,
  useNodesState,
  useEdgesState,
  useReactFlow,
  useNodes,
  useEdges,
  Panel,
  ReactFlowProvider,
} from '@xyflow/react'
import type {
  Node,
  Edge,
  Connection,
  OnSelectionChangeParams,
  NodeChange,
  EdgeChange,
  OnNodeDrag,
  NodeMouseHandler,
  EdgeMouseHandler,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { Edit3, Brain, Wrench, Settings, MessageSquare, Sparkles, LayoutDashboard, Maximize } from 'lucide-react'

import StartNode from './nodes/StartNode'
import EndNode from './nodes/EndNode'
import InputNode from './nodes/InputNode'
import LlmNode from './nodes/LlmNode'
import ToolNode from './nodes/ToolNode'
import OutputNode from './nodes/OutputNode'
import UpdateStateNode from './nodes/UpdateStateNode'
import EditableEdge from './edges/EditableEdge'
import NodeTypePopover from './NodeTypePopover'

// --- Types ---

interface Position {
  x: number
  y: number
}

interface ClipboardNode {
  id: string
  type: string
  data: Record<string, unknown>
  position: Position
}

interface AddNodePopoverState {
  sourceId: string
  position: Position
}

interface ContextMenuState {
  x: number
  y: number
}

interface NodeTypeDefinition {
  type: string
  label: string
  icon: React.ComponentType<{ size: number; style?: React.CSSProperties }>
  iconColor: string
}

interface FlowCanvasInnerProps {
  nodes: Node[]
  edges: Edge[]
  isRunning?: boolean
  readOnly?: boolean
  theme?: string
  onNodeSelect?: (nodeId: string | null) => void
  onNodeDoubleClick?: (nodeId: string) => void
  onEdgeSelect?: (edge: Edge) => void
  selectedNodeId?: string | null
  onAddNode?: (type: string) => void
  onConnect?: (source: string, target: string) => void
  onEdgeRemove?: (source: string, target: string) => void
  onLayoutChange?: (nodes: Node[], edges: Edge[]) => void
  onLayoutSave?: (nodes: Node[], edges: Edge[]) => void
  onOpenAIChat?: (context?: { context: string; nodeIds: string[] }) => void
  onNodeDelete?: (nodeIds: string[]) => void
  onAutoLayout?: () => void
  runningNodeId?: string | null
  onCreateConnectedNode?: (sourceId: string, nodeType: string, position: Position) => void
  onDuplicateNodes?: (nodes: ClipboardNode[]) => void
}

interface FlowCanvasProps extends FlowCanvasInnerProps {}


const nodeTypes = {
  start: StartNode,
  end: EndNode,
  input: InputNode,
  llm: LlmNode,
  tool: ToolNode,
  output: OutputNode,
  updateState: UpdateStateNode,
}

// Custom edge types
const edgeTypes = {
  editable: EditableEdge,
}

// Node type definitions for toolbar - matches Overflow node styling
const NODE_TYPES: NodeTypeDefinition[] = [
  { type: 'input', label: 'Input', icon: Edit3, iconColor: 'var(--node-input)' },
  { type: 'llm', label: 'LLM', icon: Brain, iconColor: 'var(--node-llm)' },
  { type: 'tool', label: 'Tool', icon: Wrench, iconColor: 'var(--node-tool)' },
  { type: 'updateState', label: 'State', icon: Settings, iconColor: 'var(--node-llm)' },
  { type: 'output', label: 'Output', icon: MessageSquare, iconColor: 'var(--node-output)' },
]

function FlowCanvasInner({ 
  nodes: propNodes, 
  edges: propEdges, 
  isRunning, 
  readOnly,
  theme, 
  onNodeSelect, 
  onNodeDoubleClick,
  onEdgeSelect,
  selectedNodeId, 
  onAddNode,
  onConnect: onConnectCallback,
  onEdgeRemove,
  onLayoutChange,
  onLayoutSave,
  onOpenAIChat,
  onNodeDelete,
  onAutoLayout,
  runningNodeId,
  onCreateConnectedNode,  // Callback for quick node creation from + button
  onDuplicateNodes        // Callback for duplicating nodes (copy/paste)
}: FlowCanvasInnerProps) {
  const { getNodes, getEdges } = useReactFlow()
  const [nodes, setNodes, onNodesChangeBase] = useNodesState<Node>([] as Node[])
  const [edges, setEdges, handleEdgesChange] = useEdgesState<Edge>([] as Edge[])
  
  // Track edge dragging to prevent sync conflicts
  const isDraggingEdgeRef = useRef(false)
  
  // Clipboard for copy/paste functionality
  const clipboardRef = useRef<ClipboardNode[]>([])

  // Listen for edge drag events
  useEffect(() => {
    const onDragStart = () => { isDraggingEdgeRef.current = true }
    // Add small delay to drag stop to ensure pending prop updates don't overwrite immediately
    const onDragStop = () => { 
      setTimeout(() => { isDraggingEdgeRef.current = false }, 500)
      // Save immediately to ensure changes persist
      if (onLayoutSave) {
        onLayoutSave(getNodes(), getEdges())
      }
    }
    
    window.addEventListener('astonish:edge-drag-start', onDragStart)
    window.addEventListener('astonish:edge-drag-stop', onDragStop)
    
    return () => {
      window.removeEventListener('astonish:edge-drag-start', onDragStart)
      window.removeEventListener('astonish:edge-drag-stop', onDragStop)
    }
  }, [onLayoutSave, getNodes, getEdges])

  // State for quick add node popover
  const [addNodePopover, setAddNodePopover] = useState<AddNodePopoverState | null>(null) // { sourceId, position: {x, y} }

  // Listen for add-node-click events from nodes
  useEffect(() => {
    const handleAddNodeClick = (e: Event) => {
      if (!readOnly) {
        setAddNodePopover((e as CustomEvent).detail)
      }
    }
    
    window.addEventListener('astonish:add-node-click', handleAddNodeClick)
    return () => window.removeEventListener('astonish:add-node-click', handleAddNodeClick)
  }, [readOnly])

  // Handle node type selection from popover
  const handleNodeTypeSelect = useCallback((nodeType: string) => {
    if (addNodePopover && onCreateConnectedNode) {
      // Find source node to get its position
      const sourceNode = nodes.find(n => n.id === addNodePopover.sourceId)
      const position = sourceNode 
        ? { x: sourceNode.position.x, y: sourceNode.position.y + 150 }
        : { x: 300, y: 300 }
      
      onCreateConnectedNode(addNodePopover.sourceId, nodeType, position)
    }
    setAddNodePopover(null)
  }, [addNodePopover, nodes, onCreateConnectedNode])

  
  // Wrap node change handler to detect deletions and notify parent
  const handleNodesChange = useCallback((changes: NodeChange<Node>[]) => {
    // Check for node removals
    const removals = changes.filter(change => change.type === 'remove')
    if (removals.length > 0 && onNodeDelete) {
      // Get the IDs of removed nodes that are not start/end
      const removedIds = removals
        .map(r => ('id' in r ? r.id : ''))
        .filter(id => id !== '' && id !== 'START' && id !== 'END')
      
      if (removedIds.length > 0) {
        // Notify parent of all deletions at once (pass array)
        onNodeDelete(removedIds)
      }
    }
    
    // Apply the change locally
    onNodesChangeBase(changes)
  }, [onNodesChangeBase, onNodeDelete])
  

  
  // Check if canvas is empty (only START and END nodes)
  const isEmptyCanvas = propNodes.filter(n => n.type !== 'start' && n.type !== 'end').length === 0

  // Track multi-selection for AI assist
  const [selectedNodeIds, setSelectedNodeIds] = useState<string[]>([])
  
  // Filter to get only "real" nodes (not START, END)
  const selectedRealNodes = useMemo(() => {
    return selectedNodeIds.filter(id => {
      const node = nodes.find(n => n.id === id)
      return node && !['start', 'end'].includes(node.type!)
    })
  }, [selectedNodeIds, nodes])
  
  const hasMultiSelection = selectedRealNodes.length >= 2

  // Keyboard shortcuts: Cmd/Ctrl+A (select all), Cmd/Ctrl+C (copy), Cmd/Ctrl+V (paste)
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      // Only handle if focus is on the canvas (not in an input/textarea)
      const target = e.target as HTMLElement
      if (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA') return
      
      // Check for Cmd+A (Mac) or Ctrl+A (Windows/Linux) - Select All
      if ((e.metaKey || e.ctrlKey) && e.key === 'a') {
        e.preventDefault()
        
        // Select all nodes by marking them as selected
        setNodes(currentNodes => 
          currentNodes.map(node => ({
            ...node,
            selected: true
          }))
        )
        
        // Update selectedNodeIds for AI assist button visibility
        setSelectedNodeIds(nodes.map(n => n.id))
      }
      
      // Cmd+C - Copy selected nodes
      if ((e.metaKey || e.ctrlKey) && e.key === 'c') {
        // Get selected nodes (excluding START and END)
        const selectedNodes = nodes.filter(n => 
          n.selected && n.id !== 'START' && n.id !== 'END'
        )
        
        if (selectedNodes.length > 0) {
          // Store node data in clipboard
          clipboardRef.current = selectedNodes.map(n => ({
            id: n.id,
            type: n.type!,
            data: { ...n.data },
            position: { ...n.position }
          }))
        }
      }
      
      // Cmd+V - Paste nodes from clipboard
      if ((e.metaKey || e.ctrlKey) && e.key === 'v') {
        if (clipboardRef.current.length > 0 && onDuplicateNodes) {
          e.preventDefault()
          // Call parent handler with clipboard contents
          onDuplicateNodes(clipboardRef.current)
        }
      }
    }
    
    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [setNodes, nodes, onDuplicateNodes])

  // Sync nodes from props - update selection state
  // Sync nodes from props - preserve multi-selection when syncing
  useEffect(() => {
    if (propNodes && propNodes.length > 0) {
      // Build set of nodes that have outgoing connections
      const nodesWithOutgoing = new Set<string>()
      if (propEdges) {
        propEdges.forEach(edge => nodesWithOutgoing.add(edge.source))
      }
      
      setNodes((currentNodes) => {
        // Build map of current selection states
        const currentSelectionMap = new Map<string, boolean | undefined>()
        currentNodes.forEach(n => currentSelectionMap.set(n.id, n.selected))
        
        // Add selected state to prop nodes, preserving current multi-selection
        return propNodes.map(node => {
          // Preserve existing selection, fallback to selectedNodeId for single-click selection
          const isSelected = currentSelectionMap.has(node.id) 
            ? currentSelectionMap.get(node.id) 
            : node.id === selectedNodeId
          
          return {
            ...node,
            selected: isSelected,
            data: {
              ...node.data,
              isSelected: node.id === selectedNodeId,
              isActive: node.id === runningNodeId,
              hasOutgoingConnection: nodesWithOutgoing.has(node.id)
            }
          }
        })
      })
    }
  }, [propNodes, propEdges, selectedNodeId, runningNodeId, setNodes])

  // Sync edges from props
  // Sync edges from props
  useEffect(() => {
    if (propEdges && !isDraggingEdgeRef.current) {
      setEdges(propEdges)
    }
  }, [propEdges, setEdges])

  // Notify parent of layout changes (for saving)
  // Get reactive state from store to ensure we capture updates from custom edges/nodes
  // that modify the store directly (like EditableEdge)
  const storeNodes = useNodes()
  const storeEdges = useEdges()

  // Notify parent of layout changes (for saving)
  useEffect(() => {
    if (onLayoutChange && storeNodes.length > 0) {
      onLayoutChange(storeNodes, storeEdges)
    }
  }, [storeNodes, storeEdges, onLayoutChange])

  // NOTE: Layout saving is now handled ONLY by explicit user interactions:
  // - onNodeDragStop (line 394) for node positions
  // - edge-drag-stop event (line 231) for edge waypoints
  // This avoids reformatting YAML when user edits the source view directly.

  // Listener for immediate save on edge drag stop (custom event from EditableEdge)
  useEffect(() => {
    if (!onLayoutSave) return

    const handleEdgeDragStop = () => {
      // Small timeout to ensure store update has propagated
      setTimeout(() => {
        onLayoutSave(storeNodes, storeEdges)
      }, 50) 
    }

    window.addEventListener('astonish:edge-drag-stop', handleEdgeDragStop)
    return () => window.removeEventListener('astonish:edge-drag-stop', handleEdgeDragStop)
  }, [storeNodes, storeEdges, onLayoutSave])

  // Get React Flow instance for coordinate conversion and viewport control
  const { setViewport, getViewport } = useReactFlow()
  const hasCentered = useRef(false)
  const containerRef = useRef<HTMLDivElement>(null)
  
  // Center viewport horizontally on START node when flow first loads (view only, no flash)
  useEffect(() => {
    if (nodes.length > 0 && !hasCentered.current) {
      const startNode = nodes.find(n => n.id === 'START')
      if (startNode && startNode.position) {
        // Get container width to calculate center offset
        const containerWidth = containerRef.current?.offsetWidth || 800
        // Calculate X to center START node horizontally
        const centerX = -(startNode.position.x - containerWidth / 2 + 60)
        // Set viewport: center horizontally, keep top visible
        setViewport({ x: centerX, y: 30, zoom: 0.9 }, { duration: 0 })
        hasCentered.current = true
      }
    }
  }, [nodes, setViewport])
  
  // Reset centering flag when agent changes (nodes cleared)
  useEffect(() => {
    if (nodes.length === 0) {
      hasCentered.current = false
    }
  }, [nodes.length])

  // Recenter flow horizontally when isRunning changes (container size changes)
  // NOTE: We use a ref to access nodes to avoid running this when nodes change
  const nodesRef = useRef(nodes)
  useEffect(() => { nodesRef.current = nodes }, [nodes])
  
  useEffect(() => {
    // Small delay to let the container resize complete
    const timer = setTimeout(() => {
      const viewport = getViewport()
      const containerWidth = containerRef.current?.offsetWidth || 800
      const currentNodes = nodesRef.current
      const startNode = currentNodes.find(n => n.id === 'START')
      if (startNode && startNode.position) {
        // Calculate X to center START node horizontally, keep same Y and zoom
        const centerX = -(startNode.position.x - containerWidth / 2 + 60) * viewport.zoom
        setViewport({ x: centerX, y: viewport.y, zoom: viewport.zoom }, { duration: 300 })
      }
    }, 100)
    return () => clearTimeout(timer)
  }, [isRunning, getViewport, setViewport]) // Removed nodes from dependencies




  // Handle new connection (drag from one node to another)
  const onConnect = useCallback((params: Connection) => {
    if (onConnectCallback) {
      // Call parent to update YAML, which will re-render with new edge
      onConnectCallback(params.source, params.target)
    }
  }, [onConnectCallback])

  // Handle edge deletion
  const onEdgesDelete = useCallback((deletedEdges: Edge[]) => {
    if (onEdgeRemove) {
      deletedEdges.forEach(edge => {
        onEdgeRemove(edge.source, edge.target)
      })
    }
  }, [onEdgeRemove])

  const onNodeClick: NodeMouseHandler = useCallback((event, node) => {
    // Skip parent's single-select handler when shift is held (multi-selection)
    if (event.shiftKey) {
      return // Let React Flow handle multi-selection
    }
    if (onNodeSelect) {
      onNodeSelect(node.id)
    }
  }, [onNodeSelect])

  const handleNodeDoubleClick: NodeMouseHandler = useCallback((event, node) => {
    // For regular nodes, call the parent handler
    if (onNodeDoubleClick) {
      onNodeDoubleClick(node.id)
    }
  }, [onNodeDoubleClick])

  // Handle edge double-click for editing conditions
  const handleEdgeDoubleClick: EdgeMouseHandler = useCallback((event, edge) => {
    if (onEdgeSelect) {
      onEdgeSelect(edge)
    }
  }, [onEdgeSelect])

  // Handle selection changes from React Flow
  const onSelectionChange = useCallback(({ nodes: selectedNodes }: OnSelectionChangeParams) => {
    const ids = selectedNodes?.map(n => n.id) || []
    setSelectedNodeIds(ids)
  }, [])

  // Context menu state
  const [contextMenu, setContextMenu] = useState<ContextMenuState | null>(null)

  const onPaneClick = useCallback(() => {
    // Clicking on empty space deselects
    if (onNodeSelect) {
      onNodeSelect(null)
    }
    // Also clear multi-selection
    setSelectedNodeIds([])
    // Close context menu if open
    setContextMenu(null)
  }, [onNodeSelect])

  // Handle right-click on pane for context menu
  const onPaneContextMenu = useCallback((event: React.MouseEvent | MouseEvent) => {
    event.preventDefault()
    if (isRunning || readOnly) return
    setContextMenu({
      x: event.clientX,
      y: event.clientY,
    })
  }, [isRunning, readOnly, setContextMenu])

  // Handle auto layout
  const handleAutoLayout = useCallback(() => {
    setContextMenu(null)
    if (onAutoLayout) {
      onAutoLayout()
    }
  }, [onAutoLayout, setContextMenu])

  // Handle reset zoom
  const handleResetZoom = useCallback(() => {
    setContextMenu(null)
    const viewport = getViewport()
    setViewport({ x: viewport.x, y: viewport.y, zoom: 0.9 }, { duration: 300 })
  }, [getViewport, setViewport, setContextMenu])

  const defaultEdgeOptions = useMemo(() => ({
    style: { stroke: 'var(--brand)', strokeWidth: 2 },
    type: 'editable',  // Use custom editable edge with inline waypoints
  }), [])




  // Track initial positions for multi-node drag to calculate delta
  interface DragStartState {
    nodes: Map<string, Position>
    edges: { id: string; points: Position[] }[]
  }
  const dragStartPositionsRef = useRef<DragStartState | null>(null)
  
  // Handle node drag start - store initial positions for delta calculation
  const handleNodeDragStart: OnNodeDrag = useCallback((event, node, draggedNodes) => {
    // Store initial positions if multiple nodes are selected
    if (draggedNodes && draggedNodes.length > 1) {
      dragStartPositionsRef.current = {
        nodes: new Map(draggedNodes.map(n => [n.id, { x: n.position.x, y: n.position.y }])),
        edges: edges.map(e => ({ 
          id: e.id, 
          points: (e.data?.points as Position[]) ? [...(e.data!.points as Position[]).map((p: Position) => ({...p}))] : [] 
        }))
      }
    }
  }, [edges])
  
  // Handle node drag - update edge waypoints to follow nodes
  const handleNodeDrag: OnNodeDrag = useCallback((event, node, draggedNodes) => {
    // Only update edge waypoints if multiple nodes are being dragged
    if (!dragStartPositionsRef.current || !draggedNodes || draggedNodes.length <= 1) return
    
    // Calculate drag delta from any dragged node
    const startPos = dragStartPositionsRef.current.nodes.get(node.id)
    if (!startPos) return
    
    const deltaX = node.position.x - startPos.x
    const deltaY = node.position.y - startPos.y
    
    // Update all edge waypoints by the same delta
    setEdges(currentEdges => 
      currentEdges.map((edge, idx) => {
        const originalEdge = dragStartPositionsRef.current!.edges[idx]
        if (!originalEdge || !originalEdge.points || originalEdge.points.length === 0) {
          return edge
        }
        
        // Shift all waypoints by the drag delta
        const newPoints = originalEdge.points.map((p: Position) => ({
          x: p.x + deltaX,
          y: p.y + deltaY
        }))
        
        return {
          ...edge,
          data: { ...edge.data, points: newPoints }
        }
      })
    )
  }, [setEdges])

  // Handle node drag stop to save layout immediately
  const handleNodeDragStop: OnNodeDrag = useCallback((event, node, draggedNodes) => {
    // Clear drag start positions
    dragStartPositionsRef.current = null
    
    if (onLayoutSave) {
      // Use getNodes() to get the current state of ALL nodes
      // The 'draggedNodes' argument (3rd arg) only contains nodes that were dragged
      const currentNodes = getNodes()
      onLayoutSave(currentNodes, edges) 
    }
  }, [onLayoutSave, edges, getNodes])

  return (
    <div ref={containerRef} className="h-full w-full bg-[var(--work-background,var(--canvas-background))]">
      <ReactFlow
        nodes={nodes}
        edges={edges}
        onNodesChange={handleNodesChange}
        onEdgesChange={handleEdgesChange}
        onConnect={onConnect}
        onEdgesDelete={onEdgesDelete}
        onNodeClick={onNodeClick}
        onNodeDoubleClick={handleNodeDoubleClick}
        onEdgeDoubleClick={handleEdgeDoubleClick}
        onPaneClick={onPaneClick}
        onPaneContextMenu={onPaneContextMenu}
        onSelectionChange={onSelectionChange}
        selectionOnDrag={false}
        selectNodesOnDrag={false}
        panOnDrag={true}
        panOnScroll={true}
        selectionKeyCode="Shift"
        multiSelectionKeyCode="Shift"
        nodeTypes={nodeTypes}
        edgeTypes={edgeTypes}
        defaultEdgeOptions={defaultEdgeOptions}
        defaultViewport={{ x: 50, y: 30, zoom: 0.8 }}
        proOptions={{ hideAttribution: true }}
        nodesDraggable={!isRunning}
        nodesConnectable={!isRunning}
        elementsSelectable={!isRunning}
        deleteKeyCode={['Backspace', 'Delete']}
        colorMode={theme as 'dark' | 'light' | 'system'}
        minZoom={0.1}
        maxZoom={2}
        onNodeDragStart={handleNodeDragStart}
        onNodeDrag={handleNodeDrag}
        onNodeDragStop={handleNodeDragStop}
      >
        <Background color="var(--border)" gap={20} />
        <Controls className="rounded-lg shadow-md" style={{ background: 'var(--panel-background)', borderColor: 'var(--panel-border)' }} />
        <MiniMap
          nodeColor={(node: Node) => {
            switch (node.type) {
              case 'start': return 'var(--node-start)'
              case 'end': return 'var(--node-end)'
              case 'input': return 'var(--node-input)'
              case 'llm': return 'var(--node-llm)'
              case 'tool': return 'var(--node-tool)'
              case 'output': return 'var(--node-output)'
              case 'updateState': return 'var(--muted-foreground)'
              default: return 'var(--border)'
            }
          }}
          className="rounded-lg shadow-md"
          style={{ background: 'var(--panel-background)' }}
          maskColor={theme === 'dark' ? 'rgba(0, 0, 0, 0.5)' : 'rgba(255, 255, 255, 0.5)'}
        />
        
        {/* Node Type Toolbar */}
        {!isRunning && (
          <Panel position="top-right" className="m-2">
            <div 
              className="flex flex-col gap-2 p-2 rounded-xl shadow-lg"
              style={{ background: 'var(--panel-background)', border: '1px solid var(--panel-border)' }}
            >
              <div className="mb-1 text-center text-xs text-muted-foreground">
                Add Node
              </div>
              {NODE_TYPES.map(({ type, label, icon: Icon, iconColor }) => (
                <button
                  key={type}
                  onClick={() => onAddNode && onAddNode(type)}
                  className="flex items-center gap-2 px-3 py-2 rounded-lg transition-all hover:scale-105"
                  style={{ 
                    background: 'var(--overflow-node-bg)',
                    border: '1px solid var(--overflow-node-border)',
                    minWidth: '80px'
                  }}
                  title={`Add ${label} node`}
                >
                  <div 
                    style={{
                      background: 'var(--overflow-icon-bg)',
                      borderRadius: '6px',
                      padding: '6px',
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'center',
                    }}
                  >
                    <Icon size={14} style={{ color: iconColor }} />
                  </div>
                  <span className="text-xs font-medium" style={{ color: 'var(--overflow-node-title)' }}>{label}</span>
                </button>
              ))}
            </div>
          </Panel>
        )}
        
        {/* Empty Canvas State - Create with AI - hidden in read-only mode */}
        {!isRunning && !readOnly && isEmptyCanvas && onOpenAIChat && (
          <div 
            className="absolute inset-0 flex items-center justify-center pointer-events-none"
            style={{ zIndex: 10 }}
          >
            <div 
              className="flex flex-col items-center gap-4 p-6 rounded-2xl shadow-xl pointer-events-auto"
              style={{ 
                background: 'var(--bg-secondary)', 
                border: '2px solid var(--border-color)',
                minWidth: '320px'
              }}
            >
              <div className="text-center">
                <h3 className="text-lg font-semibold mb-1" style={{ color: 'var(--text-primary)' }}>
                  Start Building Your Flow
                </h3>
                <p className="text-sm" style={{ color: 'var(--text-muted)' }}>
                  Describe what you want or drag nodes from the toolbar
                </p>
              </div>
              
              <button
                onClick={() => onOpenAIChat()}
                className="send-gradient flex items-center gap-2 rounded-xl px-6 py-3 font-medium text-white shadow-none transition-all hover:scale-105 hover:opacity-90"
              >
                <Sparkles className="h-5 w-5" />
                Create with AI
              </button>
              
              <div className="text-xs" style={{ color: 'var(--text-muted)' }}>
                or use the node toolbar on the right →
              </div>
            </div>
          </div>
        )}
        
        {/* Instructions */}
        {!isRunning && (
          <Panel position="bottom-left" className="m-2">
            <div 
              className="text-xs px-3 py-2 rounded-lg"
              style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)', color: 'var(--text-muted)' }}
            >
              Drag from handles to connect • ⌘A/Ctrl+A to select all • Shift+drag to multi-select
            </div>
          </Panel>
        )}
        
        {/* Multi-Selection AI Assist Button - hidden in read-only mode */}
        {!isRunning && !readOnly && hasMultiSelection && onOpenAIChat && (
          <Panel position="bottom-center" className="mb-4">
            <button
              onClick={() => onOpenAIChat({ context: 'multi_node', nodeIds: selectedRealNodes })}
              className="send-gradient flex items-center gap-2 rounded-lg px-4 py-2.5 font-medium text-white shadow-none transition-all hover:scale-105 hover:opacity-90"
            >
              <Sparkles size={18} />
              AI Assist ({selectedRealNodes.length} nodes)
            </button>
          </Panel>
        )}
      </ReactFlow>
      
      {/* Context Menu */}
      {contextMenu && (
        <div
          className="fixed z-50 py-1 rounded-lg shadow-xl"
          style={{
            left: contextMenu.x,
            top: contextMenu.y,
            background: 'var(--bg-secondary)',
            border: '1px solid var(--border-color)',
            minWidth: '160px'
          }}
        >
          <button
            onClick={handleAutoLayout}
            className="flex w-full items-center gap-2 px-3 py-2 text-sm transition-colors hover:bg-[color:var(--item-active)]"
            style={{ color: 'var(--text-primary)' }}
          >
            <LayoutDashboard size={16} className="text-primary" />
            Auto Layout
          </button>
          <button
            onClick={handleResetZoom}
            className="flex w-full items-center gap-2 px-3 py-2 text-sm transition-colors hover:bg-[color:var(--item-active)]"
            style={{ color: 'var(--text-primary)' }}
          >
            <Maximize size={16} className="text-primary" />
            Reset Zoom
          </button>
        </div>
      )}
      
      {/* Quick Add Node Popover */}
      {addNodePopover && (
        <NodeTypePopover
          position={addNodePopover.position}
          onSelect={handleNodeTypeSelect}
          onClose={() => setAddNodePopover(null)}
          theme={(theme === 'light' ? 'light' : 'dark') as 'dark' | 'light'}
        />
      )}
    </div>
  )
}

// Wrapper component - agent changes handled by key prop in App.jsx
export default function FlowCanvas(props: FlowCanvasProps) {
  return (
    <ReactFlowProvider>
      <FlowCanvasInner {...props} />
    </ReactFlowProvider>
  )
}
