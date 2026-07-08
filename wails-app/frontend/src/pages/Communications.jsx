import { useEffect, useState } from 'react'
import { RefreshCw, Mail } from 'lucide-react'
import { api } from '../services/api.js'
import MessageDetailModal from '../components/MessageDetailModal.jsx'

export default function Communications({ onProfile }) {
  const [messages, setMessages] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  const [sourceFilter, setSourceFilter] = useState('')
  const [openMessage, setOpenMessage] = useState(null)

  const load = async () => {
    setLoading(true)
    try {
      setError(null)
      const data = await api.getAllPersonMessages(200)
      setMessages(data || [])
    } catch (e) {
      setError(e?.message || 'Failed to load messages')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { load() }, [])

  const sources = [...new Set(messages.map(m => m.source).filter(Boolean))]
  const filtered = sourceFilter ? messages.filter(m => m.source === sourceFilter) : messages

  return (
    <>
      <div className="page-header">
        <div className="page-header-left">
          <div className="page-title">Communications</div>
          <div className="page-subtitle">{filtered.length} message{filtered.length !== 1 ? 's' : ''} across all people</div>
        </div>
        <div className="page-header-right">
          <button className="btn btn-ghost btn-sm" onClick={load} style={{ gap: 5 }}>
            <RefreshCw size={12} /> Refresh
          </button>
        </div>
      </div>

      <div className="page-body">
        {sources.length > 1 && (
          <div className="profile-summary-chips" style={{ marginBottom: 12 }}>
            <button
              className={`summary-chip ${!sourceFilter ? 'active' : ''}`}
              onClick={() => setSourceFilter('')}
            >
              All <span>{messages.length}</span>
            </button>
            {sources.map(src => (
              <button
                key={src}
                className={`summary-chip ${sourceFilter === src ? 'active' : ''}`}
                onClick={() => setSourceFilter(sourceFilter === src ? '' : src)}
              >
                {src} <span>{messages.filter(m => m.source === src).length}</span>
              </button>
            ))}
          </div>
        )}

        {loading ? (
          <div className="empty-state" style={{ height: '50vh' }}>
            <div className="spinner" />
          </div>
        ) : error ? (
          <div style={{ padding: '12px 16px', background: 'rgba(239,68,68,.08)', border: '1px solid rgba(239,68,68,.2)', borderRadius: 'var(--radius)', fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--red)' }}>
            {error}
          </div>
        ) : filtered.length === 0 ? (
          <div className="empty-state" style={{ height: '50vh' }}>
            <Mail size={32} style={{ opacity: 0.3, marginBottom: 8 }} />
            <div className="empty-state-title">No messages synced yet</div>
          </div>
        ) : (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
            {filtered.map(msg => (
              <div
                key={msg.id}
                role="button"
                tabIndex={0}
                onClick={() => setOpenMessage(msg)}
                onKeyDown={e => { if (e.key === 'Enter') setOpenMessage(msg) }}
                style={{
                  display: 'flex', flexDirection: 'column', gap: 4, textAlign: 'left',
                  padding: '10px 12px', borderRadius: 6, cursor: 'pointer',
                  background: 'var(--elevated)', border: '1px solid var(--border)',
                  width: '100%', font: 'inherit', color: 'inherit',
                }}
              >
                <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <span style={{
                    padding: '1px 6px', borderRadius: 4,
                    background: 'rgba(0,180,216,0.12)',
                    border: '1px solid rgba(0,180,216,0.3)',
                    color: '#00b4d8', fontSize: 9,
                    fontFamily: 'var(--font-mono)', flexShrink: 0,
                  }}>
                    {msg.source}
                  </span>
                  <button
                    onClick={e => { e.stopPropagation(); onProfile?.(msg.person_id) }}
                    style={{
                      fontFamily: 'var(--font-mono)', fontSize: 11.5,
                      color: 'var(--text)', fontWeight: 600, flexShrink: 0,
                      background: 'none', border: 'none', cursor: 'pointer', padding: 0,
                    }}
                  >
                    {msg.person_full_name || msg.person_platform_username}
                  </button>
                  <span style={{
                    fontFamily: 'var(--font-mono)', fontSize: 11,
                    color: 'var(--text-muted)',
                    overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                  }}>
                    {msg.subject || '(no subject)'}
                  </span>
                  <span style={{
                    marginLeft: 'auto', flexShrink: 0,
                    fontFamily: 'var(--font-mono)', fontSize: 10,
                    color: 'var(--text-dim)',
                  }}>
                    {msg.sent_at ? msg.sent_at.slice(0, 16).replace('T', ' ') : ''}
                  </span>
                </div>
                {msg.body && (
                  <div style={{
                    fontFamily: 'var(--font-mono)', fontSize: 10.5,
                    color: 'var(--text-muted)',
                    overflow: 'hidden', textOverflow: 'ellipsis',
                    display: '-webkit-box', WebkitLineClamp: 2, WebkitBoxOrient: 'vertical',
                  }}>
                    {stripHTML(msg.body)}
                  </div>
                )}
              </div>
            ))}
          </div>
        )}
      </div>

      {openMessage && (
        <MessageDetailModal
          message={openMessage}
          personLabel={openMessage.person_full_name || openMessage.person_platform_username}
          onClose={() => setOpenMessage(null)}
        />
      )}
    </>
  )
}

// Strips HTML tags for the plain-text preview snippet; full HTML is
// rendered properly in MessageDetailModal via a sandboxed iframe.
function stripHTML(body) {
  return body.replace(/<style[\s\S]*?<\/style>/gi, '').replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ').trim()
}
