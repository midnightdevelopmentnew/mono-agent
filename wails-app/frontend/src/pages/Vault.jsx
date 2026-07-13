import { useState, useEffect, useCallback } from 'react'
import { Plus, Trash2, Eye, EyeOff, KeyRound } from 'lucide-react'
import * as WailsApp from '../wailsjs/go/main/App'

const fmtDate = (s) => {
  if (!s) return '—'
  const d = new Date(s.includes('T') ? s : s.replace(' ', 'T') + 'Z')
  if (isNaN(d)) return s
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}

const KIND_COLORS = {
  login: { bg: 'rgba(0,180,216,0.1)', border: 'rgba(0,180,216,0.25)', color: '#00b4d8' },
  secret: { bg: 'rgba(124,58,237,0.15)', border: 'rgba(124,58,237,0.3)', color: '#a78bfa' },
}
const kindBadge = (kind) => {
  const s = KIND_COLORS[kind] || { bg: '#1a2332', border: '#334', color: '#64748b' }
  return (
    <span style={{
      background: s.bg, border: `1px solid ${s.border}`, borderRadius: 3,
      padding: '1px 6px', fontFamily: 'var(--font-mono)', fontSize: 9, color: s.color,
    }}>{kind}</span>
  )
}

const EMPTY_FORM = { kind: 'secret', name: '', value: '', username: '', url: '', notes: '' }

