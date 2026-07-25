import { Square } from 'lucide-react'
import { type Node, type NodeProps } from '@xyflow/react'
import OverflowNode, { type NodeData } from './OverflowNode'

type EndNodeType = Node<NodeData, 'end'>

export default function EndNode({ data, selected }: NodeProps<EndNodeType>) {
  return (
    <OverflowNode 
      data={data} 
      selected={selected}
      icon={Square} 
      nodeType="End"
      iconColor="var(--node-end)"
      hasBottomHandle={false}
    />
  )
}
