import type ELK_TYPE from 'elkjs/lib/elk.bundled.js'

// --- Types ---

 
type YamlData = Record<string, any>
type YamlNode = Record<string, any>
 

interface Position {
  x: number
  y: number
}

interface FlowNode {
  id: string
  type: string
  position: Position
  data: Record<string, unknown>
}

interface FlowEdge {
  id: string
  source: string
  target: string
  animated: boolean
  style: Record<string, unknown>
  type: string
  label?: string
  labelStyle?: Record<string, unknown>
  labelBgStyle?: Record<string, unknown>
  labelBgPadding?: number[]
  data?: Record<string, unknown>
}

interface SavedLayout {
  nodes: Record<string, Position>
  edges: Record<string, Position[]>
}

// --- Constants ---

// Lazy-load ELK to avoid bundling elkjs in the main chunk.
// It's only needed when computing layouts, which is always async.
let _elk: InstanceType<typeof ELK_TYPE> | null = null
async function getElk() {
  if (!_elk) {
    const { default: ELK } = await import('elkjs/lib/elk.bundled.js')
    _elk = new ELK()
  }
  return _elk
}

const NODE_TYPE_MAP: Record<string, string> = {
  input: 'input',
  llm: 'llm',
  tool: 'tool',
  output: 'output',
  update_state: 'updateState',
}

const elkOptions: Record<string, string> = {
  'elk.algorithm': 'layered',
  'elk.direction': 'DOWN',
  'elk.layered.spacing.nodeNodeBetweenLayers': '100',
  'elk.spacing.nodeNode': '100',
  'elk.layered.spacing.edgeEdgeBetweenLayers': '30',
  'elk.layered.spacing.edgeNodeBetweenLayers': '50',
  'elk.edgeRouting': 'ORTHOGONAL',
  'elk.layered.edgeRouting.selfLoopDistribution': 'EQUALLY',
  'elk.layered.nodePlacement.strategy': 'BRANDES_KOEPF',
  'elk.layered.nodePlacement.bk.fixedAlignment': 'BALANCED',
  'elk.layered.nodePlacement.favorStraightEdges': 'true',
  'elk.layered.crossingMinimization.strategy': 'LAYER_SWEEP',
  'elk.layered.crossingMinimization.thoroughness': '10',
  'elk.layered.considerModelOrder.strategy': 'NODES_AND_EDGES',
  'elk.separateConnectedComponents': 'false',
}

// --- Functions ---

