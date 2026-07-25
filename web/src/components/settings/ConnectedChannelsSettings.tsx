import { useState, useEffect, useCallback, useRef } from 'react'
import { Plus, Trash2, Check, AlertCircle, Loader2, Radio, CheckCircle, XCircle, Edit2, X, Copy, ExternalLink } from 'lucide-react'
import { inputClass, inputStyle, labelStyle, hintStyle, sectionBorderStyle, saveButtonStyle } from './settingsApi'
import { useChannelLinkFlow } from '../../hooks/useChannelLinkFlow'

interface UserChannel {
  id: string
  user_id: string
  channel_type: string
  external_id: string
  display_name: string
  enabled: boolean
  verified: boolean
  verified_at?: string
  created_at: string
}

interface ChannelInfo {
  telegram: {
    bot_username: string
    configured: boolean
    connected: boolean
    enabled: boolean
    error: string
  }
  email?: {
    configured: boolean
    connected: boolean
    enabled: boolean
    error: string
    address?: string
  }
  slack?: {
    bot_user_id: string
    configured: boolean
    connected: boolean
    enabled: boolean
    error: string
  }
}

const channelIcons: Record<string, string> = {
  telegram: '✈️',
  email: '📧',
  slack: '💬',
}

const channelLabels: Record<string, string> = {
  telegram: 'Telegram',
  email: 'Email',
  slack: 'Slack',
}

