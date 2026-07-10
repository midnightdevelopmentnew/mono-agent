import { useState, useEffect, useCallback } from 'react'
import { CheckCircle, XCircle, Clock, RefreshCw, ChevronDown, ChevronRight, AlertTriangle, Mail } from 'lucide-react'
import * as WailsApp from '../wailsjs/go/main/App'

const GetHILItems = WailsApp.GetHILItems ?? (async () => [])
const ApproveHIL  = WailsApp.ApproveHIL  ?? (async () => {})
const RejectHIL   = WailsApp.RejectHIL   ?? (async () => {})
const GetDraftPersonMessages   = WailsApp.GetDraftPersonMessages   ?? (async () => [])
const SendDraftPersonMessage   = WailsApp.SendDraftPersonMessage   ?? (async () => {})
const RejectDraftPersonMessage = WailsApp.RejectDraftPersonMessage ?? (async () => {})

function formatDate(s) {
  if (!s) return ''
  const d = new Date(s.replace(' ', 'T'))
  return isNaN(d) ? s : d.toLocaleString()
}

function isImageValue(value) {
  if (typeof value !== 'string') return false
  return /\.(png|jpe?g|gif|webp|svg|bmp)(\?.*)?$/i.test(value) ||
    value.startsWith('data:image/') ||
    value.startsWith('blob:')
}

function ReadonlyField({ label, value }) {
  const display = typeof value === 'object' ? JSON.stringify(value, null, 2) : String(value ?? '')
  const isImg = isImageValue(display)
  return (
    <div style={{ marginBottom: 10 }}>
      <div style={{ fontSize: 10, fontFamily: 'var(--font-mono)', color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1, marginBottom: 3 }}>
        {label}
      </div>
      {isImg ? (
        <div style={{ borderRadius: 6, overflow: 'hidden', border: '1px solid rgba(255,255,255,0.07)', background: 'rgba(255,255,255,0.04)' }}>
          <img src={display} alt={label} style={{ maxWidth: '100%', maxHeight: 200, display: 'block', objectFit: 'contain' }} />
          <div style={{ padding: '4px 8px', fontSize: 11, color: 'var(--text-muted)', fontFamily: 'var(--font-mono)', wordBreak: 'break-all' }}>{display}</div>
        </div>
      ) : (
        <div style={{
          padding: '7px 10px',
          background: 'rgba(255,255,255,0.04)',
          borderRadius: 6,
          border: '1px solid rgba(255,255,255,0.07)',
          fontSize: 13,
          color: 'var(--text-secondary)',
          wordBreak: 'break-all',
        }}>
          {display}
        </div>
      )}
    </div>
  )
}

function EditableField({ label, value, onChange }) {
  const isLong = typeof value === 'string' && value.length > 80
  return (
    <div style={{ marginBottom: 12 }}>
      <div style={{ fontSize: 10, fontFamily: 'var(--font-mono)', color: '#00b4d8', textTransform: 'uppercase', letterSpacing: 1, marginBottom: 4 }}>
        {label} <span style={{ color: 'var(--text-muted)', fontWeight: 400 }}>(editable)</span>
      </div>
      <textarea
        value={typeof value === 'object' ? JSON.stringify(value, null, 2) : String(value ?? '')}
        onChange={e => onChange(e.target.value)}
        rows={isLong ? 6 : 3}
        style={{
          width: '100%',
          boxSizing: 'border-box',
          padding: '9px 12px',
          background: 'rgba(0,180,216,0.04)',
          border: '1px solid rgba(0,180,216,0.25)',
          borderRadius: 6,
          fontSize: 13,
          color: 'var(--text-primary)',
          fontFamily: 'inherit',
          resize: 'vertical',
          outline: 'none',
          lineHeight: 1.6,
        }}
        onFocus={e => { e.target.style.borderColor = 'rgba(0,180,216,0.6)' }}
        onBlur={e => { e.target.style.borderColor = 'rgba(0,180,216,0.25)' }}
      />
    </div>
  )
}

