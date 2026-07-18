import { useState, useEffect, useRef } from 'react'
import { List, X, Trash2 } from 'lucide-react'
import * as WailsApp from '../wailsjs/go/main/App'
import { notify } from '../services/api.js'

// Wails bindings used by the workflow modals, with dev fallbacks.
const ListWorkflows = WailsApp.ListWorkflows ?? (async () => [])
const ListWorkflowTemplates = WailsApp.ListWorkflowTemplates ?? (async () => [])
const CreateWorkflowFromTemplate = WailsApp.CreateWorkflowFromTemplate ?? (async () => { throw new Error('templates unavailable') })
const GetWorkflowExecutions = WailsApp.GetWorkflowExecutions ?? (async () => [])

// Extracted from NodeRunner.jsx to keep that file focused on the canvas editor.
export function SaveModal({ initialName, onConfirm, onClose }) {
  const [name, setName] = useState(initialName || '')
  const inputRef = useRef(null)
  useEffect(() => { inputRef.current?.focus(); inputRef.current?.select() }, [])

  const submit = () => { if (name.trim()) onConfirm(name.trim()) }

  return (
    <div style={{
      position: 'absolute', inset: 0, zIndex: 200,
      background: 'rgba(2,5,9,0.8)',
      display: 'flex', alignItems: 'center', justifyContent: 'center',
    }} onMouseDown={e => { if (e.target === e.currentTarget) onClose() }}>
      <div role="dialog" aria-modal="true" aria-label="Save workflow" style={{
        width: 360,
        background: '#080d16',
        border: '1px solid rgba(0,180,216,0.25)',
        borderRadius: 12,
        padding: '20px 20px 16px',
        boxShadow: '0 24px 60px rgba(0,0,0,.85)',
        display: 'flex', flexDirection: 'column', gap: 14,
      }}>
        <div style={{ fontFamily: 'var(--font-mono)', fontSize: 12, fontWeight: 700, color: '#e2e8f0', letterSpacing: 1 }}>
          SAVE WORKFLOW
        </div>

        <div>
          <div style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)', letterSpacing: 1.5, textTransform: 'uppercase', marginBottom: 6 }}>
            Workflow Name
          </div>
          <input
            ref={inputRef}
            value={name}
            onChange={e => setName(e.target.value)}
            onKeyDown={e => { if (e.key === 'Enter') submit(); if (e.key === 'Escape') onClose() }}
            placeholder="e.g. Instagram Outreach"
            style={{
              width: '100%', boxSizing: 'border-box',
              background: '#020509',
              border: '1px solid rgba(0,180,216,0.2)',
              borderRadius: 6, padding: '8px 10px',
              color: '#e2e8f0', fontFamily: 'var(--font-mono)', fontSize: 12,
              outline: 'none',
            }}
          />
        </div>

        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
          <button
            onMouseDown={onClose}
            style={{ background: 'transparent', border: '1px solid rgba(0,180,216,0.15)', borderRadius: 6, padding: '6px 16px', cursor: 'pointer', fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}
          >Cancel</button>
          <button
            onMouseDown={() => { if (name.trim()) submit() }}
            style={{ background: 'rgba(0,180,216,0.15)', border: '1px solid rgba(0,180,216,0.3)', borderRadius: 6, padding: '6px 20px', cursor: 'pointer', fontFamily: 'var(--font-mono)', fontSize: 11, color: '#00b4d8' }}
          >Save</button>
        </div>
      </div>
    </div>
  )
}

