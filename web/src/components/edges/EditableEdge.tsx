import { useCallback, useMemo } from 'react'
import { 
  BaseEdge, 
  EdgeLabelRenderer, 
  useReactFlow,
  type EdgeProps,
  type Edge
} from '@xyflow/react'
import type { CSSProperties } from 'react'

interface Point {
  x: number
  y: number
}

interface EdgeHandle extends Point {
  index: number
}

interface EditableEdgeData extends Record<string, unknown> {
  points?: Point[]
  condition?: string
  label?: string
}

type EditableEdgeType = Edge<EditableEdgeData>

/**
 * EditableEdge - Orthogonal edge with segment manipulation
 */
export default function EditableEdge({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  style = {},
  markerEnd,
  selected,
  data,
  // Label props
  label,
  labelStyle,
  labelShowBg = true, 
  labelBgStyle,
  labelBgPadding,
  labelBgBorderRadius,
}: EdgeProps<EditableEdgeType>) {
  const { setEdges, screenToFlowPosition } = useReactFlow()

  // 1. Get points or initialize with orthogonal defaults
  const rawPoints = useMemo(() => {
    if (data?.points && Array.isArray(data.points) && data.points.length > 0) {
      return data.points
    }
    // Default: Generate orthogonal path (step pattern)
    // Creates: Source -> vertical drop -> horizontal -> vertical to Target
    const midY = (sourceY + targetY) / 2
    return [
      { x: sourceX, y: midY },  // Drop down from source
      { x: targetX, y: midY }   // Move horizontally to target column
    ]
  }, [data?.points, sourceX, sourceY, targetX, targetY])

  // 2. Enforce Vertical Connection Constraints (Snap to Node X)
  // This ensures that when nodes move, the vertical segments attached to them
  // stay vertical and move with the node.
  const points = useMemo(() => {
    if (rawPoints.length === 0) return [] as Point[]
    
    // Create copy
    const snapped = rawPoints.map(p => ({...p}))
    
    // Force first point X to match Source X (Vertical drop from Source)
    if (snapped.length > 0) {
       snapped[0].x = sourceX
    }
    
    // Force last point X to match Target X (Vertical drop to Target)
    if (snapped.length > 0) {
       snapped[snapped.length - 1].x = targetX
    }
    
    return snapped
  }, [rawPoints, sourceX, targetX])

  // 3. Build Path and Label Position
  const { path, labelX, labelY } = useMemo(() => {
    let p = `M ${sourceX} ${sourceY}`
    const allPoints: Point[] = [{x: sourceX, y: sourceY}, ...points, {x: targetX, y: targetY}]
    
    // Build path string
    for (let i = 0; i < points.length; i++) {
      p += ` L ${points[i].x} ${points[i].y}`
    }
    p += ` L ${targetX} ${targetY}`
    
    // Calculate Label Position (Polyline Midpoint)
    let totalLen = 0
    const segmentLens: number[] = []
    
    // Calculate lengths
    for (let i=0; i<allPoints.length-1; i++) {
        const p1 = allPoints[i]
        const p2 = allPoints[i+1]
        const d = Math.hypot(p2.x - p1.x, p2.y - p1.y)
        segmentLens.push(d)
        totalLen += d
    }
    
    let targetLen = totalLen / 2
    let currentLen = 0
    let lx = (sourceX + targetX) / 2
    let ly = (sourceY + targetY) / 2
    
    // Find point
    for (let i=0; i<allPoints.length-1; i++) {
        const len = segmentLens[i]
        if (currentLen + len >= targetLen) {
            const remaining = targetLen - currentLen
            const ratio = len === 0 ? 0 : remaining / len
            const p1 = allPoints[i]
            const p2 = allPoints[i+1]
            lx = p1.x + (p2.x - p1.x) * ratio
            ly = p1.y + (p2.y - p1.y) * ratio
            break
        }
        currentLen += len
    }

    return { path: p, labelX: lx, labelY: ly }
  }, [sourceX, sourceY, targetX, targetY, points])

  // 3. Handle Drag Logic
  const onHandleMouseDown = useCallback((e: React.MouseEvent, index: number) => {
    e.stopPropagation()
    e.preventDefault()
    
    // Signal drag start to prevent prop sync
    window.dispatchEvent(new CustomEvent('astonish:edge-drag-start'))

    // Determine segment we are dragging
    // Segments: 
    // 0: Source -> P[0]
    // i: P[i-1] -> P[i]
    // n: P[n-1] -> Target
    
    // We need the start and end of the segment to know its orientation
    const start = index === 0 ? { x: sourceX, y: sourceY } : points[index - 1]
    const end = index === points.length ? { x: targetX, y: targetY } : points[index]

    const isHorizontal = Math.abs(start.y - end.y) < Math.abs(start.x - end.x)

    // --- DRAG START LOGIC ---
    let currentPoints = [...points]
    let activeLeftIndex = index - 1 // Index in 'currentPoints' of the start of the dragged segment
    let activeRightIndex = index    // Index in 'currentPoints' of the end of the dragged segment
    
    const STUB_LEN = 30
    
    // Capture state for dragging
    const startX = e.clientX
    const startY = e.clientY
    // Calculate Flow Position for initial corner alignment
    const startFlow = screenToFlowPosition({ x: startX, y: startY })
    
    // 1. Isolate FROM SOURCE if needed
    if (activeLeftIndex === -1) {
      const dirX = Math.sign(targetX - sourceX) || 1
      const dirY = Math.sign(targetY - sourceY) || 1
      
      let pStub: Point, pCorner: Point
      
      if (isHorizontal) {
         pStub = { x: sourceX + (dirX * STUB_LEN), y: sourceY }
         // Use Mouse Y for Corner Y to enforce orthogonal drag immediately
         pCorner = { x: sourceX + (dirX * STUB_LEN), y: startFlow.y }
      } else {
         pStub = { x: sourceX, y: sourceY + (dirY * STUB_LEN) }
         // Use Mouse X for Corner X
         pCorner = { x: startFlow.x, y: sourceY + (dirY * STUB_LEN) }
      }
      
      currentPoints.unshift(pStub, pCorner)
      activeLeftIndex += 2
      activeRightIndex += 2
    }
    
    // 2. Isolate FROM TARGET if needed
    if (activeRightIndex === currentPoints.length) {
       const dirX = Math.sign(sourceX - targetX) || -1 
       const dirY = Math.sign(sourceY - targetY) || -1
       
       let pStub: Point, pCorner: Point
       
       if (isHorizontal) {
         pStub = { x: targetX + (dirX * STUB_LEN), y: targetY }
         pCorner = { x: targetX + (dirX * STUB_LEN), y: startFlow.y }
       } else {
         pStub = { x: targetX, y: targetY + (dirY * STUB_LEN) }
         pCorner = { x: startFlow.x, y: targetY + (dirY * STUB_LEN) }
       }
       
       currentPoints.push(pCorner, pStub)
    }

    // Apply insertions immediately
    if (currentPoints.length !== points.length) {
       setEdges(edges => edges.map(edge => {
        if (edge.id === id) return { ...edge, data: { ...edge.data, points: currentPoints } }
        return edge
      }))
    }
    
    const initialDragPoints = currentPoints.map(p => ({...p})) 
    let currentDragPoints = [...initialDragPoints]

    const handleMouseMove = (moveEvent: MouseEvent) => {
      const currentFlow = screenToFlowPosition({ x: moveEvent.clientX, y: moveEvent.clientY })
      const startFlowRel = screenToFlowPosition({ x: startX, y: startY })
      
      const deltaX = currentFlow.x - startFlowRel.x
      const deltaY = currentFlow.y - startFlowRel.y

      const newPoints = initialDragPoints.map(p => ({ ...p }))

      if (isHorizontal) {
        // Move Y
        if (activeLeftIndex >= 0) {
           newPoints[activeLeftIndex].y = initialDragPoints[activeLeftIndex].y + deltaY
        }
        if (activeRightIndex < newPoints.length) {
           newPoints[activeRightIndex].y = initialDragPoints[activeRightIndex].y + deltaY
        }
      } else {
        // Move X
        if (activeLeftIndex >= 0) {
           newPoints[activeLeftIndex].x = initialDragPoints[activeLeftIndex].x + deltaX
        }
        if (activeRightIndex < newPoints.length) {
           newPoints[activeRightIndex].x = initialDragPoints[activeRightIndex].x + deltaX
        }
      }
      
      currentDragPoints = newPoints

      setEdges(edges => edges.map(edge => {
        if (edge.id === id) {
          return { ...edge, data: { ...edge.data, points: newPoints } }
        }
        return edge
      }))
    }

    const handleMouseUp = () => {
      document.removeEventListener('mousemove', handleMouseMove)
      document.removeEventListener('mouseup', handleMouseUp)
      
      // Simplify path: Remove collinear or overlapping points
      if (currentDragPoints.length > 0) {
        const simplified: Point[] = []
        const fullPath: Point[] = [
          { x: sourceX, y: sourceY }, 
          ...currentDragPoints, 
          { x: targetX, y: targetY }
        ]
        
        for (let i = 1; i < fullPath.length - 1; i++) {
          const prev = fullPath[i-1]
          const curr = fullPath[i]
          const next = fullPath[i+1]
          
          if (Math.hypot(curr.x - prev.x, curr.y - prev.y) < 5) continue
          if (Math.hypot(curr.x - next.x, curr.y - next.y) < 5) continue
          
          const isCollinearHorizontal = Math.abs(prev.y - curr.y) < 2 && Math.abs(curr.y - next.y) < 2
          const isCollinearVertical = Math.abs(prev.x - curr.x) < 2 && Math.abs(curr.x - next.x) < 2
          
          if (isCollinearHorizontal || isCollinearVertical) continue
          
          simplified.push(curr)
        }
        
        if (simplified.length !== currentDragPoints.length) {
           setEdges(edges => edges.map(edge => {
            if (edge.id === id) {
              return { ...edge, data: { ...edge.data, points: simplified } }
            }
            return edge
          }))
        }
      }
      
      // Dipatch event to trigger save and signal drag end
      window.dispatchEvent(new CustomEvent('astonish:edge-drag-stop'))
    }

    document.addEventListener('mousemove', handleMouseMove)
    document.addEventListener('mouseup', handleMouseUp)

  }, [points, sourceX, sourceY, targetX, targetY, id, setEdges, screenToFlowPosition])


  // 4. Calculate Handle Positions (Midpoints of segments)
  const handles: EdgeHandle[] = []
  const MIN_SEGMENT_LENGTH = 30
  
  const addHandleIfLongEnough = (p1: Point, p2: Point, index: number) => {
    const dist = Math.hypot(p2.x - p1.x, p2.y - p1.y)
    if (dist >= MIN_SEGMENT_LENGTH) {
      handles.push({
        x: (p1.x + p2.x) / 2,
        y: (p1.y + p2.y) / 2,
        index
      })
    }
  }
  
  // Segment 0: Source -> P[0]
  if (points.length > 0) {
    addHandleIfLongEnough({ x: sourceX, y: sourceY }, points[0], 0)
    for (let i = 0; i < points.length - 1; i++) {
      addHandleIfLongEnough(points[i], points[i+1], i + 1)
    }
    const lastP = points[points.length - 1]
    addHandleIfLongEnough(lastP, { x: targetX, y: targetY }, points.length)
  } else {
     // No points -> Source -> Target direct
     addHandleIfLongEnough({ x: sourceX, y: sourceY }, { x: targetX, y: targetY }, 0)
  }

  return (
    <>
      <BaseEdge 
        path={path} 
        markerEnd={markerEnd} 
        style={{
          ...style,
          strokeWidth: selected ? 3 : 2,
          stroke: selected ? 'var(--brand-strong)' : (style as CSSProperties)?.stroke as string || 'var(--brand)'
        }}
        interactionWidth={20}
        label={label}
        labelX={labelX}
        labelY={labelY}
        labelStyle={labelStyle}
        labelShowBg={labelShowBg}
        labelBgStyle={labelBgStyle}
        labelBgPadding={labelBgPadding}
        labelBgBorderRadius={labelBgBorderRadius}
      />
      
      <path
        d={path}
        fill="none"
        stroke="transparent"
        strokeWidth={20}
        style={{ cursor: 'pointer' }}
      />
      
      {selected && (
        <EdgeLabelRenderer>
          {handles.map((handle, i) => (
             <div
               key={i}
               className="nodrag nopan"
               style={{
                 position: 'absolute',
                 transform: `translate(-50%, -50%) translate(${handle.x}px, ${handle.y}px)`,
                 width: 12,
                 height: 12,
                 backgroundColor: 'var(--brand)',
                 border: '2px solid white',
                 borderRadius: 2,
                 cursor: 'grab',
                 pointerEvents: 'all',
                 zIndex: 2000
               }}
               onMouseDown={(e) => onHandleMouseDown(e, handle.index)}
             />
          ))}
        </EdgeLabelRenderer>
      )}
    </>
  )
}
