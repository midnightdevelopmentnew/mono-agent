import { useEffect, useRef } from 'react'
import { X } from 'lucide-react'

// Renders untrusted HTML email content in a sandboxed iframe — no scripts,
// no same-origin access, no top-level navigation. Falls back to <pre> for
// plain-text bodies.
function isLikelyHTML(body) {
  return /<\/?[a-z][\s\S]*>/i.test(body || '')
}

export default function MessageDetailModal({ message, personLabel, onClose }) {
  const overlayRef = useRef(null)

  useEffect(() => {
    const handler = (e) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [onClose])

  if (!message) return null

  const html = isLikelyHTML(message.body)

  return (
    <div
      ref={overlayRef}
      onClick={(e) => { if (e.target === overlayRef.current) onClose() }}
      style={{
        position: 'fixed', inset: 0, zIndex: 1000,
        background: 'rgba(0,0,0,0.75)',
        display: 'flex', alignItems: 'center', justifyContent: 'center',
      }}
    >
      <div role="dialog" aria-modal="true" aria-label="Message detail" style={{
        background: '#0d1a26', border: '1px solid #1e3a4f', borderRadius: 12,
        width: 720, maxWidth: '92vw', height: '80vh',
        display: 'flex', flexDirection: 'column',
        boxShadow: '0 20px 60px rgba(0,0,0,0.6)',
        overflow: 'hidden',
      }}>
        {/* Header */}
        <div style={{
          display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start',
          padding: '14px 18px', borderBottom: '1px solid #1e3a4f', gap: 12,
        }}>
          <div style={{ minWidth: 0 }}>
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 13, color: 'var(--text)', fontWeight: 600, marginBottom: 4 }}>
              {message.subject || '(no subject)'}
            </div>
            <div style={{ display: 'flex', gap: 10, alignItems: 'center', fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}>
              <span>{personLabel || message.sender}</span>
              <span style={{
                padding: '1px 6px', borderRadius: 4,
                background: 'rgba(0,180,216,0.12)', border: '1px solid rgba(0,180,216,0.3)',
                color: '#00b4d8', fontSize: 9,
              }}>
                {message.source}
              </span>
              {message.sent_at && <span>{message.sent_at.slice(0, 16).replace('T', ' ')}</span>}
            </div>
          </div>
          <button onClick={onClose} style={{ background: 'none', border: 'none', cursor: 'pointer', color: '#475569', padding: 2, flexShrink: 0 }}>
            <X size={18} />
          </button>
        </div>

        {/* Body */}
        <div style={{ flex: 1, overflow: 'hidden', background: '#fff' }}>
          {html ? (
            <iframe
              title="message-body"
              srcDoc={message.body}
              sandbox=""
              style={{ width: '100%', height: '100%', border: 'none' }}
            />
          ) : (
            <pre style={{
              margin: 0, padding: 18, height: '100%', overflow: 'auto',
              whiteSpace: 'pre-wrap', wordBreak: 'break-word',
              fontFamily: 'var(--font-mono)', fontSize: 12, color: '#111',
              background: '#fff',
            }}>
              {message.body || '(no content)'}
            </pre>
          )}
        </div>
      </div>
    </div>
  )
}
