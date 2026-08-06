import { useState, useEffect } from 'react'
import * as WailsApp from '../wailsjs/go/main/App'
import KeyValueFields, { fieldsToRows, rowsToFields } from './KeyValueFields.jsx'

const inputStyle = {
  background: '#060b11', border: '1px solid #1e3a4f', borderRadius: 5,
  padding: '6px 8px', color: '#e2e8f0', fontFamily: 'var(--font-mono)', fontSize: 11,
}

// Opened by clicking a Vault list row. Loads the entry's decrypted fields
// and notes, lets the user edit everything, and saves through UpdateSecret.
// Kind is fixed — no convert-in-place.
export default function VaultItemModal({ entry, onClose, onSaved }) {
  const [name, setName] = useState(entry.name)
  const [username, setUsername] = useState(entry.username || '')
  const [url, setUrl] = useState(entry.url || '')
  const [notes, setNotes] = useState('')
  const [rows, setRows] = useState([])
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState(null)

  useEffect(() => {
    let cancelled = false
    WailsApp.GetSecretFields(entry.name)
      .then(result => {
        if (cancelled) return
        setNotes(result.notes || '')
        setRows(fieldsToRows(result.fields))
        setLoading(false)
      })
      .catch(e => {
        if (cancelled) return
        setError('Failed to load entry: ' + e)
        setLoading(false)
      })
    return () => { cancelled = true }
  }, [entry.name])

  const handleSave = async (e) => {
    e.preventDefault()
    setError(null)
    const fields = rowsToFields(rows)
    if (Object.keys(fields).length === 0) {
      // Guards against a real gap: UpdateSecret always sends every current
      // field, so zero fields means zero --field flags on the CLI side,
      // which "update" reads as "don't touch fields" (not "clear them") —
      // silently leaving the old ones in place instead of erroring or
      // saving an empty set. Block it here instead.
      setError('At least one field is required.')
      return
    }
    setSaving(true)
    try {
      await WailsApp.UpdateSecret(entry.name, name, username, url, notes, fields)
      onSaved()
    } catch (e) {
      setError('Save failed: ' + e)
      setSaving(false)
    }
  }

  return (
    <div
      className="modal-overlay"
      onClick={(e) => e.target === e.currentTarget && onClose()}
      style={{
        position: 'fixed', inset: 0, zIndex: 10001,
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        background: 'rgba(0,0,0,0.55)',
      }}
    >
      <form
        onSubmit={handleSave}
        style={{
          background: '#0d1520', border: '1px solid #1e3a4f', borderRadius: 10,
          padding: 20, width: 420, maxWidth: '90%', display: 'flex', flexDirection: 'column', gap: 10,
        }}
      >
        <div style={{ color: '#e2e8f0', fontSize: 14, fontWeight: 600 }}>
          Edit {entry.kind === 'login' ? 'Login' : 'Item'}
        </div>

        {error && (
          <div style={{ background: 'rgba(239,68,68,0.08)', border: '1px solid rgba(239,68,68,0.2)', borderRadius: 5, padding: '7px 10px', fontFamily: 'var(--font-mono)', fontSize: 11, color: '#fca5a5' }}>
            {error}
          </div>
        )}

        <input placeholder="Name" value={name} onChange={e => setName(e.target.value)} required style={inputStyle} />

        {entry.kind === 'login' && (
          <div style={{ display: 'flex', gap: 8 }}>
            <input placeholder="Username" value={username} onChange={e => setUsername(e.target.value)} style={{ ...inputStyle, flex: 1 }} />
            <input placeholder="URL" value={url} onChange={e => setUrl(e.target.value)} style={{ ...inputStyle, flex: 1 }} />
          </div>
        )}

        {loading ? (
          <div style={{ color: '#475569', fontFamily: 'var(--font-mono)', fontSize: 11 }}>Loading fields…</div>
        ) : (
          <KeyValueFields rows={rows} onChange={setRows} />
        )}

        <textarea
          placeholder="Notes"
          value={notes}
          onChange={e => setNotes(e.target.value)}
          rows={2}
          style={{ ...inputStyle, resize: 'vertical' }}
        />

        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 4 }}>
          <button
            type="button"
            onClick={onClose}
            style={{ background: 'none', border: '1px solid #1e3a4f', borderRadius: 5, padding: '6px 12px', color: '#94a3b8', fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer' }}
          >
            Cancel
          </button>
          <button
            type="submit"
            disabled={loading || saving}
            style={{ background: 'rgba(0,180,216,0.1)', border: '1px solid rgba(0,180,216,0.3)', borderRadius: 5, padding: '6px 12px', color: '#00b4d8', fontFamily: 'var(--font-mono)', fontSize: 11, cursor: 'pointer' }}
          >
            Save
          </button>
        </div>
      </form>
    </div>
  )
}
