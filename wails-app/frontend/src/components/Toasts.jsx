import { useState, useEffect, useCallback, useRef } from 'react'
import { AlertTriangle, X } from 'lucide-react'
import { onApiError } from '../services/api.js'

// Listens to the api error bus and shows transient toasts, so a failed backend
// call is visible instead of silently degrading a page to empty data.
export default function Toasts() {
  const [toasts, setToasts] = useState([])
  const seq = useRef(0)

  const dismiss = useCallback((id) => {
    setToasts(prev => prev.filter(t => t.id !== id))
  }, [])

  useEffect(() => {
    return onApiError(({ op, message }) => {
      const id = ++seq.current
      setToasts(prev => {
        // Collapse a rapid burst of the same op into one toast.
        const filtered = prev.filter(t => t.op !== op)
        return [...filtered, { id, op, message }].slice(-4)
      })
      setTimeout(() => dismiss(id), 6000)
    })
  }, [dismiss])

  if (!toasts.length) return null

  return (
    <div style={{
      position: 'fixed', bottom: 16, right: 16, zIndex: 10000,
      display: 'flex', flexDirection: 'column', gap: 8, maxWidth: 380,
    }}>
      {toasts.map(t => (
        <div
          key={t.id}
          role="alert"
          style={{
            display: 'flex', alignItems: 'flex-start', gap: 10,
            padding: '10px 12px',
            background: '#1a0f12', border: '1px solid rgba(239,68,68,0.35)',
            borderRadius: 8, boxShadow: '0 8px 24px rgba(0,0,0,0.5)',
            fontFamily: 'var(--font-mono)', fontSize: 11.5, color: '#fca5a5',
          }}
        >
          <AlertTriangle size={14} style={{ flexShrink: 0, marginTop: 1, color: '#ef4444' }} />
          <div style={{ flex: 1, minWidth: 0 }}>
            <div style={{ color: '#fecaca', fontWeight: 600, marginBottom: 2 }}>Failed: {t.op}</div>
            <div style={{ color: '#f8a5a5', wordBreak: 'break-word', opacity: 0.85 }}>{t.message}</div>
          </div>
          <button
            onClick={() => dismiss(t.id)}
            aria-label="Dismiss"
            style={{ background: 'none', border: 'none', color: '#fca5a5', cursor: 'pointer', flexShrink: 0, padding: 0, lineHeight: 1 }}
          >
            <X size={13} />
          </button>
        </div>
      ))}
    </div>
  )
}
