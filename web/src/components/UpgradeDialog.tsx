import { Download, Terminal, ExternalLink } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'

interface UpgradeDialogProps {
  info: { version: string; url: string }
  onClose: () => void
}

export default function UpgradeDialog({ info, onClose }: UpgradeDialogProps) {
  const updateOptions = [
    {
      title: 'Homebrew (Recommended)',
      icon: Terminal,
      command: 'brew upgrade SAP/astonish/astonish',
    },
    {
      title: 'Install Script',
      icon: Terminal,
      command: 'curl -sSL https://sap.github.io/astonish/install.sh | bash',
    },
  ]

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="z-[200] max-w-lg border-panel-border bg-panel-background shadow-[var(--shadow-elevated)]">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2 text-xl">
            <Download size={20} className="text-primary" />
            Update Available: {info.version}
          </DialogTitle>
          <DialogDescription>
            A new version of Astonish is available. Choose one of the methods below to update.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          {updateOptions.map(({ title, icon: Icon, command }) => (
            <div key={title} className="rounded-lg border bg-secondary/70 p-4">
              <div className="mb-2 flex items-center gap-2">
                <Icon size={18} className="text-primary" />
                <span className="font-medium text-foreground">{title}</span>
              </div>
              <code className="block rounded-md border bg-background px-3 py-2 font-mono text-sm text-secondary-foreground">
                {command}
              </code>
            </div>
          ))}

          <div className="rounded-lg border bg-secondary/70 p-4">
            <div className="mb-2 flex items-center gap-2">
              <Download size={18} className="text-primary" />
              <span className="font-medium text-foreground">Manual Download</span>
            </div>
            <Button variant="link" className="h-auto p-0 text-foreground" onClick={() => window.open(info.url, '_blank')}>
              <ExternalLink size={14} />
              Download from GitHub Releases
            </Button>
          </div>
        </div>

        <DialogFooter>
          <Button variant="secondary" onClick={onClose}>Close</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
