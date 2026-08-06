import { useState } from 'react'
import { Plus, Trash2, Eye, EyeOff } from 'lucide-react'

let nextRowId = 0

// newRow/fieldsToRows/rowsToFields all share the same id counter, so a row
// minted by Vault.jsx's initial form state and one minted later by this
// component's own "Add field" button never collide.
export function newRow(key = '', value = '') {
  return { id: nextRowId++, key, value }
}

export function fieldsToRows(fields) {
  return Object.entries(fields || {}).map(([key, value]) => newRow(key, value))
}

export function rowsToFields(rows) {
  const fields = {}
  for (const row of rows) {
    const key = row.key.trim()
    if (key) fields[key] = row.value
  }
  return fields
}

// validateRows checks raw rows for the two ways rowsToFields' last-one-wins
// collapse can silently lose data: two rows sharing the same trimmed key
// (only the last survives) and a row with a value but a blank/whitespace-
// only key (dropped entirely). Since Vault updates are a full-replace of an
// entry's fields, either case can permanently delete a credential with no
// warning. Callers must run this over `rows` *before* calling rowsToFields
// and bail out (showing `error`) rather than proceed when it's non-null.
// On success, `fields` is the same map rowsToFields(rows) would produce, so
// callers don't need to call it separately.
export function validateRows(rows) {
  const seen = new Set()
  for (const row of rows) {
    const key = row.key.trim()
    if (!key) {
      if (row.value) return { fields: null, error: 'Every field with a value needs a key.' }
      continue
    }
    if (seen.has(key)) return { fields: null, error: `Duplicate field key "${key}".` }
    seen.add(key)
  }
  return { fields: rowsToFields(rows), error: null }
}

const inputStyle = {
  flex: 1, background: '#060b11', border: '1px solid #1e3a4f', borderRadius: 5,
  padding: '6px 8px', color: '#e2e8f0', fontFamily: 'var(--font-mono)', fontSize: 11,
}

// Dynamic key/value row editor shared by the vault's add-item form and its
// edit modal. `rows` is an array of {id, key, value} — see
// fieldsToRows/rowsToFields for converting to/from the plain map the Wails
// API uses.
export default function KeyValueFields({ rows, onChange }) {
  const [shown, setShown] = useState({})

  const updateRow = (id, patch) => {
    onChange(rows.map(r => (r.id === id ? { ...r, ...patch } : r)))
  }
  const addRow = () => {
    onChange([...rows, newRow()])
  }
  const removeRow = (id) => {
    onChange(rows.filter(r => r.id !== id))
    setShown(prev => {
      const next = { ...prev }
      delete next[id]
      return next
    })
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      {rows.map(row => (
        <div key={row.id} style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
          <input
            placeholder="key"
            value={row.key}
            onChange={e => updateRow(row.id, { key: e.target.value })}
            style={{ ...inputStyle, flex: '0 0 120px' }}
          />
          <input
            type={shown[row.id] ? 'text' : 'password'}
            placeholder="value"
            value={row.value}
            onChange={e => updateRow(row.id, { value: e.target.value })}
            style={inputStyle}
          />
          <button
            type="button"
            onClick={() => setShown(prev => ({ ...prev, [row.id]: !prev[row.id] }))}
            title={shown[row.id] ? 'Hide' : 'Show'}
            style={{ background: 'none', border: 'none', cursor: 'pointer', color: '#4b5563', padding: 4, display: 'flex' }}
          >
            {shown[row.id] ? <EyeOff size={13} /> : <Eye size={13} />}
          </button>
          <button
            type="button"
            onClick={() => removeRow(row.id)}
            title="Remove field"
            style={{ background: 'none', border: 'none', cursor: 'pointer', color: '#4b5563', padding: 4, display: 'flex' }}
          >
            <Trash2 size={13} />
          </button>
        </div>
      ))}
      <button
        type="button"
        onClick={addRow}
        style={{
          alignSelf: 'flex-start', background: 'none', border: '1px dashed #1e3a4f', borderRadius: 5,
          padding: '4px 10px', color: '#64748b', fontFamily: 'var(--font-mono)', fontSize: 10, cursor: 'pointer',
          display: 'flex', alignItems: 'center', gap: 4,
        }}
      >
        <Plus size={11} /> Add field
      </button>
    </div>
  )
}
