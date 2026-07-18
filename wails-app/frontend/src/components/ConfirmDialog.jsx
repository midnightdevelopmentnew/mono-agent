import { useState, useEffect, useCallback } from 'react'

// Promise-based confirm to replace blocking native window.confirm(), which looks
// foreign in a styled Wails app and freezes the webview. Usage:
//   if (!(await confirm('Delete this?'))) return
const bus = new EventTarget()
let pendingResolve = null

export function confirm(message, opts = {}) {
  return new Promise((resolve) => {
    pendingResolve = resolve
    bus.dispatchEvent(new CustomEvent('confirm:open', { detail: { message, ...opts } }))
  })
}

function settle(value) {
  if (pendingResolve) {
    pendingResolve(value)
    pendingResolve = null
  }
}

// ConfirmHost renders the dialog; mount once near the app root.
export default function ConfirmHost() {
  const [req, setReq] = useState(null)

  const close = useCallback((value) => {
    settle(value)
    setReq(null)
  }, [])

  useEffect(() => {
    const handler = (e) => setReq(e.detail)
    bus.addEventListener('confirm:open', handler)
    return () => bus.removeEventListener('confirm:open', handler)
  }, [])

  useEffect(() => {
    if (!req) return
    const onKey = (e) => {
      if (e.key === 'Escape') close(false)
      if (e.key === 'Enter') close(true)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [req, close])

  if (!req) return null

  const danger = req.danger !== false // default to danger styling
  return (
    <div
      className="modal-overlay"
      onClick={(e) => e.target === e.currentTarget && close(false)}
      style={{
        position: 'fixed', inset: 0, zIndex: 10001,
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        background: 'rgba(0,0,0,0.55)',
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label={req.title || 'Confirm'}
        style={{
          background: 'var(--surface, #0d1520)',
          border: '1px solid var(--border, rgba(255,255,255,0.1))',
          borderRadius: 10, padding: '20px 22px', maxWidth: 400, width: '90%',
          boxShadow: '0 12px 40px rgba(0,0,0,0.6)', fontFamily: 'var(--font-mono)',
        }}
      >
        {req.title && (
          <div style={{ fontSize: 14, fontWeight: 600, color: 'var(--text)', marginBottom: 8 }}>{req.title}</div>
        )}
        <div style={{ fontSize: 12.5, color: 'var(--text-secondary)', lineHeight: 1.5, marginBottom: 18 }}>
          {req.message}
        </div>
        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 10 }}>
          <button
            onClick={() => close(false)}
            style={{
              padding: '7px 14px', borderRadius: 6, cursor: 'pointer', fontSize: 12,
              background: 'transparent', border: '1px solid var(--border, rgba(255,255,255,0.15))',
              color: 'var(--text-secondary)', fontFamily: 'var(--font-mono)',
            }}
          >
            {req.cancelLabel || 'Cancel'}
          </button>
          <button
            autoFocus
            onClick={() => close(true)}
            style={{
              padding: '7px 14px', borderRadius: 6, cursor: 'pointer', fontSize: 12, border: 'none',
              background: danger ? '#ef4444' : '#00b4d8', color: '#fff', fontFamily: 'var(--font-mono)',
            }}
          >
            {req.confirmLabel || 'Confirm'}
          </button>
        </div>
      </div>
    </div>
  )
}
