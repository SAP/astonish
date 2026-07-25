import { Play } from 'lucide-react'
import { type Node, type NodeProps } from '@xyflow/react'
import OverflowNode, { type NodeData } from './OverflowNode'

type StartNodeType = Node<NodeData, 'start'>

export default function StartNode({ id, data, selected }: NodeProps<StartNodeType>) {
  return (
    <OverflowNode 
      id={id}
      data={data} 
      selected={selected}
      icon={Play} 
      nodeType="Start"
      iconColor="var(--node-start)"
      hasTopHandle={false}
    />
  )
}