export default function ConnectedChannelsSettings({ isAdmin = false }: { isAdmin?: boolean }) {
  const [channels, setChannels] = useState<UserChannel[]>([])
  const [channelInfo, setChannelInfo] = useState<ChannelInfo | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [success, setSuccess] = useState<string | null>(null)
  const [editingChannel, setEditingChannel] = useState<UserChannel | null>(null)
  const [actionLoading, setActionLoading] = useState<string | null>(null)

  // Link code flow state (Telegram) — via shared hook
  const [telegramLink, telegramActions] = useChannelLinkFlow({
    channelType: 'telegram',
    channels,
    onLinked: () => void fetchChannels(),
    onError: (msg) => setError(msg),
    onSuccess: (msg) => { setSuccess(msg); setTimeout(() => setSuccess(null), 5000) },
  })

  // Email link flow state (unique two-step flow — not extracted to hook)
  const [emailLinkStep, setEmailLinkStep] = useState<'idle' | 'enter_email' | 'enter_code'>('idle')
  const [emailInput, setEmailInput] = useState('')
  const [emailCodeInput, setEmailCodeInput] = useState('')
  const [emailLinkLoading, setEmailLinkLoading] = useState(false)
  const [emailLinkEmail, setEmailLinkEmail] = useState('') // the email we sent code to
  const [emailExpiresAt, setEmailExpiresAt] = useState<number | null>(null)
  const [emailTimeLeft, setEmailTimeLeft] = useState<number>(0)
  const emailTimerRef = useRef<ReturnType<typeof setInterval> | null>(null)
  const emailPollRef = useRef<ReturnType<typeof setInterval> | null>(null)

  // Slack link flow state — via shared hook
  const [slackLink, slackActions] = useChannelLinkFlow({
    channelType: 'slack',
    channels,
    onLinked: () => void fetchChannels(),
    onError: (msg) => setError(msg),
    onSuccess: (msg) => { setSuccess(msg); setTimeout(() => setSuccess(null), 5000) },
  })

  // Edit form state
  const [editForm, setEditForm] = useState({
    display_name: '',
    enabled: true,
  })

  // Derived state
  const botUsername = channelInfo?.telegram?.bot_username || ''
  const botConfigured = channelInfo?.telegram?.configured ?? false
  const emailConfigured = channelInfo?.email?.configured ?? false
  const slackConfigured = channelInfo?.slack?.configured ?? false
  const slackBotUserID = channelInfo?.slack?.bot_user_id || ''

  const fetchChannels = useCallback(async () => {
    try {
      const res = await fetch('/api/user/channels', { credentials: 'include' })
      if (!res.ok) throw new Error('Failed to load channels')
      const data = await res.json()
      setChannels(data.channels || [])
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setLoading(false)
    }
  }, [])

  const fetchChannelInfo = useCallback(async () => {
    try {
      const res = await fetch('/api/channels/info', { credentials: 'include' })
      if (!res.ok) throw new Error('Failed to load channel info')
      const data: ChannelInfo = await res.json()
      setChannelInfo(data)
    } catch {
      // If this fails, channelInfo stays null — UI will show "not configured"
    }
  }, [])

  useEffect(() => {
    void fetchChannels()
    void fetchChannelInfo()
  }, [fetchChannels, fetchChannelInfo])

  // --- Email linking handlers ---

  // Countdown timer for email code expiry
  useEffect(() => {
    if (emailExpiresAt === null) {
      setEmailTimeLeft(0)
      return
    }
    const update = () => {
      const remaining = Math.max(0, Math.floor((emailExpiresAt - Date.now()) / 1000))
      setEmailTimeLeft(remaining)
      if (remaining === 0) {
        setEmailLinkStep('idle')
        setEmailExpiresAt(null)
        if (emailPollRef.current) clearInterval(emailPollRef.current)
      }
    }
    update()
    emailTimerRef.current = setInterval(update, 1000)
    return () => { if (emailTimerRef.current) clearInterval(emailTimerRef.current) }
  }, [emailExpiresAt])

  const handleEmailSendCode = async () => {
    setEmailLinkLoading(true)
    setError(null)
    try {
      const res = await fetch('/api/user/channels/link-code', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({ channel_type: 'email', email: emailInput.trim() }),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({ error: 'Failed to send code' }))
        throw new Error(data.error || 'Failed to send verification email')
      }
      const data = await res.json()
      setEmailLinkEmail(data.email)
      setEmailLinkStep('enter_code')
      setEmailExpiresAt(Date.now() + (data.expires_in || 300) * 1000)
      setEmailCodeInput('')
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setEmailLinkLoading(false)
    }
  }

  const handleEmailVerifyCode = async () => {
    setEmailLinkLoading(true)
    setError(null)
    try {
      const res = await fetch('/api/user/channels/verify-email-code', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({ code: emailCodeInput.trim() }),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({ error: 'Verification failed' }))
        throw new Error(data.error || 'Failed to verify email code')
      }
      setEmailLinkStep('idle')
      setEmailExpiresAt(null)
      setEmailInput('')
      setEmailCodeInput('')
      setSuccess('Email linked and verified successfully!')
      await fetchChannels()
      setTimeout(() => setSuccess(null), 5000)
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setEmailLinkLoading(false)
    }
  }

  const handleEmailCancelLink = () => {
    setEmailLinkStep('idle')
    setEmailExpiresAt(null)
    setEmailCodeInput('')
    if (emailPollRef.current) clearInterval(emailPollRef.current)
  }

  // --- End email linking handlers ---

  const handleUnlink = async (id: string) => {
    setActionLoading(id)
    setError(null)
    try {
      const res = await fetch(`/api/user/channels/${id}`, {
        method: 'DELETE',
        credentials: 'include',
      })
      if (!res.ok) throw new Error('Failed to unlink channel')
      setSuccess('Channel unlinked')
      await fetchChannels()
      setTimeout(() => setSuccess(null), 3000)
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setActionLoading(null)
    }
  }

  const handleEditSave = async () => {
    if (!editingChannel) return
    setActionLoading(`edit-${editingChannel.id}`)
    setError(null)
    try {
      const res = await fetch(`/api/user/channels/${editingChannel.id}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify(editForm),
      })
      if (!res.ok) throw new Error('Failed to update channel')
      setSuccess('Channel updated')
      setEditingChannel(null)
      await fetchChannels()
      setTimeout(() => setSuccess(null), 3000)
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setActionLoading(null)
    }
  }

  const startEdit = (ch: UserChannel) => {
    setEditingChannel(ch)
    setEditForm({
      display_name: ch.display_name,
      enabled: ch.enabled,
    })
  }

  const formatTime = (secs: number) => {
    const m = Math.floor(secs / 60)
    const s = secs % 60
    return `${m}:${s.toString().padStart(2, '0')}`
  }

  const hasTelegram = channels.some(c => c.channel_type === 'telegram' && c.verified)
  const hasEmail = channels.some(c => c.channel_type === 'email' && c.verified)
  const hasSlack = channels.some(c => c.channel_type === 'slack' && c.verified)

  return (
    <div className="max-w-xl space-y-6">
      {/* Header */}
      <div>
        <h3 className="text-base font-semibold" style={{ color: 'var(--text-primary)' }}>
          Channels
        </h3>
        <p className="text-xs mt-1" style={hintStyle}>
          {isAdmin
            ? 'Manage channel infrastructure and link your personal messaging accounts.'
            : 'Link your messaging accounts to receive scheduled job results and interact with the AI agent via external channels.'}
        </p>
      </div>

      {/* Channel Infrastructure Status — visible to all users so they know which
          channels are available for linking. Admin-only setup hints below. */}
      {channelInfo && (
        <div className="rounded-lg border p-4 space-y-3" style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border-color)' }}>
          <h4 className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>Channel Infrastructure</h4>
          <div className="space-y-2">
            {/* Telegram status */}
            <div className="flex items-center justify-between py-1.5">
              <div className="flex items-center gap-2">
                <span className="text-base">✈️</span>
                <span className="text-sm" style={{ color: 'var(--text-primary)' }}>Telegram</span>
                {channelInfo.telegram?.bot_username && (
                  <span className="text-xs font-mono" style={hintStyle}>@{channelInfo.telegram.bot_username}</span>
                )}
              </div>
              <div className="flex items-center gap-1.5">
                {(channelInfo.telegram?.connected || channelInfo.telegram?.enabled) && !channelInfo.telegram?.error ? (
                  <span className="flex items-center gap-1 text-xs" style={{ color: '#22c55e' }}>
                    <CheckCircle size={12} /> Running
                  </span>
                ) : channelInfo.telegram?.configured ? (
                  <span className="flex items-center gap-1 text-xs" style={{ color: '#f59e0b' }}>
                    <AlertCircle size={12} /> Configured (not running)
                  </span>
                ) : (
                  <span className="flex items-center gap-1 text-xs" style={{ color: 'var(--text-muted)' }}>
                    <XCircle size={12} /> Not configured
                  </span>
                )}
              </div>
            </div>
            {channelInfo.telegram?.error && (
              <div className="ml-7 text-xs px-2 py-1 rounded" style={{ background: 'rgba(239, 68, 68, 0.1)', color: '#f87171' }}>
                {channelInfo.telegram.error}
              </div>
            )}

            {/* Email status */}
            {channelInfo.email && (
              <>
                <div className="flex items-center justify-between py-1.5">
                  <div className="flex items-center gap-2">
                    <span className="text-base">📧</span>
                    <span className="text-sm" style={{ color: 'var(--text-primary)' }}>Email</span>
                    {channelInfo.email.address && (
                      <span className="text-xs font-mono" style={hintStyle}>{channelInfo.email.address}</span>
                    )}
                  </div>
                  <div className="flex items-center gap-1.5">
                    {(channelInfo.email.connected || channelInfo.email.enabled) && !channelInfo.email.error ? (
                      <span className="flex items-center gap-1 text-xs" style={{ color: '#22c55e' }}>
                        <CheckCircle size={12} /> Running
                      </span>
                    ) : channelInfo.email.configured ? (
                      <span className="flex items-center gap-1 text-xs" style={{ color: '#f59e0b' }}>
                        <AlertCircle size={12} /> Configured (not running)
                      </span>
                    ) : (
                      <span className="flex items-center gap-1 text-xs" style={{ color: 'var(--text-muted)' }}>
                        <XCircle size={12} /> Not configured
                      </span>
                    )}
                  </div>
                </div>
                {channelInfo.email.error && (
                  <div className="ml-7 text-xs px-2 py-1 rounded" style={{ background: 'rgba(239, 68, 68, 0.1)', color: '#f87171' }}>
                    {channelInfo.email.error}
                  </div>
                )}
              </>
            )}

            {/* Slack status */}
            {channelInfo.slack && (
              <>
                <div className="flex items-center justify-between py-1.5">
                  <div className="flex items-center gap-2">
                    <span className="text-base">💬</span>
                    <span className="text-sm" style={{ color: 'var(--text-primary)' }}>Slack</span>
                    {channelInfo.slack.bot_user_id && (
                      <span className="text-xs font-mono" style={hintStyle}>{channelInfo.slack.bot_user_id}</span>
                    )}
                  </div>
                  <div className="flex items-center gap-1.5">
                    {(channelInfo.slack.connected || channelInfo.slack.enabled) && !channelInfo.slack.error ? (
                      <span className="flex items-center gap-1 text-xs" style={{ color: '#22c55e' }}>
                        <CheckCircle size={12} /> Running
                      </span>
                    ) : channelInfo.slack.configured ? (
                      <span className="flex items-center gap-1 text-xs" style={{ color: '#f59e0b' }}>
                        <AlertCircle size={12} /> Configured (not running)
                      </span>
                    ) : (
                      <span className="flex items-center gap-1 text-xs" style={{ color: 'var(--text-muted)' }}>
                        <XCircle size={12} /> Not configured
                      </span>
                    )}
                  </div>
                </div>
                {channelInfo.slack.error && (
                  <div className="ml-7 text-xs px-2 py-1 rounded" style={{ background: 'rgba(239, 68, 68, 0.1)', color: '#f87171' }}>
                    {channelInfo.slack.error}
                  </div>
                )}
              </>
            )}
          </div>
          {isAdmin && (
            <p className="text-xs pt-2 border-t" style={{ ...sectionBorderStyle, color: 'var(--text-muted)' }}>
              To set up or reconfigure channels, use the CLI: <code className="px-1 py-0.5 rounded text-xs" style={{ background: 'var(--bg-tertiary)' }}>astonish channels setup [telegram|slack|email]</code>
            </p>
          )}
        </div>
      )}

      {/* Status banners */}
      {error && (
        <div className="flex items-center gap-2 p-3 rounded-lg text-sm"
          style={{ background: 'rgba(239, 68, 68, 0.1)', color: 'var(--danger)' }}>
          <AlertCircle size={14} /> {error}
          <button onClick={() => setError(null)} className="ml-auto"><X size={14} /></button>
        </div>
      )}
      {success && (
        <div className="flex items-center gap-2 p-3 rounded-lg text-sm"
          style={{ background: 'rgba(34, 197, 94, 0.1)', color: '#22c55e' }}>
          <Check size={14} /> {success}
        </div>
      )}

      {/* Loading */}
      {loading && (
        <div className="flex items-center gap-2 py-6">
          <Loader2 size={16} className="animate-spin" style={{ color: 'var(--accent)' }} />
          <span className="text-sm" style={hintStyle}>Loading connected channels...</span>
        </div>
      )}

      {/* Channel List */}
      {!loading && channels.length === 0 && !telegramLink.isActive && (
        <div className="py-6 text-center rounded-lg border" style={{ borderColor: 'var(--border-color)', background: 'var(--bg-secondary)' }}>
          <Radio size={28} className="mx-auto mb-2" style={{ color: 'var(--text-muted)', opacity: 0.5 }} />
          <p className="text-sm" style={hintStyle}>No channels connected yet.</p>
          <p className="text-xs mt-1" style={hintStyle}>
            Link your Telegram, Slack, or Email to receive notifications and interact with the AI.
          </p>
        </div>
      )}

      {!loading && channels.length > 0 && (
        <div className="space-y-2">
          {channels.map(ch => (
            <div
              key={ch.id}
              className="flex items-center gap-3 p-3 rounded-lg border"
              style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border-color)' }}
            >
              {/* Channel icon */}
              <span className="text-lg">{channelIcons[ch.channel_type] || '📡'}</span>

              {/* Channel info */}
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <span className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>
                    {channelLabels[ch.channel_type] || ch.channel_type}
                  </span>
                  {ch.display_name && (
                    <span className="text-xs" style={hintStyle}>{ch.display_name}</span>
                  )}
                  {ch.verified ? (
                    <span className="flex items-center gap-0.5 text-xs" style={{ color: '#22c55e' }}>
                      <CheckCircle size={11} /> Verified
                    </span>
                  ) : (
                    <span className="flex items-center gap-0.5 text-xs" style={{ color: '#f59e0b' }}>
                      <XCircle size={11} /> Unverified
                    </span>
                  )}
                  {!ch.enabled && (
                    <span className="text-xs px-1.5 py-0.5 rounded" style={{ background: 'rgba(239, 68, 68, 0.1)', color: '#f87171' }}>
                      Disabled
                    </span>
                  )}
                </div>
                <div className="text-xs font-mono mt-0.5" style={hintStyle}>
                  ID: {ch.external_id}
                </div>
              </div>

              {/* Actions */}
              <div className="flex items-center gap-1">
                <button
                  onClick={() => startEdit(ch)}
                  className="p-1.5 rounded-lg transition-colors hover:bg-gray-600/30"
                  style={{ color: 'var(--text-muted)' }}
                  title="Edit"
                >
                  <Edit2 size={14} />
                </button>
                <button
                  onClick={() => handleUnlink(ch.id)}
                  disabled={actionLoading === ch.id}
                  className="p-1.5 rounded-lg transition-colors hover:bg-red-600/20 disabled:opacity-50"
                  style={{ color: 'var(--text-muted)' }}
                  title="Unlink"
                >
                  {actionLoading === ch.id ? <Loader2 size={14} className="animate-spin" /> : <Trash2 size={14} />}
                </button>
              </div>
            </div>
          ))}
        </div>
      )}

      {/* Link Code Flow */}
      {telegramLink.isActive && botUsername && (
        <div className="rounded-lg border p-5 space-y-4" style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border-color)' }}>
          <div className="flex items-center justify-between">
            <h4 className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>Link Telegram</h4>
            <button onClick={telegramActions.cancel} className="p-1 rounded" style={{ color: 'var(--text-muted)' }}>
              <X size={16} />
            </button>
          </div>

          {/* Bot identity — prominent */}
          <div className="text-center">
            <p className="text-sm font-medium mb-1" style={{ color: 'var(--text-primary)' }}>
              Send this command to <span style={{ color: 'var(--accent)' }}>@{botUsername}</span>:
            </p>
          </div>

          {/* Code display */}
          <div className="space-y-3">
            <div className="text-center">
              <div
                className="inline-flex items-center gap-3 px-5 py-3 rounded-lg font-mono text-lg tracking-widest select-all"
                style={{ background: 'var(--bg-tertiary)', border: '1px solid var(--border-color)', color: 'var(--text-primary)' }}
              >
                <span>/link {telegramLink.code}</span>
                <button
                  onClick={telegramActions.copyCode}
                  className="p-1 rounded transition-colors hover:bg-gray-600/30"
                  style={{ color: telegramLink.copied ? '#22c55e' : 'var(--text-muted)' }}
                  title="Copy to clipboard"
                >
                  {telegramLink.copied ? <Check size={16} /> : <Copy size={16} />}
                </button>
              </div>
            </div>

            {/* Timer */}
            <div className="text-center">
              <span className="text-xs" style={{ color: telegramLink.timeLeft < 60 ? '#f59e0b' : 'var(--text-muted)' }}>
                Expires in {formatTime(telegramLink.timeLeft)}
              </span>
            </div>

            {/* Bot deep link — always visible since we gate on botUsername */}
            <div className="text-center">
              <a
                href={`https://t.me/${botUsername}`}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-2 px-4 py-2 rounded-lg text-sm font-medium transition-all hover:scale-[1.02]"
                style={{ background: '#2AABEE', color: 'white' }}
              >
                <ExternalLink size={14} />
                Open @{botUsername} in Telegram
              </a>
            </div>

            {/* Instructions */}
            <div className="space-y-1 pt-2 border-t" style={{ borderColor: 'var(--border-color)' }}>
              <p className="text-xs font-medium" style={{ color: 'var(--text-secondary)' }}>Steps:</p>
              <ol className="text-xs space-y-1 list-decimal list-inside" style={hintStyle}>
                <li>Open <strong>@{botUsername}</strong> in Telegram (click the button above)</li>
                <li>Send: <code className="px-1 py-0.5 rounded text-xs" style={{ background: 'var(--bg-tertiary)' }}>/link {telegramLink.code}</code></li>
                <li>Wait for confirmation — this page updates automatically</li>
              </ol>
            </div>

            {/* Polling indicator */}
            <div className="flex items-center justify-center gap-2 pt-2">
              <Loader2 size={12} className="animate-spin" style={{ color: 'var(--accent)' }} />
              <span className="text-xs" style={hintStyle}>Waiting for verification...</span>
            </div>
          </div>
        </div>
      )}

      {/* Link Button — only when bot is configured and user has no verified telegram */}
      {!telegramLink.isActive && !loading && !hasTelegram && botConfigured && (
        <button
          onClick={telegramActions.generateCode}
          disabled={telegramLink.loading}
          className="flex items-center gap-2 px-4 py-2 rounded-lg text-sm font-medium transition-all hover:scale-[1.02] disabled:opacity-50"
          style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)', border: '1px solid var(--border-color)' }}
        >
          {telegramLink.loading ? <Loader2 size={16} className="animate-spin" /> : <Plus size={16} />}
          Link Telegram
          {botUsername && <span className="text-xs ml-1" style={hintStyle}>via @{botUsername}</span>}
        </button>
      )}

      {/* Bot not configured warning */}
      {!telegramLink.isActive && !loading && !hasTelegram && !botConfigured && channelInfo !== null && (
        <div className="flex items-start gap-3 p-4 rounded-lg border"
          style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border-color)' }}>
          <AlertCircle size={18} className="mt-0.5 flex-shrink-0" style={{ color: '#f59e0b' }} />
          <div>
            <p className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>
              Telegram bot is not connected
            </p>
            <p className="text-xs mt-1" style={hintStyle}>
              {channelInfo.telegram.error
                ? `Reason: ${channelInfo.telegram.error}. `
                : ''}
              Contact your administrator to configure the bot token
              {!channelInfo.telegram.enabled && ' and enable the Telegram channel'}.
            </p>
            <p className="text-xs mt-1 font-mono" style={hintStyle}>
              Run: astonish channels setup telegram
            </p>
          </div>
        </div>
      )}

      {/* Already linked note */}
      {!telegramLink.isActive && !loading && hasTelegram && (
        <p className="text-xs" style={hintStyle}>
          To link a different Telegram account, unlink the current one first.
        </p>
      )}

      {/* --- Email Linking --- */}

      {/* Email Link Code Flow */}
      {emailLinkStep === 'enter_code' && (
        <div className="rounded-lg border p-5 space-y-4" style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border-color)' }}>
          <div className="flex items-center justify-between">
            <h4 className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>Verify Email</h4>
            <button onClick={handleEmailCancelLink} className="p-1 rounded" style={{ color: 'var(--text-muted)' }}>
              <X size={16} />
            </button>
          </div>

          <div className="text-center">
            <p className="text-sm" style={{ color: 'var(--text-primary)' }}>
              A verification code was sent to <span className="font-medium" style={{ color: 'var(--accent)' }}>{emailLinkEmail}</span>
            </p>
            <p className="text-xs mt-1" style={hintStyle}>Check your inbox and enter the 6-character code below.</p>
          </div>

          <div className="space-y-3">
            <div className="flex items-center gap-2 justify-center">
              <input
                type="text"
                value={emailCodeInput}
                onChange={(e) => setEmailCodeInput(e.target.value.toUpperCase().slice(0, 6))}
                placeholder="ABC123"
                className="w-36 text-center font-mono text-lg tracking-widest px-4 py-2 rounded-lg"
                style={{ background: 'var(--bg-tertiary)', border: '1px solid var(--border-color)', color: 'var(--text-primary)' }}
                maxLength={6}
                autoFocus
              />
              <button
                onClick={handleEmailVerifyCode}
                disabled={emailLinkLoading || emailCodeInput.length < 6}
                className="flex items-center gap-2 px-4 py-2 rounded-lg text-white text-sm font-medium transition-all disabled:opacity-50"
                style={{ background: 'var(--accent)', border: 'none' }}
              >
                {emailLinkLoading ? <Loader2 size={14} className="animate-spin" /> : <Check size={14} />}
                Verify
              </button>
            </div>

            {/* Timer */}
            <div className="text-center">
              <span className="text-xs" style={{ color: emailTimeLeft < 60 ? '#f59e0b' : 'var(--text-muted)' }}>
                Expires in {formatTime(emailTimeLeft)}
              </span>
            </div>

            <div className="text-center">
              <button
                onClick={handleEmailSendCode}
                disabled={emailLinkLoading}
                className="text-xs underline"
                style={{ color: 'var(--text-muted)' }}
              >
                Resend code
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Email enter address step */}
      {emailLinkStep === 'enter_email' && (
        <div className="rounded-lg border p-5 space-y-4" style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border-color)' }}>
          <div className="flex items-center justify-between">
            <h4 className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>Link Email</h4>
            <button onClick={() => setEmailLinkStep('idle')} className="p-1 rounded" style={{ color: 'var(--text-muted)' }}>
              <X size={16} />
            </button>
          </div>

          <p className="text-xs" style={hintStyle}>
            Enter your email address. We'll send a verification code to confirm you own it.
          </p>

          <div className="flex items-center gap-2">
            <input
              type="email"
              value={emailInput}
              onChange={(e) => setEmailInput(e.target.value)}
              placeholder="you@example.com"
              className={inputClass + ' flex-1'}
              style={inputStyle}
              autoFocus
              onKeyDown={(e) => { if (e.key === 'Enter' && emailInput.includes('@')) handleEmailSendCode() }}
            />
            <button
              onClick={handleEmailSendCode}
              disabled={emailLinkLoading || !emailInput.includes('@')}
              className="flex items-center gap-2 px-4 py-2 rounded-lg text-white text-sm font-medium transition-all disabled:opacity-50"
              style={{ background: 'var(--accent)', border: 'none' }}
            >
              {emailLinkLoading ? <Loader2 size={14} className="animate-spin" /> : null}
              Send Code
            </button>
          </div>
        </div>
      )}

      {/* Link Email Button — only when email channel is configured and user has no verified email */}
      {emailLinkStep === 'idle' && !loading && !hasEmail && emailConfigured && (
        <button
          onClick={() => setEmailLinkStep('enter_email')}
          className="flex items-center gap-2 px-4 py-2 rounded-lg text-sm font-medium transition-all hover:scale-[1.02]"
          style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)', border: '1px solid var(--border-color)' }}
        >
          <Plus size={16} />
          Link Email
        </button>
      )}

      {/* Email not configured warning */}
      {emailLinkStep === 'idle' && !loading && !hasEmail && !emailConfigured && channelInfo !== null && channelInfo.email && (
        <div className="flex items-start gap-3 p-4 rounded-lg border"
          style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border-color)' }}>
          <AlertCircle size={18} className="mt-0.5 flex-shrink-0" style={{ color: 'var(--text-muted)' }} />
          <div>
            <p className="text-sm" style={{ color: 'var(--text-secondary)' }}>
              Email channel is not configured
            </p>
            <p className="text-xs mt-1" style={hintStyle}>
              Contact your administrator to set up the email channel.
            </p>
          </div>
        </div>
      )}

      {/* Already linked email note */}
      {emailLinkStep === 'idle' && !loading && hasEmail && (
        <p className="text-xs" style={hintStyle}>
          To link a different email address, unlink the current one first.
        </p>
      )}

      {/* --- Slack Linking --- */}

      {/* Slack Link Code Flow */}
      {slackLink.isActive && (
        <div className="rounded-lg border p-5 space-y-4" style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border-color)' }}>
          <div className="flex items-center justify-between">
            <h4 className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>Link Slack</h4>
            <button onClick={slackActions.cancel} className="p-1 rounded" style={{ color: 'var(--text-muted)' }}>
              <X size={16} />
            </button>
          </div>

          {/* Instructions */}
          <div className="text-center">
            <p className="text-sm font-medium mb-1" style={{ color: 'var(--text-primary)' }}>
              Send this command to the bot via DM in Slack:
            </p>
          </div>

          {/* Code display */}
          <div className="space-y-3">
            <div className="text-center">
              <div
                className="inline-flex items-center gap-3 px-5 py-3 rounded-lg font-mono text-lg tracking-widest select-all"
                style={{ background: 'var(--bg-tertiary)', border: '1px solid var(--border-color)', color: 'var(--text-primary)' }}
              >
                <span>/link {slackLink.code}</span>
                <button
                  onClick={slackActions.copyCode}
                  className="p-1 rounded transition-colors hover:bg-gray-600/30"
                  style={{ color: slackLink.copied ? '#22c55e' : 'var(--text-muted)' }}
                  title="Copy to clipboard"
                >
                  {slackLink.copied ? <Check size={16} /> : <Copy size={16} />}
                </button>
              </div>
            </div>

            {/* Timer */}
            <div className="text-center">
              <span className="text-xs" style={{ color: slackLink.timeLeft < 60 ? '#f59e0b' : 'var(--text-muted)' }}>
                Expires in {formatTime(slackLink.timeLeft)}
              </span>
            </div>

            {/* Instructions */}
            <div className="space-y-1 pt-2 border-t" style={{ borderColor: 'var(--border-color)' }}>
              <p className="text-xs font-medium" style={{ color: 'var(--text-secondary)' }}>Steps:</p>
              <ol className="text-xs space-y-1 list-decimal list-inside" style={hintStyle}>
                <li>Open your Slack workspace and find the bot in your sidebar (or Apps section)</li>
                <li>Send a direct message: <code className="px-1 py-0.5 rounded text-xs" style={{ background: 'var(--bg-tertiary)' }}>/link {slackLink.code}</code></li>
                <li>Wait for confirmation — this page updates automatically</li>
              </ol>
              {slackBotUserID && (
                <p className="text-xs mt-1 font-mono" style={hintStyle}>
                  Bot ID: {slackBotUserID}
                </p>
              )}
            </div>

            {/* Polling indicator */}
            <div className="flex items-center justify-center gap-2 pt-2">
              <Loader2 size={12} className="animate-spin" style={{ color: 'var(--accent)' }} />
              <span className="text-xs" style={hintStyle}>Waiting for verification...</span>
            </div>
          </div>
        </div>
      )}

      {/* Link Slack Button — only when Slack is configured and user has no verified Slack channel */}
      {!slackLink.isActive && !loading && !hasSlack && slackConfigured && (
        <button
          onClick={slackActions.generateCode}
          disabled={slackLink.loading}
          className="flex items-center gap-2 px-4 py-2 rounded-lg text-sm font-medium transition-all hover:scale-[1.02] disabled:opacity-50"
          style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)', border: '1px solid var(--border-color)' }}
        >
          {slackLink.loading ? <Loader2 size={16} className="animate-spin" /> : <Plus size={16} />}
          Link Slack
        </button>
      )}

      {/* Slack not configured warning */}
      {!slackLink.isActive && !loading && !hasSlack && !slackConfigured && channelInfo !== null && channelInfo.slack && (
        <div className="flex items-start gap-3 p-4 rounded-lg border"
          style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border-color)' }}>
          <AlertCircle size={18} className="mt-0.5 flex-shrink-0" style={{ color: '#f59e0b' }} />
          <div>
            <p className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>
              Slack bot is not connected
            </p>
            <p className="text-xs mt-1" style={hintStyle}>
              {channelInfo.slack.error
                ? `Reason: ${channelInfo.slack.error}. `
                : ''}
              Contact your administrator to configure the Slack bot token
              {!channelInfo.slack.enabled && ' and enable the Slack channel'}.
            </p>
            <p className="text-xs mt-1 font-mono" style={hintStyle}>
              Run: astonish channels setup slack
            </p>
          </div>
        </div>
      )}

      {/* Already linked Slack note */}
      {!slackLink.isActive && !loading && hasSlack && (
        <p className="text-xs" style={hintStyle}>
          To link a different Slack account, unlink the current one first.
        </p>
      )}

      {/* Edit Modal */}
      {editingChannel && (
        <div className="fixed inset-0 z-50 flex items-center justify-center p-4" style={{ background: 'rgba(0,0,0,0.7)' }}>
          <div className="rounded-xl w-full max-w-sm p-6 shadow-2xl"
            style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
            <h3 className="text-lg font-semibold mb-4" style={{ color: 'var(--text-primary)' }}>
              Edit {channelLabels[editingChannel.channel_type] || editingChannel.channel_type} Channel
            </h3>

            <div className="space-y-4">
              <div>
                <label className="block text-sm font-medium mb-2" style={labelStyle}>Display Name</label>
                <input
                  type="text"
                  value={editForm.display_name}
                  onChange={(e) => setEditForm({ ...editForm, display_name: e.target.value })}
                  className={inputClass}
                  style={inputStyle}
                />
              </div>

              <div className="flex items-center justify-between">
                <div>
                  <label className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>Enabled</label>
                  <p className="text-xs" style={hintStyle}>Disable to temporarily stop receiving messages.</p>
                </div>
                <button
                  onClick={() => setEditForm({ ...editForm, enabled: !editForm.enabled })}
                  className="relative w-11 h-6 rounded-full transition-colors"
                  style={{
                    background: editForm.enabled ? 'var(--brand)' : 'var(--bg-tertiary)',
                    border: `1px solid ${editForm.enabled ? 'var(--brand)' : 'var(--border-color)'}`
                  }}
                >
                  <span
                    className="absolute top-0.5 left-0.5 w-4 h-4 rounded-full transition-transform bg-white"
                    style={{ transform: editForm.enabled ? 'translateX(20px)' : 'translateX(0)' }}
                  />
                </button>
              </div>
            </div>

            <div className="flex justify-end gap-3 mt-6">
              <button
                onClick={() => setEditingChannel(null)}
                className="px-4 py-2 rounded-lg text-sm font-medium"
                style={{ color: 'var(--text-secondary)', background: 'var(--bg-tertiary)', border: '1px solid var(--border-color)' }}
              >
                Cancel
              </button>
              <button
                onClick={handleEditSave}
                disabled={actionLoading === `edit-${editingChannel.id}`}
                className="flex items-center gap-2 px-4 py-2 rounded-lg text-white text-sm font-medium transition-all disabled:opacity-50"
                style={saveButtonStyle}
              >
                {actionLoading === `edit-${editingChannel.id}` ? <Loader2 size={14} className="animate-spin" /> : <Check size={14} />}
                Save
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Info */}
      <div className="border-t pt-4" style={sectionBorderStyle}>
        <p className="text-xs" style={hintStyle}>
          {isAdmin
            ? 'Channel infrastructure is managed via CLI. Users link their accounts via this panel. Verified channels receive scheduled job results and allow direct AI interaction.'
            : 'Verified channels receive scheduled job results and allow you to interact with the AI agent directly via Telegram, Slack, or Email. The linking process is automatic \u2014 just send the code to the bot and you\'re connected.'}
        </p>
      </div>
    </div>
  )
}