function HILCard({ item, onApprove, onReject }) {
  const [expanded, setExpanded] = useState(true)
  const [editedValues, setEditedValues] = useState({ ...item.editable_data })
  const [loading, setLoading] = useState(null) // 'approve' | 'reject'

  const editableKeys = item.node_config?.editable_fields ?? Object.keys(item.editable_data ?? {})
  const readonlyKeys = item.node_config?.readonly_fields ?? Object.keys(item.readonly_data ?? {})

  const handleApprove = async () => {
    setLoading('approve')
    try {
      // Re-parse each edited string back to its original JSON type (number, bool, object)
      // so downstream nodes don't receive stringified values where they expect typed data.
      const typed = Object.fromEntries(
        Object.entries(editedValues).map(([k, v]) => {
          if (typeof v !== 'string') return [k, v]
          try { return [k, JSON.parse(v)] } catch { return [k, v] }
        })
      )
      await onApprove(item.id, JSON.stringify(typed))
    } finally {
      setLoading(null)
    }
  }

  const handleReject = async () => {
    setLoading('reject')
    try {
      await onReject(item.id)
    } finally {
      setLoading(null)
    }
  }

  return (
    <div style={{
      background: 'linear-gradient(160deg,#0d1a28 0%,#091220 100%)',
      border: '1.5px solid rgba(0,180,216,0.18)',
      borderRadius: 10,
      marginBottom: 16,
      overflow: 'hidden',
    }}>
      {/* Header */}
      <div
        style={{
          display: 'flex', alignItems: 'center', gap: 10,
          padding: '12px 16px',
          background: 'rgba(0,180,216,0.07)',
          borderBottom: expanded ? '1px solid rgba(0,180,216,0.12)' : 'none',
          cursor: 'pointer',
        }}
        onClick={() => setExpanded(v => !v)}
      >
        <Clock size={14} color="#00b4d8" style={{ flexShrink: 0 }} />
        <div style={{ flex: 1 }}>
          <div style={{ fontSize: 13, fontWeight: 600, color: '#e2e8f0' }}>
            {item.node_name}
            {item.workflow_name && (
              <span style={{ fontWeight: 400, color: 'var(--text-muted)', marginLeft: 6 }}>
                — {item.workflow_name}
              </span>
            )}
          </div>
          <div style={{ fontSize: 11, color: 'var(--text-muted)', fontFamily: 'var(--font-mono)', marginTop: 2 }}>
            {formatDate(item.created_at)} · exec: {item.execution_id.slice(0, 8)}…
          </div>
        </div>
        <span style={{
          padding: '2px 8px', borderRadius: 10,
          background: 'rgba(0,180,216,0.12)', color: '#00b4d8',
          fontSize: 11, fontFamily: 'var(--font-mono)',
        }}>pending</span>
        {expanded ? <ChevronDown size={14} color="var(--text-muted)" /> : <ChevronRight size={14} color="var(--text-muted)" />}
      </div>

      {expanded && (
        <div style={{ padding: 16 }}>
          {/* Read-only section */}
          {readonlyKeys.length > 0 && (
            <div style={{ marginBottom: 20 }}>
              <div style={{ fontSize: 11, color: 'var(--text-muted)', fontFamily: 'var(--font-mono)', textTransform: 'uppercase', letterSpacing: 1, marginBottom: 10, paddingBottom: 6, borderBottom: '1px solid rgba(255,255,255,0.06)' }}>
                Context Info
              </div>
              {readonlyKeys.map(k => (
                <ReadonlyField key={k} label={k.replace(/_/g, ' ')} value={item.readonly_data?.[k]} />
              ))}
            </div>
          )}

          {/* Editable section */}
          {editableKeys.length > 0 && (
            <div style={{ marginBottom: 20 }}>
              <div style={{ fontSize: 11, color: '#00b4d8', fontFamily: 'var(--font-mono)', textTransform: 'uppercase', letterSpacing: 1, marginBottom: 10, paddingBottom: 6, borderBottom: '1px solid rgba(0,180,216,0.1)' }}>
                Review &amp; Edit
              </div>
              {editableKeys.map(k => (
                <EditableField
                  key={k}
                  label={k.replace(/_/g, ' ')}
                  value={editedValues[k] ?? ''}
                  onChange={val => setEditedValues(prev => ({ ...prev, [k]: val }))}
                />
              ))}
            </div>
          )}

          {/* Actions */}
          <div style={{ display: 'flex', gap: 10, justifyContent: 'flex-end', paddingTop: 8, borderTop: '1px solid rgba(255,255,255,0.06)' }}>
            <button
              onClick={handleReject}
              disabled={!!loading}
              style={{
                display: 'flex', alignItems: 'center', gap: 6,
                padding: '8px 16px',
                background: loading === 'reject' ? 'rgba(239,68,68,0.15)' : 'rgba(239,68,68,0.1)',
                border: '1px solid rgba(239,68,68,0.3)',
                borderRadius: 6, cursor: loading ? 'wait' : 'pointer',
                color: '#ef4444', fontSize: 13, fontWeight: 500,
                opacity: loading && loading !== 'reject' ? 0.5 : 1,
              }}
            >
              <XCircle size={14} />
              {loading === 'reject' ? 'Rejecting…' : 'Reject'}
            </button>
            <button
              onClick={handleApprove}
              disabled={!!loading}
              style={{
                display: 'flex', alignItems: 'center', gap: 6,
                padding: '8px 18px',
                background: loading === 'approve' ? 'rgba(16,185,129,0.25)' : 'rgba(16,185,129,0.15)',
                border: '1px solid rgba(16,185,129,0.4)',
                borderRadius: 6, cursor: loading ? 'wait' : 'pointer',
                color: '#10b981', fontSize: 13, fontWeight: 600,
                opacity: loading && loading !== 'approve' ? 0.5 : 1,
              }}
            >
              <CheckCircle size={14} />
              {loading === 'approve' ? 'Approving…' : 'Approve & Continue'}
            </button>
          </div>
        </div>
      )}
    </div>
  )
}