// ── Workflows modal ───────────────────────────────────────────────────────────
export function WorkflowsModal({ currentId, onLoad, onDelete, onClose }) {
  const [tab, setTab]         = useState('saved') // 'saved' | 'templates'
  const [list, setList]       = useState([])
  const [loading, setLoading] = useState(true)
  const [templates, setTemplates] = useState([])
  const [templatesLoading, setTemplatesLoading] = useState(true)
  const [usingTemplate, setUsingTemplate] = useState(null) // template id being instantiated
  const [execsFor, setExecsFor] = useState(null) // workflowId to show executions for
  const [execs, setExecs]     = useState([])

  useEffect(() => {
    ListWorkflows().then(d => { setList(d || []); setLoading(false) }).catch(() => setLoading(false))
    ListWorkflowTemplates().then(d => { setTemplates(d || []); setTemplatesLoading(false) }).catch(() => setTemplatesLoading(false))
  }, [])

  const useTemplate = async (id) => {
    setUsingTemplate(id)
    try {
      const wf = await CreateWorkflowFromTemplate(id)
      onLoad(wf.id)
      onClose()
    } catch (e) {
      setUsingTemplate(null)
      notify('create workflow from template', e.message || e)
    }
  }

  const showExecs = async (id) => {
    setExecsFor(id)
    try { setExecs(await GetWorkflowExecutions(id)) } catch { setExecs([]) }
  }

  return (
    <div style={{
      position: 'absolute', inset: 0, zIndex: 100,
      background: 'rgba(2,5,9,0.85)',
      display: 'flex', alignItems: 'center', justifyContent: 'center',
    }} onMouseDown={e => { if (e.target === e.currentTarget) onClose() }}>
      <div role="dialog" aria-modal="true" aria-label="Workflows" style={{
        width: 520, maxHeight: '70vh',
        background: '#080d16',
        border: '1px solid rgba(0,180,216,0.2)',
        borderRadius: 12,
        display: 'flex', flexDirection: 'column',
        overflow: 'hidden',
        boxShadow: '0 24px 60px rgba(0,0,0,.8)',
      }}>
        {/* Header */}
        <div style={{ padding: '12px 16px', borderBottom: '1px solid rgba(0,180,216,0.1)', display: 'flex', alignItems: 'center', gap: 8, flexShrink: 0 }}>
          {execsFor ? (
            <>
              <button onMouseDown={() => { setExecsFor(null); setExecs([]) }} style={{ background:'transparent',border:'none',cursor:'pointer',color:'var(--text-muted)',display:'flex',alignItems:'center',gap:4,fontFamily:'var(--font-mono)',fontSize:10 }}>
                ← BACK
              </button>
              <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: '#e2e8f0' }}>EXECUTIONS</span>
            </>
          ) : (
            <>
              <List size={13} color="#00b4d8" />
              <div style={{ display: 'flex', gap: 4, flex: 1 }}>
                <button
                  onMouseDown={() => setTab('saved')}
                  style={{ background: tab === 'saved' ? 'rgba(0,180,216,0.15)' : 'transparent', border: 'none', borderRadius: 5, cursor: 'pointer', color: tab === 'saved' ? '#00b4d8' : 'var(--text-muted)', padding: '4px 10px', fontFamily: 'var(--font-mono)', fontSize: 10, fontWeight: 700 }}
                >SAVED WORKFLOWS</button>
                <button
                  onMouseDown={() => setTab('templates')}
                  style={{ background: tab === 'templates' ? 'rgba(0,180,216,0.15)' : 'transparent', border: 'none', borderRadius: 5, cursor: 'pointer', color: tab === 'templates' ? '#00b4d8' : 'var(--text-muted)', padding: '4px 10px', fontFamily: 'var(--font-mono)', fontSize: 10, fontWeight: 700 }}
                >TEMPLATES</button>
              </div>
            </>
          )}
          <button onMouseDown={onClose} aria-label="Close" style={{ background:'transparent',border:'none',cursor:'pointer',color:'var(--text-muted)',display:'flex' }}><X size={14} /></button>
        </div>

        {/* Body */}
        <div style={{ flex: 1, overflowY: 'auto', padding: '8px 0' }}>
          {execsFor ? (
            execs.length === 0 ? (
              <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)', padding: '24px', textAlign: 'center' }}>No executions found</div>
            ) : execs.map(ex => (
              <div key={ex.id} style={{ padding: '8px 16px', display: 'flex', alignItems: 'center', gap: 10, borderBottom: '1px solid rgba(0,180,216,0.05)' }}>
                <div style={{ width: 7, height: 7, borderRadius: '50%', background: ex.status === 'success' ? '#10b981' : ex.status === 'running' ? '#00b4d8' : '#ef4444', flexShrink: 0 }} />
                <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', flex: 1 }}>{ex.id}</span>
                <span style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)' }}>{ex.status}</span>
                {ex.started_at && <span style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)' }}>{new Date(ex.started_at).toLocaleString()}</span>}
              </div>
            ))
          ) : tab === 'templates' ? (
            templatesLoading ? (
              <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)', padding: '24px', textAlign: 'center' }}>Loading…</div>
            ) : templates.length === 0 ? (
              <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)', padding: '24px', textAlign: 'center' }}>No templates available</div>
            ) : templates.map(t => (
              <div key={t.id} style={{
                padding: '10px 16px', display: 'flex', alignItems: 'center', gap: 10,
                borderBottom: '1px solid rgba(0,180,216,0.05)',
              }}>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: '#e2e8f0' }}>{t.name}</div>
                  <div style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)', marginTop: 2, whiteSpace: 'normal' }}>
                    {t.description}
                  </div>
                </div>
                <button
                  onMouseDown={() => useTemplate(t.id)}
                  disabled={usingTemplate === t.id}
                  style={{ background:'rgba(0,180,216,0.08)',border:'1px solid rgba(0,180,216,0.2)',borderRadius:5,cursor:'pointer',color:'#00b4d8',padding:'3px 10px',fontFamily:'var(--font-mono)',fontSize:10,flexShrink:0,opacity: usingTemplate === t.id ? 0.5 : 1 }}
                  onMouseEnter={e => e.currentTarget.style.background='rgba(0,180,216,0.18)'}
                  onMouseLeave={e => e.currentTarget.style.background='rgba(0,180,216,0.08)'}
                >{usingTemplate === t.id ? 'Creating…' : 'Use Template'}</button>
              </div>
            ))
          ) : loading ? (
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)', padding: '24px', textAlign: 'center' }}>Loading…</div>
          ) : list.length === 0 ? (
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)', padding: '24px', textAlign: 'center' }}>No saved workflows yet</div>
          ) : list.map(wf => (
            <div key={wf.id} style={{
              padding: '10px 16px', display: 'flex', alignItems: 'center', gap: 10,
              borderBottom: '1px solid rgba(0,180,216,0.05)',
              background: wf.id === currentId ? 'rgba(0,180,216,0.05)' : 'transparent',
              cursor: 'pointer',
            }}
              onMouseEnter={e => { if (wf.id !== currentId) e.currentTarget.style.background = 'rgba(255,255,255,0.03)' }}
              onMouseLeave={e => { if (wf.id !== currentId) e.currentTarget.style.background = 'transparent' }}
            >
              <div style={{ width: 7, height: 7, borderRadius: '50%', background: wf.is_active ? '#10b981' : '#334155', flexShrink: 0 }} title={wf.is_active ? 'Active' : 'Inactive'} />
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: '#e2e8f0', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{wf.name || 'Untitled'}</div>
                <div style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)', marginTop: 2 }}>
                  {(wf.nodes || []).length} nodes · v{wf.version || 1} · {wf.updated_at ? new Date(wf.updated_at).toLocaleString() : ''}
                </div>
              </div>
              <button
                onMouseDown={e => { e.stopPropagation(); showExecs(wf.id) }}
                title="View executions"
                style={{ background:'transparent',border:'none',cursor:'pointer',color:'var(--text-muted)',padding:4,display:'flex',alignItems:'center' }}
                onMouseEnter={e => e.currentTarget.style.color='#00b4d8'}
                onMouseLeave={e => e.currentTarget.style.color='var(--text-muted)'}
              ><List size={11} /></button>
              <button
                onMouseDown={e => { e.stopPropagation(); onLoad(wf.id) }}
                style={{ background:'rgba(0,180,216,0.08)',border:'1px solid rgba(0,180,216,0.2)',borderRadius:5,cursor:'pointer',color:'#00b4d8',padding:'3px 10px',fontFamily:'var(--font-mono)',fontSize:10 }}
                onMouseEnter={e => e.currentTarget.style.background='rgba(0,180,216,0.18)'}
                onMouseLeave={e => e.currentTarget.style.background='rgba(0,180,216,0.08)'}
              >Open</button>
              <button
                onMouseDown={e => { e.stopPropagation(); onDelete(wf.id).then(() => setList(l => l.filter(w => w.id !== wf.id))) }}
                title="Delete"
                style={{ background:'transparent',border:'none',cursor:'pointer',color:'rgba(239,68,68,0.4)',padding:4,display:'flex',alignItems:'center' }}
                onMouseEnter={e => e.currentTarget.style.color='#ef4444'}
                onMouseLeave={e => e.currentTarget.style.color='rgba(239,68,68,0.4)'}
              ><Trash2 size={11} /></button>
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}
