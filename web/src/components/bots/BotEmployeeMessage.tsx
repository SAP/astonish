import ReactMarkdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'

/** The same markdown assistant content used in the transcript, beside exact controls. */
export default function BotEmployeeMessage({ children, role, components }: { children: string; role?: 'alert' | 'status'; components?: Components }) {
  return <div role={role} className="markdown-body text-sm text-foreground"><ReactMarkdown remarkPlugins={[remarkGfm]} components={components}>{children}</ReactMarkdown></div>
}