function DraftMessageCard({ item, onSend, onReject }) {
  const [expanded, setExpanded] = useState(true)
  const [loading, setLoading] = useState(null) // 'send' | 'reject'

  const handleSend = async () => {
    setLoading('send')
    try { await onSend(item.id) } finally { setLoading(null) }
  }
  const handleReject = async () => {
    setLoading('reject')
    try { await onReject(item.id) } finally { setLoading(null) }
  }

  return (
    <div style={{
      background: 'linear-gradient(160deg,#1c1608 0%,#120d05 100%)',
      border: '1.5px solid rgba(245,158,11,0.25)',
      borderRadius: 10,
      marginBottom: 16,
      overflow: 'hidden',
    }}>
      <div
        style={{
          display: 'flex', alignItems: 'center', gap: 10,
          padding: '12px 16px',
          background: 'rgba(245,158,11,0.08)',
          borderBottom: expanded ? '1px solid rgba(245,158,11,0.15)' : 'none',
          cursor: 'pointer',
        }}
        onClick={() => setExpanded(v => !v)}
      >
        <Mail size={14} color="#f59e0b" style={{ flexShrink: 0 }} />
        <div style={{ flex: 1 }}>
          <div style={{ fontSize: 13, fontWeight: 600, color: '#e2e8f0' }}>
            {item.subject || '(no subject)'}
            <span style={{ fontWeight: 400, color: 'var(--text-muted)', marginLeft: 6 }}>
              — to {item.person_full_name || item.person_platform_username}
            </span>
          </div>
          <div style={{ fontSize: 11, color: 'var(--text-muted)', fontFamily: 'var(--font-mono)', marginTop: 2 }}>
            {formatDate(item.created_at)} · {item.source}
          </div>
        </div>
        <span style={{
          padding: '2px 8px', borderRadius: 10,
          background: 'rgba(245,158,11,0.15)', color: '#f59e0b',
          fontSize: 11, fontFamily: 'var(--font-mono)',
        }}>draft</span>
        {expanded ? <ChevronDown size={14} color="var(--text-muted)" /> : <ChevronRight size={14} color="var(--text-muted)" />}
      </div>

      {expanded && (
        <div style={{ padding: 16 }}>
          <div style={{
            padding: '9px 12px', background: 'rgba(255,255,255,0.04)', borderRadius: 6,
            border: '1px solid rgba(255,255,255,0.07)', fontSize: 13, color: 'var(--text-secondary)',
            whiteSpace: 'pre-wrap', marginBottom: 16,
          }}>
            {item.body}
          </div>

          <div style={{ display: 'flex', gap: 10, justifyContent: 'flex-end', paddingTop: 8, borderTop: '1px solid rgba(255,255,255,0.06)' }}>
            <button
              onClick={handleReject}
              disabled={!!loading}
              style={{
                display: 'flex', alignItems: 'center', gap: 6,
                padding: '8px 16px',
                background: loading === 'reject' ? 'rgba(239,68,68,0.15)' : 'rgba(239,68,68,0.1)',
                border: '1px solid rgba(239,68,68,0.3)',
                borderRadius: 6, cursor: loading ? 'wait' : 'pointer',
                color: '#ef4444', fontSize: 13, fontWeight: 500,
                opacity: loading && loading !== 'reject' ? 0.5 : 1,
              }}
            >
              <XCircle size={14} />
              {loading === 'reject' ? 'Discarding…' : 'Discard'}
            </button>
            <button
              onClick={handleSend}
              disabled={!!loading}
              style={{
                display: 'flex', alignItems: 'center', gap: 6,
                padding: '8px 18px',
                background: loading === 'send' ? 'rgba(16,185,129,0.25)' : 'rgba(16,185,129,0.15)',
                border: '1px solid rgba(16,185,129,0.4)',
                borderRadius: 6, cursor: loading ? 'wait' : 'pointer',
                color: '#10b981', fontSize: 13, fontWeight: 600,
                opacity: loading && loading !== 'send' ? 0.5 : 1,
              }}
            >
              <CheckCircle size={14} />
              {loading === 'send' ? 'Sending…' : 'Send'}
            </button>
          </div>
        </div>
      )}
    </div>
  )
}

