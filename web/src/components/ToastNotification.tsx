import type { ToastData } from './AppTypes'

interface ToastNotificationProps {
  toast: ToastData
  onDismiss: () => void
}

export default function ToastNotification({ toast, onDismiss }: ToastNotificationProps) {
  return (
    <div 
      className="fixed bottom-6 right-6 z-[100] animate-slide-up"
      style={{ animation: 'slide-up 0.3s ease-out' }}
    >
      <div 
        className={`flex items-center gap-3 px-5 py-3 rounded-xl shadow-2xl backdrop-blur-sm ${
          toast.type === 'error' 
            ? 'bg-red-500/90 text-white' 
            : toast.type === 'info'
              ? 'bg-gradient-to-r from-primary to-[var(--brand-strong)] text-primary-foreground'
              : 'bg-gradient-to-r from-emerald-500/90 to-teal-500/90 text-white'
        } ${toast.persistent ? 'cursor-pointer hover:scale-[1.02] transition-transform' : ''}`}
        style={{ minWidth: '280px' }}
        onClick={() => toast.persistent && toast.action ? toast.action.onClick() : undefined}
      >
        {toast.type === 'error' ? (
          <svg className="w-5 h-5 flex-shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z" />
          </svg>
        ) : toast.type === 'info' ? (
          <svg className="w-5 h-5 flex-shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z" />
          </svg>
        ) : (
          <svg className="w-5 h-5 flex-shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z" />
          </svg>
        )}
        <span className="text-sm font-medium flex-1">{toast.message}</span>
        {toast.action && (
          <button
            onClick={(e) => { e.stopPropagation(); toast.action!.onClick() }}
            className="px-3 py-1 bg-white/20 hover:bg-white/30 rounded-lg text-sm font-medium transition-colors"
          >
            {toast.action.label}
          </button>
        )}
        <button 
          onClick={(e) => { e.stopPropagation(); onDismiss() }}
          className="ml-1 p-1 hover:bg-white/20 rounded-lg transition-colors"
        >
          <svg className="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M6 18L18 6M6 6l12 12" />
          </svg>
        </button>
      </div>
    </div>
  )
}
