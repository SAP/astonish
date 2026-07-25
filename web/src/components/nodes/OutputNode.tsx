import { MessageSquare } from 'lucide-react'
import { type Node, type NodeProps } from '@xyflow/react'
import OverflowNode, { type NodeData } from './OverflowNode'

type OutputNodeType = Node<NodeData, 'output'>

export default function OutputNode({ id, data, selected }: NodeProps<OutputNodeType>) {
  return (
    <OverflowNode 
      id={id}
      data={data} 
      selected={selected}
      icon={MessageSquare} 
      nodeType="Output"
      iconColor="var(--node-output)"
    />
  )
}