export function parseNodes(yamlData: YamlData, savedLayout: SavedLayout | null = null): FlowNode[] {
  const nodes: FlowNode[] = []
  const nodePositions = savedLayout?.nodes || {}
  
  const startPos = nodePositions['START']
  nodes.push({
    id: 'START',
    type: 'start',
    position: startPos ? { x: startPos.x, y: startPos.y } : { x: 0, y: 0 },
    data: { label: 'START' }
  })
  
  if (yamlData.nodes && Array.isArray(yamlData.nodes)) {
    yamlData.nodes.forEach((node: YamlNode, index: number) => {
      try {
        if (!node || typeof node !== 'object') {
          throw new Error('Node must be an object')
        }
        if (!node.name) {
          throw new Error('Node missing required "name" field')
        }
        
        const validationErrors: string[] = []
        const VALID_NODE_TYPES = ['input', 'llm', 'tool', 'output', 'update_state']
        
        if (!node.type) {
          validationErrors.push(`'type' is required. Valid types: ${VALID_NODE_TYPES.join(', ')}`)
        } else if (!VALID_NODE_TYPES.includes(node.type as string)) {
          validationErrors.push(`'type' must be one of: ${VALID_NODE_TYPES.join(', ')} (got '${node.type}')`)
        }
        
        if (node.options !== undefined && !Array.isArray(node.options)) {
          validationErrors.push(`'options' must be an array (got ${typeof node.options}). Use: options: [value1, value2] or options: [variable_name]`)
        }
        
        if (node.output_model !== undefined && typeof node.output_model !== 'object') {
          validationErrors.push(`'output_model' must be an object (got ${typeof node.output_model})`)
        }
        
        const TYPES_REQUIRING_OUTPUT_MODEL = ['input', 'llm', 'tool']
        if (TYPES_REQUIRING_OUTPUT_MODEL.includes(node.type as string) && !node.output_model) {
          validationErrors.push(`'output_model' is required for '${node.type}' nodes to store results in state`)
        }
        
        if (node.type === 'llm') {
          if (!node.system) {
            validationErrors.push(`'system' is required for llm nodes to set the AI's behavior`)
          }
          if (!node.prompt) {
            validationErrors.push(`'prompt' is required for llm nodes to provide the input to the AI`)
          }
        }
        
        if (node.type === 'output') {
          if (!node.user_message) {
            validationErrors.push(`'user_message' is required for output nodes to display content to the user`)
          } else if (!Array.isArray(node.user_message)) {
            validationErrors.push(`'user_message' must be an array (got ${typeof node.user_message})`)
          }
        } else if (node.user_message !== undefined && !Array.isArray(node.user_message)) {
          validationErrors.push(`'user_message' must be an array (got ${typeof node.user_message})`)
        }
        
        const nodeType = NODE_TYPE_MAP[node.type as string] || 'llm'
        const savedPos = nodePositions[node.name as string]
        
        nodes.push({
          id: node.name as string,
          type: nodeType,
          position: savedPos ? { x: savedPos.x, y: savedPos.y } : { x: 0, y: 0 },
          data: {
            label: node.name,
            nodeType: node.type,
            yaml: node,
            hasError: validationErrors.length > 0,
            errorMessage: validationErrors.length > 0 ? validationErrors.join('; ') : undefined
          }
        })
      } catch (error) {
        const nodeName = (node?.name as string) || `error_node_${index}`
        console.error(`Error parsing node at index ${index}:`, error)
        nodes.push({
          id: nodeName,
          type: 'llm',
          position: { x: 0, y: 0 },
          data: {
            label: nodeName,
            nodeType: node?.type || 'unknown',
            yaml: node || {},
            hasError: true,
            errorMessage: error instanceof Error ? error.message : String(error)
          }
        })
      }
    })
  }
  
  const endPos = nodePositions['END']
    
  nodes.push({
    id: 'END',
    type: 'end',
    position: endPos ? { x: endPos.x, y: endPos.y } : { x: 0, y: 500 },
    data: { label: 'END' }
  })
  
  return nodes
}

export function parseEdges(yamlData: YamlData, _nodePositions: Record<string, Position> = {}, savedEdges: Record<string, Position[]> = {}): FlowEdge[] {
  const edges: FlowEdge[] = []
  
  if (yamlData.flow && Array.isArray(yamlData.flow)) {
    const hasOtherLogic = yamlData.flow.length > 1

    yamlData.flow.forEach((flowItem: YamlData, index: number) => {
      if (hasOtherLogic && flowItem.from === 'START' && flowItem.to === 'END') {
          return 
      }
      const from = flowItem.from as string
      
      if (flowItem.to) {
        edges.push({
          id: `e-${from}-${flowItem.to}-${index}`,
          source: from,
          target: flowItem.to as string,
          animated: true,
          style: { stroke: 'var(--brand)', strokeWidth: 2 },
          type: 'editable',
          data: { points: savedEdges[`${from}->${flowItem.to}`] }
        })
      } else if (flowItem.edges && Array.isArray(flowItem.edges)) {
        flowItem.edges.forEach((edge: YamlData, edgeIndex: number) => {
          let label = ''
          if (edge.condition) {
            const match = (edge.condition as string).match(/x\['(\w+)'\]\s*==\s*'([^']+)'/)
            if (match) {
              label = `${match[1]} == "${match[2]}"`
            } else {
              const newMatch = (edge.condition as string).match(/x\.get\('([^']+)'\).*?==\s*'([^']+)'/)
              if (newMatch) {
                label = `${newMatch[1]} == "${newMatch[2]}"`
              } else {
                label = (edge.condition as string).replace(/lambda\s+x:\s*/, '').slice(0, 30)
              }
            }
          }
          
          edges.push({
            id: `e-${from}-${edge.to}-${index}-${edgeIndex}`,
            source: from,
            target: edge.to as string,
            animated: true,
            style: { stroke: 'var(--brand)', strokeWidth: 2 },
            type: 'editable',
            label: label,
            labelStyle: { fill: '#9CA3AF', fontSize: 10 },
            labelBgStyle: { fill: '#1a1a2e', fillOpacity: 0.8 },
            labelBgPadding: [4, 2],
            data: { 
              points: savedEdges[`${from}->${edge.to}`],
              condition: edge.condition || ''
            }
          })
        })
      }
    })
  }
  
  return edges
}

