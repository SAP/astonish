import { Wrench } from 'lucide-react'
import { type Node, type NodeProps } from '@xyflow/react'
import OverflowNode, { type NodeData } from './OverflowNode'

type ToolNodeType = Node<NodeData, 'tool'>

export default function ToolNode({ id, data, selected }: NodeProps<ToolNodeType>) {
  return (
    <OverflowNode 
      id={id}
      data={data} 
      selected={selected}
      icon={Wrench} 
      nodeType="Tool"
      iconColor="var(--node-tool)"
    />
  )
}