export default function Vault() {
  const [entries, setEntries] = useState([])
  const [revealed, setRevealed] = useState({})
  const [showAdd, setShowAdd] = useState(false)
  const [form, setForm] = useState(EMPTY_FORM)
  const [error, setError] = useState(null)

  const load = useCallback(async () => {
    try {
      const list = await WailsApp.ListSecrets()
      setEntries(list || [])
    } catch (e) {
      setError('Failed to load vault: ' + e)
    }
  }, [])

  useEffect(() => { load() }, [load])

  const handleAdd = async (e) => {
    e.preventDefault()
    setError(null)
    try {
      await WailsApp.AddSecret(form.kind, form.name, form.value, form.username, form.url, form.notes)
      setForm(EMPTY_FORM)
      setShowAdd(false)
      load()
    } catch (e) {
      setError('Save failed: ' + e)
    }
  }

  const handleReveal = async (id) => {
    if (revealed[id]) {
      setRevealed(prev => { const next = { ...prev }; delete next[id]; return next })
      return
    }
    try {
      const value = await WailsApp.RevealSecret(id)
      setRevealed(prev => ({ ...prev, [id]: value }))
    } catch (e) {
      setError('Reveal failed: ' + e)
    }
  }

  const handleDelete = async (id) => {
    if (!window.confirm('Delete this vault entry? This cannot be undone.')) return
    setError(null)
    try {
      await WailsApp.DeleteSecret(id)
      setEntries(prev => prev.filter(e => e.id !== id))
      setRevealed(prev => { const next = { ...prev }; delete next[id]; return next })
    } catch (e) {
      setError('Delete failed: ' + e)
    }
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', overflow: 'hidden' }}>
      {/* Header */}
      <div style={{
        padding: '14px 20px 10px', borderBottom: '1px solid #0d1a26',
        display: 'flex', alignItems: 'center', gap: 12,
      }}>
        <div>
          <div style={{ color: '#e2e8f0', fontSize: 16, fontWeight: 600 }}>Vault</div>
          <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: '#475569' }}>
            {entries.length} {entries.length === 1 ? 'entry' : 'entries'}
          </div>
        </div>
        <div style={{ flex: 1 }} />
        <button
          onClick={() => setShowAdd(true)}
          style={{
            background: 'rgba(0,180,216,0.1)', border: '1px solid rgba(0,180,216,0.3)',
            borderRadius: 6, padding: '6px 12px', color: '#00b4d8',
            fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer',
            display: 'flex', alignItems: 'center', gap: 5,
          }}
        >
          <Plus size={12} /> Add Secret
        </button>
      </div>

      {/* Error */}
      {error && (
        <div style={{ margin: '8px 20px', background: 'rgba(239,68,68,0.08)', border: '1px solid rgba(239,68,68,0.2)', borderRadius: 5, padding: '7px 10px', fontFamily: 'var(--font-mono)', fontSize: 11, color: '#fca5a5' }}>
          {error}
        </div>
      )}

      {/* Add form */}
      {showAdd && (
        <form
          onSubmit={handleAdd}
          style={{
            margin: '10px 20px', padding: 14, background: '#0d1a26',
            border: '1px solid #1e3a4f', borderRadius: 6,
            display: 'flex', flexDirection: 'column', gap: 8,
          }}
        >
          <div style={{ display: 'flex', gap: 8 }}>
            <select
              value={form.kind}
              onChange={e => setForm({ ...form, kind: e.target.value })}
              style={{
                background: '#060b11', border: '1px solid #1e3a4f', borderRadius: 5,
                padding: '6px 8px', color: '#e2e8f0', fontFamily: 'var(--font-mono)', fontSize: 11,
              }}
            >
              <option value="secret">Secret</option>
              <option value="login">Login</option>
            </select>
            <input
              placeholder="Name"
              value={form.name}
              onChange={e => setForm({ ...form, name: e.target.value })}
              required
              style={{
                flex: 1, background: '#060b11', border: '1px solid #1e3a4f', borderRadius: 5,
                padding: '6px 8px', color: '#e2e8f0', fontFamily: 'var(--font-mono)', fontSize: 11,
              }}
            />
          </div>
          {form.kind === 'login' && (
            <div style={{ display: 'flex', gap: 8 }}>
              <input
                placeholder="Username"
                value={form.username}
                onChange={e => setForm({ ...form, username: e.target.value })}
                style={{
                  flex: 1, background: '#060b11', border: '1px solid #1e3a4f', borderRadius: 5,
                  padding: '6px 8px', color: '#e2e8f0', fontFamily: 'var(--font-mono)', fontSize: 11,
                }}
              />
              <input
                placeholder="URL"
                value={form.url}
                onChange={e => setForm({ ...form, url: e.target.value })}
                style={{
                  flex: 1, background: '#060b11', border: '1px solid #1e3a4f', borderRadius: 5,
                  padding: '6px 8px', color: '#e2e8f0', fontFamily: 'var(--font-mono)', fontSize: 11,
                }}
              />
            </div>
          )}
          <input
            type="password"
            placeholder="Value"
            value={form.value}
            onChange={e => setForm({ ...form, value: e.target.value })}
            required
            style={{
              background: '#060b11', border: '1px solid #1e3a4f', borderRadius: 5,
              padding: '6px 8px', color: '#e2e8f0', fontFamily: 'var(--font-mono)', fontSize: 11,
            }}
          />
          <textarea
            placeholder="Notes"
            value={form.notes}
            onChange={e => setForm({ ...form, notes: e.target.value })}
            rows={2}
            style={{
              background: '#060b11', border: '1px solid #1e3a4f', borderRadius: 5,
              padding: '6px 8px', color: '#e2e8f0', fontFamily: 'var(--font-mono)', fontSize: 11, resize: 'vertical',
            }}
          />
          <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
            <button
              type="button"
              onClick={() => { setShowAdd(false); setForm(EMPTY_FORM) }}
              style={{
                background: 'none', border: '1px solid #1e3a4f', borderRadius: 5,
                padding: '6px 12px', color: '#94a3b8', fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer',
              }}
            >
              Cancel
            </button>
            <button
              type="submit"
              style={{
                background: 'rgba(0,180,216,0.1)', border: '1px solid rgba(0,180,216,0.3)',
                borderRadius: 5, padding: '6px 12px', color: '#00b4d8',
                fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer',
              }}
            >
              Save
            </button>
          </div>
        </form>
      )}

      {/* Column headers */}
      <div style={{
        display: 'flex', alignItems: 'center', gap: 0,
        padding: '5px 20px', borderBottom: '1px solid #0a1520',
        fontFamily: 'var(--font-mono)', fontSize: 9, color: '#334155',
        letterSpacing: '1px', textTransform: 'uppercase',
      }}>
        <div style={{ flex: 1 }}>Name</div>
        <div style={{ width: 70 }}>Kind</div>
        <div style={{ width: 110 }}>Username</div>
        <div style={{ width: 200 }}>Value</div>
        <div style={{ width: 56 }}>Updated</div>
        <div style={{ width: 28 }} />
      </div>

      {/* Rows */}
      <div style={{ flex: 1, overflowY: 'auto' }}>
        {entries.length === 0 && (
          <div style={{
            display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center',
            height: 200, gap: 12, color: '#334155',
          }}>
            <KeyRound size={32} style={{ opacity: 0.3 }} />
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 12 }}>
              No vault entries yet — add a secret or login above
            </div>
          </div>
        )}
        {entries.map(entry => (
          <div
            key={entry.id}
            style={{
              display: 'flex', alignItems: 'center', gap: 0,
              padding: '6px 20px', borderBottom: '1px solid #0a1520',
            }}
          >
            <div style={{ flex: 1, minWidth: 0, fontFamily: 'var(--font-mono)', fontSize: 11, color: '#94a3b8', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', paddingRight: 10 }}>
              {entry.name}
            </div>
            <div style={{ width: 70 }}>{kindBadge(entry.kind)}</div>
            <div style={{ width: 110, fontFamily: 'var(--font-mono)', fontSize: 10, color: '#475569', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', paddingRight: 8 }}>
              {entry.username || '—'}
            </div>
            <div style={{ width: 200, display: 'flex', alignItems: 'center', gap: 6, paddingRight: 8 }}>
              <span style={{
                fontFamily: 'var(--font-mono)', fontSize: 11, color: '#e2e8f0',
                overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', flex: 1,
              }}>
                {revealed[entry.id] ?? '••••••••'}
              </span>
              <button
                onClick={() => handleReveal(entry.id)}
                title={revealed[entry.id] ? 'Hide' : 'Reveal'}
                style={{
                  background: 'none', border: 'none', cursor: 'pointer',
                  color: '#4b5563', padding: 4, borderRadius: 3, flexShrink: 0,
                  display: 'flex', alignItems: 'center', justifyContent: 'center',
                }}
                onMouseEnter={e => e.currentTarget.style.color = '#00b4d8'}
                onMouseLeave={e => e.currentTarget.style.color = '#4b5563'}
              >
                {revealed[entry.id] ? <EyeOff size={13} /> : <Eye size={13} />}
              </button>
            </div>
            <div style={{ width: 56, fontFamily: 'var(--font-mono)', fontSize: 10, color: '#475569' }}>{fmtDate(entry.updated_at)}</div>
            <div style={{ width: 28 }}>
              <button
                onClick={() => handleDelete(entry.id)}
                style={{
                  background: 'none', border: 'none', cursor: 'pointer',
                  color: '#4b5563', padding: 4, borderRadius: 3,
                  display: 'flex', alignItems: 'center', justifyContent: 'center',
                }}
                onMouseEnter={e => e.currentTarget.style.color = '#ef4444'}
                onMouseLeave={e => e.currentTarget.style.color = '#4b5563'}
              >
                <Trash2 size={13} />
              </button>
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}