function getNodeDimensions(_label: string): { width: number; height: number } {
  const width = 180
  const height = 50
  return { width, height }
}

export async function autoLayoutAsync(nodes: FlowNode[], edges: FlowEdge[]): Promise<FlowNode[]> {
  const graph = {
    id: 'root',
    layoutOptions: elkOptions,
    children: nodes.map((node) => {
      const { width, height } = getNodeDimensions(node.data?.label as string || node.id)
      return {
        id: node.id,
        width,
        height,
      }
    }),
    edges: edges.map((edge) => ({
      id: edge.id,
      sources: [edge.source],
      targets: [edge.target],
    })),
  }

  try {
    const elk = await getElk()
    const layoutedGraph = await elk.layout(graph)
    
    return nodes.map((node) => {
      const elkNode = layoutedGraph.children?.find((n) => n.id === node.id)
      if (elkNode) {
        return {
          ...node,
          position: { x: elkNode.x ?? 0, y: elkNode.y ?? 0 },
        }
      }
      return node
    })
  } catch (error) {
    console.error('ELK layout error:', error)
    return nodes.map((node, index) => ({
      ...node,
      position: { x: 100, y: index * 100 },
    }))
  }
}

export function autoLayout(nodes: FlowNode[], edges: FlowEdge[]): FlowNode[] {
  void edges // unused in sync fallback
  let y = 0
  const spacing = 100
  
  return nodes.map((node) => {
    const { height } = getNodeDimensions(node.data?.label as string || node.id)
    const position = { x: 200, y }
    y += height + spacing
    return { ...node, position }
  })
}

export async function yamlToFlowAsync(yamlData: YamlData): Promise<{ nodes: FlowNode[]; edges: FlowEdge[] }> {
  if (!yamlData) {
    return { nodes: [], edges: [] }
  }
  
  const savedLayout: SavedLayout | null = yamlData.layout || null
  const hasLayout = savedLayout && savedLayout.nodes && Object.keys(savedLayout.nodes).length > 0
  
  const nodes = parseNodes(yamlData, savedLayout)
  
  const nodePositions: Record<string, Position> = {}
  nodes.forEach(n => { nodePositions[n.id] = n.position })
  
  const edges = parseEdges(yamlData, nodePositions, savedLayout?.edges)
  
  let finalNodes: FlowNode[]
  if (hasLayout) {
    finalNodes = [...nodes]
  } else {
    finalNodes = await autoLayoutAsync(nodes, edges)
  }
  
  return {
    nodes: finalNodes,
    edges,
  }
}

export function yamlToFlow(yamlData: YamlData): { nodes: FlowNode[]; edges: FlowEdge[] } {
  if (!yamlData) {
    return { nodes: [], edges: [] }
  }
  
  const savedLayout: SavedLayout | null = yamlData.layout || null
  const hasLayout = savedLayout && savedLayout.nodes && Object.keys(savedLayout.nodes).length > 0
  
  const nodes = parseNodes(yamlData, savedLayout)
  
  const nodePositions: Record<string, Position> = {}
  nodes.forEach(n => { nodePositions[n.id] = n.position })
  
  const edges = parseEdges(yamlData, nodePositions, savedLayout?.edges)
  
  let finalNodes: FlowNode[]
  if (hasLayout) {
    finalNodes = [...nodes]
  } else {
    finalNodes = autoLayout(nodes, edges)
  }
  
  return {
    nodes: finalNodes,
    edges,
  }
}

export function extractLayout(nodes: FlowNode[], edges: FlowEdge[]): SavedLayout {
  const layout: SavedLayout = {
    nodes: {},
    edges: {}
  }
  
  nodes.forEach((node) => {
    if (node.type !== 'waypoint') {
      layout.nodes[node.id] = {
        x: Math.round(node.position.x),
        y: Math.round(node.position.y)
      }
    }
  })

  edges.forEach((edge) => {
    const points = edge.data?.points
    if (Array.isArray(points) && points.length > 0) {
      layout.edges[`${edge.source}->${edge.target}`] = points.map((p: Position) => ({
        x: Math.round(p.x),
        y: Math.round(p.y)
      }))
    }
  })
  
  return layout
}
