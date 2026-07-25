import { AlertTriangle } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'

interface ConfirmDeleteModalProps {
  isOpen: boolean
  onClose: () => void
  onConfirm: () => void
  agentName: string
  isStoreFlow: boolean
}

/**
 * Confirmation dialog for deleting an agent or uninstalling a store flow.
 */
export default function ConfirmDeleteModal({ isOpen, onClose, onConfirm, agentName, isStoreFlow }: ConfirmDeleteModalProps) {
  const title = isStoreFlow ? 'Uninstall Flow' : 'Delete Agent'
  const actionText = isStoreFlow ? 'Uninstall Flow' : 'Delete Agent'
  const description = isStoreFlow
    ? 'This will remove the flow from your installed store flows.'
    : 'This will permanently remove the agent file from your system.'

  return (
    <Dialog open={isOpen} onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent className="max-w-md overflow-hidden p-0 shadow-[var(--shadow-elevated)]" showCloseButton={false}>
        <div className="bg-destructive px-6 py-5 text-destructive-foreground">
          <DialogHeader className="flex-row items-center gap-3 space-y-0 text-left">
            <div className="flex size-10 shrink-0 items-center justify-center rounded-xl bg-white/20">
              <AlertTriangle size={20} />
            </div>
            <div>
              <DialogTitle className="text-destructive-foreground">{title}</DialogTitle>
              <DialogDescription className="text-destructive-foreground/75">
                This action cannot be undone.
              </DialogDescription>
            </div>
          </DialogHeader>
        </div>

        <div className="space-y-6 p-6">
          <div className="space-y-2">
            <p className="text-secondary-foreground">
              Are you sure you want to {isStoreFlow ? 'uninstall' : 'delete'}{' '}
              <strong className="text-foreground">{agentName}</strong>?
            </p>
            <p className="text-sm text-muted-foreground">{description}</p>
          </div>

          <DialogFooter className="grid grid-cols-2 gap-3 sm:grid-cols-2">
            <Button type="button" variant="secondary" onClick={onClose}>
              Cancel
            </Button>
            <Button type="button" variant="destructive" onClick={onConfirm}>
              {actionText}
            </Button>
          </DialogFooter>
        </div>
      </DialogContent>
    </Dialog>
  )
}
