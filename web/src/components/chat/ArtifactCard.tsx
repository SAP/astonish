import { Download, Edit3, ExternalLink, FilePlus, FileText, Film } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

import type { ArtifactMessage } from './chatTypes'
import { getArtifactDownloadUrl } from '../../api/studioChat'

// Extracts filename from an absolute path
function getFileName(path: string): string {
  const parts = path.split('/')
  return parts[parts.length - 1] || path
}

// Gets a human-readable file extension label
function getFileType(path: string): string {
  const ext = path.split('.').pop()?.toLowerCase()
  const typeMap: Record<string, string> = {
    md: 'Markdown', txt: 'Text', json: 'JSON', yaml: 'YAML', yml: 'YAML',
    py: 'Python', go: 'Go', js: 'JavaScript', ts: 'TypeScript', tsx: 'TSX', jsx: 'JSX',
    html: 'HTML', css: 'CSS', sh: 'Shell', bash: 'Shell',
    csv: 'CSV', xml: 'XML', sql: 'SQL', toml: 'TOML',
    rs: 'Rust', rb: 'Ruby', java: 'Java', c: 'C', cpp: 'C++', h: 'Header',
    dockerfile: 'Dockerfile', makefile: 'Makefile',
    mp4: 'Video', webm: 'Video', mov: 'Video',
  }
  return typeMap[ext || ''] || (ext ? ext.toUpperCase() : 'File')
}

interface ArtifactCardProps {
  data: ArtifactMessage
  sessionId?: string | null
  onOpenInPanel?: (path: string) => void
}

// Inline artifact card showing a file that was created/modified by a tool.
// Displays the filename, path, and buttons to open in the Files panel or download.
export default function ArtifactCard({ data, sessionId, onOpenInPanel }: ArtifactCardProps) {
  const fileName = getFileName(data.path)
  const fileType = getFileType(data.path)
  const isEdit = data.toolName === 'edit_file'
  const isVideo = fileType === 'Video'

  const handleDownload = () => {
    const url = getArtifactDownloadUrl(data.path, sessionId || undefined)
    window.open(url, '_blank')
  }

  return (
    <div
      className={cn(
        'my-1.5 inline-flex max-w-md items-center gap-3 overflow-hidden rounded-lg border px-3 py-2',
        isVideo
          ? 'border-rose-500/35 bg-rose-500/10'
          : 'border-[color:var(--success)]/30 bg-[color:var(--success)]/10'
      )}
    >
      <div className={cn('flex size-8 items-center justify-center rounded', isVideo ? 'bg-rose-500/15' : 'bg-[color:var(--success)]/15')}>
        {isVideo ? (
          <Film size={16} className="text-rose-400" />
        ) : isEdit ? (
          <Edit3 size={16} className="text-[color:var(--success)]" />
        ) : (
          <FilePlus size={16} className="text-[color:var(--success)]" />
        )}
      </div>
      <div className="flex min-w-0 flex-1 flex-col">
        <div className="flex items-center gap-1.5">
          {isVideo
            ? <Film size={12} className="shrink-0 text-rose-400" />
            : <FileText size={12} className="shrink-0 text-[color:var(--success)]" />
          }
          <span className="truncate text-xs font-medium text-foreground">{fileName}</span>
          <span className="shrink-0 text-[10px] text-muted-foreground">{fileType}</span>
        </div>
        <span className="truncate text-[10px] text-muted-foreground" title={data.path}>{data.path}</span>
      </div>
      {onOpenInPanel && (
        <Button
          type="button"
          variant="ghost"
          size="icon"
          onClick={() => onOpenInPanel(data.path)}
          className="size-7 shrink-0 text-[color:var(--success)] hover:bg-[color:var(--success)]/15 hover:text-[color:var(--success)]"
          title="Open in Files panel"
          aria-label="Open in Files panel"
        >
          <ExternalLink />
        </Button>
      )}
      <Button
        type="button"
        variant="ghost"
        size="icon"
        onClick={handleDownload}
        className="size-7 shrink-0 text-[color:var(--success)] hover:bg-[color:var(--success)]/15 hover:text-[color:var(--success)]"
        title="Download file"
        aria-label="Download file"
      >
        <Download />
      </Button>
    </div>
  )
}
