import React, { useState, useEffect } from 'react'
import { LogIn, UserPlus, Loader2, AlertCircle, Eye, EyeOff, ExternalLink, Mail, ShieldCheck, Clock } from 'lucide-react'
import { getSetupStatus, verifyEmail, resendVerification, type SetupStatus } from '../api/auth'

interface LoginPageProps {
  onLogin: (email: string, password: string) => Promise<void>
  onRegister: (email: string, password: string, displayName: string) => Promise<void>
  /** Set externally when the login/register response indicates verification is required */
  pendingVerificationEmail?: string | null
  /** Set externally when login response indicates no team membership */
  noTeamMembership?: boolean
}

export default function LoginPage({ onLogin, onRegister, pendingVerificationEmail, noTeamMembership }: LoginPageProps) {
  const [mode, setMode] = useState<'login' | 'register' | 'verify' | 'no_team'>('login')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [verificationCode, setVerificationCode] = useState('')
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')
  const [loading, setLoading] = useState(false)
  const [showPassword, setShowPassword] = useState(false)
  const [setupStatus, setSetupStatus] = useState<SetupStatus | null>(null)
  const [ssoProviders, setSsoProviders] = useState<Array<{id: string, name: string}>>([])
  const [resendCooldown, setResendCooldown] = useState(0)

  // Respond to external state changes
  useEffect(() => {
    if (pendingVerificationEmail) {
      setEmail(pendingVerificationEmail)
      setMode('verify')
    }
  }, [pendingVerificationEmail])

  useEffect(() => {
    if (noTeamMembership) {
      setMode('no_team')
    }
  }, [noTeamMembership])

  // Resend cooldown timer
  useEffect(() => {
    if (resendCooldown <= 0) return
    const timer = setTimeout(() => setResendCooldown(c => c - 1), 1000)
    return () => clearTimeout(timer)
  }, [resendCooldown])

  useEffect(() => {
    getSetupStatus().then(status => {
      setSetupStatus(status)
      // If not initialized, default to register mode
      if (!status.initialized) {
        setMode('register')
      }
    }).catch(() => {
      // Failed to check — show login by default
    })

    // Fetch available SSO providers (retry up to 3 times on failure)
    const fetchSSOProviders = async () => {
      for (let attempt = 0; attempt < 3; attempt++) {
        try {
          const res = await fetch('/api/auth/sso/providers')
          if (!res.ok) throw new Error(`HTTP ${res.status}`)
          const data = await res.json()
          if (data.providers && data.providers.length > 0) {
            setSsoProviders(data.providers)
            return
          }
          // Got empty providers with 200 — genuinely no providers configured, stop retrying
          return
        } catch {
          // Transient failure — retry after delay
          if (attempt < 2) await new Promise(r => setTimeout(r, 1000))
        }
      }
    }
    fetchSSOProviders()
  }, [])

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    setSuccess('')
    setLoading(true)

    try {
      if (mode === 'register') {
        await onRegister(email, password, displayName)
      } else {
        await onLogin(email, password)
      }
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : 'An error occurred'
      setError(message)
    } finally {
      setLoading(false)
    }
  }

  const handleVerifyEmail = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    setSuccess('')
    setLoading(true)

    try {
      const result = await verifyEmail(email, verificationCode)
      setSuccess(result.message)
      // After successful verification, switch to login mode
      setTimeout(() => {
        setMode('login')
        setSuccess('')
        setVerificationCode('')
      }, 2000)
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : 'Verification failed'
      setError(message)
    } finally {
      setLoading(false)
    }
  }

  const handleResendCode = async () => {
    if (resendCooldown > 0) return
    setError('')
    try {
      const result = await resendVerification(email)
      setSuccess(result.message)
      setResendCooldown(60) // 60 second cooldown
    } catch {
      setError('Failed to resend verification code')
    }
  }

  const isFirstSetup = setupStatus && !setupStatus.initialized
  const canRegister = setupStatus?.allow_registration !== false

  // --- Verification Code UI ---
  if (mode === 'verify') {
    return (
      <div
        className="fixed inset-0 z-50 flex items-center justify-center"
        style={{ background: 'var(--bg-primary)' }}
      >
        <div
          className="w-full max-w-md mx-auto p-8 rounded-2xl shadow-2xl"
          style={{
            background: 'var(--bg-secondary)',
            border: '1px solid var(--border-color)',
          }}
        >
          <div className="text-center mb-8">
            <div
              className="inline-flex items-center justify-center w-16 h-16 rounded-2xl mb-4"
              style={{
                background: 'linear-gradient(135deg, var(--brand) 0%, var(--brand-strong) 100%)',
              }}
            >
              <Mail size={28} className="text-white" />
            </div>
            <h1
              className="text-2xl font-bold"
              style={{ color: 'var(--text-primary)' }}
            >
              Verify Your Email
            </h1>
            <p
              className="mt-2 text-sm"
              style={{ color: 'var(--text-muted)' }}
            >
              We sent a 6-digit code to <strong style={{ color: 'var(--text-secondary)' }}>{email}</strong>
            </p>
          </div>

          {error && (
            <div
              className="flex items-center gap-2 p-3 rounded-lg mb-4 text-sm"
              style={{
                background: 'rgba(239, 68, 68, 0.1)',
                color: '#ef4444',
                border: '1px solid rgba(239, 68, 68, 0.2)',
              }}
            >
              <AlertCircle size={16} />
              <span>{error}</span>
            </div>
          )}

          {success && (
            <div
              className="flex items-center gap-2 p-3 rounded-lg mb-4 text-sm"
              style={{
                background: 'rgba(34, 197, 94, 0.1)',
                color: '#22c55e',
                border: '1px solid rgba(34, 197, 94, 0.2)',
              }}
            >
              <ShieldCheck size={16} />
              <span>{success}</span>
            </div>
          )}

          <form onSubmit={handleVerifyEmail} className="space-y-4">
            <div>
              <label
                className="block text-sm font-medium mb-1.5"
                style={{ color: 'var(--text-secondary)' }}
              >
                Verification Code
              </label>
              <input
                type="text"
                value={verificationCode}
                onChange={e => setVerificationCode(e.target.value.replace(/\D/g, '').slice(0, 6))}
                placeholder="000000"
                required
                maxLength={6}
                className="w-full px-4 py-3 rounded-xl text-center text-2xl font-mono tracking-[0.5em] outline-none transition-all"
                style={{
                  background: 'var(--bg-tertiary)',
                  color: 'var(--text-primary)',
                  border: '1px solid var(--border-color)',
                  letterSpacing: '0.5em',
                }}
                autoFocus
              />
            </div>

            <button
              type="submit"
              disabled={loading || verificationCode.length !== 6}
              className="w-full py-3 rounded-xl text-white font-medium text-sm flex items-center justify-center gap-2 transition-all hover:opacity-90 disabled:opacity-50"
              style={{
                background: 'linear-gradient(135deg, var(--brand) 0%, var(--brand-strong) 100%)',
              }}
            >
              {loading ? (
                <Loader2 size={18} className="animate-spin" />
              ) : (
                <>
                  <ShieldCheck size={18} />
                  Verify Email
                </>
              )}
            </button>
          </form>

          <div className="mt-4 text-center">
            <button
              onClick={handleResendCode}
              disabled={resendCooldown > 0}
              className="text-sm font-medium hover:underline disabled:opacity-50 disabled:no-underline"
              style={{ color: 'var(--brand)' }}
            >
              {resendCooldown > 0
                ? `Resend code in ${resendCooldown}s`
                : "Didn't receive the code? Resend"}
            </button>
          </div>

          <p
            className="mt-4 text-xs text-center"
            style={{ color: 'var(--text-muted)' }}
          >
            The code expires in 10 minutes. Check your spam folder if you don't see it.
          </p>
        </div>
      </div>
    )
  }

  // --- No Team Membership UI ---
  if (mode === 'no_team') {
    return (
      <div
        className="fixed inset-0 z-50 flex items-center justify-center"
        style={{ background: 'var(--bg-primary)' }}
      >
        <div
          className="w-full max-w-md mx-auto p-8 rounded-2xl shadow-2xl"
          style={{
            background: 'var(--bg-secondary)',
            border: '1px solid var(--border-color)',
          }}
        >
          <div className="text-center mb-6">
            <div
              className="inline-flex items-center justify-center w-16 h-16 rounded-2xl mb-4"
              style={{
                background: 'linear-gradient(135deg, #f59e0b 0%, #d97706 100%)',
              }}
            >
              <Clock size={28} className="text-white" />
            </div>
            <h1
              className="text-2xl font-bold"
              style={{ color: 'var(--text-primary)' }}
            >
              Awaiting Team Assignment
            </h1>
            <p
              className="mt-3 text-sm leading-relaxed"
              style={{ color: 'var(--text-muted)' }}
            >
              Your account is verified and active, but you haven't been added to any team yet.
            </p>
            <p
              className="mt-2 text-sm leading-relaxed"
              style={{ color: 'var(--text-muted)' }}
            >
              Please contact a <strong style={{ color: 'var(--text-secondary)' }}>team administrator</strong> to
              request access. Once you're added to a team, you'll be able to use Astonish.
            </p>
          </div>

          <div
            className="p-4 rounded-xl text-sm"
            style={{
              background: 'var(--bg-tertiary)',
              border: '1px solid var(--border-color)',
              color: 'var(--text-secondary)',
            }}
          >
            <p className="font-medium mb-1">What happens next?</p>
            <ul className="list-disc list-inside space-y-1" style={{ color: 'var(--text-muted)' }}>
              <li>A team admin adds you via the admin panel</li>
              <li>You'll receive an email notification</li>
              <li>Then you can sign in and start using Astonish</li>
            </ul>
          </div>

          <button
            onClick={() => {
              setMode('login')
              setError('')
              setSuccess('')
            }}
            className="w-full mt-6 py-3 rounded-xl font-medium text-sm flex items-center justify-center gap-2 transition-all hover:opacity-90"
            style={{
              background: 'var(--bg-tertiary)',
              color: 'var(--text-primary)',
              border: '1px solid var(--border-color)',
            }}
          >
            <LogIn size={18} />
            Back to Sign In
          </button>
        </div>
      </div>
    )
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center"
      style={{ background: 'var(--bg-primary)' }}
    >
      <div
        className="w-full max-w-md mx-auto p-8 rounded-2xl shadow-2xl"
        style={{
          background: 'var(--bg-secondary)',
          border: '1px solid var(--border-color)',
        }}
      >
        {/* Logo / Title */}
        <div className="text-center mb-8">
          <div
            className="inline-flex items-center justify-center w-16 h-16 rounded-2xl mb-4"
            style={{
              background: 'linear-gradient(135deg, var(--brand) 0%, var(--brand-strong) 100%)',
            }}
          >
            <span className="text-3xl text-white font-bold">A</span>
          </div>
          <h1
            className="text-2xl font-bold"
            style={{ color: 'var(--text-primary)' }}
          >
            {isFirstSetup ? 'Set Up Astonish' : 'Welcome to Astonish'}
          </h1>
          <p
            className="mt-2 text-sm"
            style={{ color: 'var(--text-muted)' }}
          >
            {isFirstSetup
              ? 'Create your admin account to get started'
              : mode === 'register'
                ? 'Create your account'
                : 'Sign in to your account'}
          </p>
        </div>

        {/* Error message */}
        {error && (
          <div
            className="flex items-center gap-2 p-3 rounded-lg mb-4 text-sm"
            style={{
              background: 'rgba(239, 68, 68, 0.1)',
              color: '#ef4444',
              border: '1px solid rgba(239, 68, 68, 0.2)',
            }}
          >
            <AlertCircle size={16} />
            <span>{error}</span>
          </div>
        )}

        <form onSubmit={handleSubmit} className="space-y-4">
          {/* Display name (register only) */}
          {mode === 'register' && (
            <div>
              <label
                className="block text-sm font-medium mb-1.5"
                style={{ color: 'var(--text-secondary)' }}
              >
                Display Name
              </label>
              <input
                type="text"
                value={displayName}
                onChange={e => setDisplayName(e.target.value)}
                placeholder="Your name"
                className="w-full px-4 py-2.5 rounded-xl text-sm outline-none transition-all"
                style={{
                  background: 'var(--bg-tertiary)',
                  color: 'var(--text-primary)',
                  border: '1px solid var(--border-color)',
                }}
                autoFocus={mode === 'register'}
              />
            </div>
          )}

          {/* Email */}
          <div>
            <label
              className="block text-sm font-medium mb-1.5"
              style={{ color: 'var(--text-secondary)' }}
            >
              Email
            </label>
            <input
              type="email"
              value={email}
              onChange={e => setEmail(e.target.value)}
              placeholder="you@example.com"
              required
              className="w-full px-4 py-2.5 rounded-xl text-sm outline-none transition-all"
              style={{
                background: 'var(--bg-tertiary)',
                color: 'var(--text-primary)',
                border: '1px solid var(--border-color)',
              }}
              autoFocus={mode === 'login'}
            />
          </div>

          {/* Password */}
          <div>
            <label
              className="block text-sm font-medium mb-1.5"
              style={{ color: 'var(--text-secondary)' }}
            >
              Password
            </label>
            <div className="relative">
              <input
                type={showPassword ? 'text' : 'password'}
                value={password}
                onChange={e => setPassword(e.target.value)}
                placeholder={mode === 'register' ? 'At least 8 characters' : 'Enter your password'}
                required
                minLength={mode === 'register' ? 8 : undefined}
                className="w-full px-4 py-2.5 rounded-xl text-sm outline-none transition-all pr-10"
                style={{
                  background: 'var(--bg-tertiary)',
                  color: 'var(--text-primary)',
                  border: '1px solid var(--border-color)',
                }}
              />
              <button
                type="button"
                onClick={() => setShowPassword(!showPassword)}
                className="absolute right-3 top-1/2 -translate-y-1/2 opacity-50 hover:opacity-100 transition-opacity"
                style={{ color: 'var(--text-muted)' }}
              >
                {showPassword ? <EyeOff size={16} /> : <Eye size={16} />}
              </button>
            </div>
          </div>

          {/* Submit button */}
          <button
            type="submit"
            disabled={loading}
            className="w-full py-3 rounded-xl text-white font-medium text-sm flex items-center justify-center gap-2 transition-all hover:opacity-90 disabled:opacity-50"
            style={{
              background: 'linear-gradient(135deg, var(--brand) 0%, var(--brand-strong) 100%)',
            }}
          >
            {loading ? (
              <Loader2 size={18} className="animate-spin" />
            ) : mode === 'register' ? (
              <>
                <UserPlus size={18} />
                {isFirstSetup ? 'Create Admin Account' : 'Create Account'}
              </>
            ) : (
              <>
                <LogIn size={18} />
                Sign In
              </>
            )}
          </button>
        </form>

        {/* Mode toggle */}
        {!isFirstSetup && canRegister && (
          <div className="mt-6 text-center">
            <span
              className="text-sm"
              style={{ color: 'var(--text-muted)' }}
            >
              {mode === 'login' ? "Don't have an account? " : 'Already have an account? '}
            </span>
            <button
              onClick={() => {
                setMode(mode === 'login' ? 'register' : 'login')
                setError('')
              }}
              className="text-sm font-medium hover:underline"
              style={{ color: 'var(--brand)' }}
            >
              {mode === 'login' ? 'Sign up' : 'Sign in'}
            </button>
          </div>
        )}

        {/* OIDC / SSO login */}
        {ssoProviders.length > 0 && !isFirstSetup && (
          <div className="mt-4">
            <div className="flex items-center gap-3 mb-4">
              <div className="flex-1 h-px" style={{ background: 'var(--border-color)' }} />
              <span className="text-xs" style={{ color: 'var(--text-muted)' }}>or</span>
              <div className="flex-1 h-px" style={{ background: 'var(--border-color)' }} />
            </div>
            {ssoProviders.map(provider => (
              <button
                key={provider.id}
                onClick={async () => {
                  setLoading(true)
                  setError('')
                  try {
                    const resp = await fetch('/api/auth/sso/init', {
                      method: 'POST',
                      headers: { 'Content-Type': 'application/json' },
                      body: JSON.stringify({ provider_id: provider.id, client_type: 'web' }),
                    })
                    if (!resp.ok) {
                      // Try to extract error message from response
                      const text = await resp.text()
                      let msg = 'Failed to initiate SSO login'
                      try {
                        const parsed = JSON.parse(text)
                        msg = parsed.error || parsed.message || msg
                      } catch {
                        if (text) msg = text.trim()
                      }
                      setError(msg)
                      setLoading(false)
                      return
                    }
                    const data = await resp.json()
                    if (data.verify_url) {
                      window.location.href = data.verify_url
                    } else {
                      setError(data.error || 'Failed to initiate SSO')
                      setLoading(false)
                    }
                  } catch {
                    setError('Failed to initiate SSO login')
                    setLoading(false)
                  }
                }}
                disabled={loading}
                className="w-full py-3 rounded-xl font-medium text-sm flex items-center justify-center gap-2 transition-all hover:opacity-90 mb-2"
                style={{
                  background: 'var(--bg-tertiary)',
                  color: 'var(--text-primary)',
                  border: '1px solid var(--border-color)',
                }}
              >
                <ExternalLink size={16} />
                Sign in with {provider.name}
              </button>
            ))}
          </div>
        )}

        {/* First setup hint */}
        {isFirstSetup && (
          <p
            className="mt-4 text-xs text-center"
            style={{ color: 'var(--text-muted)' }}
          >
            This account will be the organization owner with full admin access.
          </p>
        )}
      </div>
    </div>
  )
}