export default function HumanInLoop() {
  const [items, setItems] = useState([])
  const [drafts, setDrafts] = useState([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const [data, draftData] = await Promise.all([GetHILItems(), GetDraftPersonMessages()])
      setItems(data ?? [])
      setDrafts(draftData ?? [])
    } catch (e) {
      setError(e?.message ?? 'Failed to load')
    } finally {
      setLoading(false)
    }
  }, [])

  // Auto-refresh every 3 seconds while the page is open.
  useEffect(() => {
    load()
    const interval = setInterval(load, 3000)
    return () => clearInterval(interval)
  }, [load])

  const handleApprove = async (id, editedJSON) => {
    try {
      await ApproveHIL(id, editedJSON)
      setItems(prev => prev.filter(i => i.id !== id))
    } catch (e) {
      setError(e?.message ?? 'Failed to approve — the item may have already been resolved.')
    }
  }

  const handleReject = async (id) => {
    try {
      await RejectHIL(id)
      setItems(prev => prev.filter(i => i.id !== id))
    } catch (e) {
      setError(e?.message ?? 'Failed to reject — the item may have already been resolved.')
    }
  }

  const handleSendDraft = async (id) => {
    try {
      await SendDraftPersonMessage(id)
      setDrafts(prev => prev.filter(d => d.id !== id))
    } catch (e) {
      setError(e?.message ?? 'Failed to send — the draft may have already been resolved.')
    }
  }

  const handleRejectDraft = async (id) => {
    try {
      await RejectDraftPersonMessage(id)
      setDrafts(prev => prev.filter(d => d.id !== id))
    } catch (e) {
      setError(e?.message ?? 'Failed to discard — the draft may have already been resolved.')
    }
  }

  return (
    <div style={{ padding: '24px 28px', maxWidth: 760 }}>
      {/* Header */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 24 }}>
        <div>
          <h1 style={{ fontSize: 20, fontWeight: 700, color: '#e2e8f0', margin: 0 }}>Human in Loop</h1>
          <p style={{ fontSize: 13, color: 'var(--text-muted)', margin: '4px 0 0' }}>
            Workflows paused here are waiting for your review. Edit content if needed, then approve or reject.
          </p>
        </div>
        <button
          onClick={load}
          disabled={loading}
          style={{
            marginLeft: 'auto',
            display: 'flex', alignItems: 'center', gap: 6,
            padding: '7px 14px',
            background: 'rgba(255,255,255,0.06)',
            border: '1px solid rgba(255,255,255,0.1)',
            borderRadius: 6, cursor: loading ? 'wait' : 'pointer',
            color: 'var(--text-secondary)', fontSize: 13,
          }}
        >
          <RefreshCw size={13} style={{ animation: loading ? 'spin 1s linear infinite' : 'none' }} />
          Refresh
        </button>
      </div>

      {error && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '10px 14px', background: 'rgba(239,68,68,0.08)', border: '1px solid rgba(239,68,68,0.2)', borderRadius: 8, marginBottom: 16, color: '#ef4444', fontSize: 13 }}>
          <AlertTriangle size={14} /> {error}
        </div>
      )}

      {!loading && items.length === 0 && drafts.length === 0 && !error && (
        <div style={{
          textAlign: 'center', padding: '60px 20px',
          border: '1px dashed rgba(255,255,255,0.1)', borderRadius: 12,
          color: 'var(--text-muted)',
        }}>
          <CheckCircle size={32} style={{ marginBottom: 12, opacity: 0.3 }} />
          <div style={{ fontSize: 15, fontWeight: 500, marginBottom: 6 }}>No pending reviews</div>
          <div style={{ fontSize: 13 }}>Workflows with a Human in Loop node, and drafted messages awaiting send, will appear here.</div>
        </div>
      )}

      {drafts.map(item => (
        <DraftMessageCard
          key={item.id}
          item={item}
          onSend={handleSendDraft}
          onReject={handleRejectDraft}
        />
      ))}

      {items.map(item => (
        <HILCard
          key={item.id}
          item={item}
          onApprove={handleApprove}
          onReject={handleReject}
        />
      ))}

      <style>{`@keyframes spin { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }`}</style>
    </div>
  )
}
