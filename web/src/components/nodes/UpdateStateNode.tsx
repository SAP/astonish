import { Settings } from 'lucide-react'
import { type Node, type NodeProps } from '@xyflow/react'
import OverflowNode, { type NodeData } from './OverflowNode'

type UpdateStateNodeType = Node<NodeData, 'updateState'>

export default function UpdateStateNode({ id, data, selected }: NodeProps<UpdateStateNodeType>) {
  return (
    <OverflowNode 
      id={id}
      data={data} 
      selected={selected}
      icon={Settings} 
      nodeType="State"
      iconColor="var(--node-llm)"
    />
  )
}
