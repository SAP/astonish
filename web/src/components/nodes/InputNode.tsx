import { Edit3 } from 'lucide-react'
import { type Node, type NodeProps } from '@xyflow/react'
import OverflowNode, { type NodeData } from './OverflowNode'

type InputNodeType = Node<NodeData, 'input'>

export default function InputNode({ id, data, selected }: NodeProps<InputNodeType>) {
  return (
    <OverflowNode 
      id={id}
      data={data} 
      selected={selected}
      icon={Edit3} 
      nodeType="Input"
      iconColor="var(--node-input)"
    />
  )
}
