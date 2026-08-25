import { useState, useEffect, useCallback } from 'react'
import {
  X, RefreshCw, Building2, CheckCircle2, XCircle, Circle,
  MessageCircleQuestion, ShieldAlert, Coins, GitBranch, ScrollText, ListTree,
} from 'lucide-react'
import { api, onOrgEvent, onOrgEventsClosed, notify } from '../services/api.js'

const TABS = [
  { id: 'overview',  label: 'Overview',  icon: Circle },
  { id: 'questions', label: 'Questions', icon: MessageCircleQuestion },
  { id: 'gates',     label: 'Gates',     icon: ShieldAlert },
  { id: 'logs',      label: 'Logs',      icon: ScrollText },
  { id: 'costs',     label: 'Costs',     icon: Coins },
  { id: 'flow',      label: 'Flow',      icon: GitBranch },
  { id: 'decisions', label: 'Decisions', icon: ListTree },
]

function statusColor(status) {
  if (status === 'running') return 'var(--green-neon)'
  if (status === 'crashed' || status === 'error') return 'var(--red)'
  if (status === 'paused') return '#eab308'
  return 'var(--text-muted)'
}

function StatusDot({ status }) {
  const c = statusColor(status)
  return <span style={{ width: 6, height: 6, borderRadius: '50%', background: c, boxShadow: c !== 'var(--text-muted)' ? `0 0 5px ${c}` : 'none', flexShrink: 0 }} />
}

function itemsOf(payload) {
  if (!payload) return []
  if (Array.isArray(payload.items)) return payload.items
  if (Array.isArray(payload)) return payload
  return []
}

// ── Detail JSON dump — the org shapes are intentionally opaque JSON, so a
// key/value table renders whatever monomind actually returned instead of
// guessing field names and silently dropping ones we didn't anticipate. ──
function KVBlock({ obj }) {
  if (!obj || typeof obj !== 'object') return null
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      {Object.entries(obj).map(([k, v]) => (
        <div key={k} style={{ display: 'flex', justifyContent: 'space-between', gap: 10 }}>
          <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1, flexShrink: 0 }}>{k}</span>
          <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-secondary)', textAlign: 'right', wordBreak: 'break-word' }}>
            {typeof v === 'object' ? JSON.stringify(v) : String(v)}
          </span>
        </div>
      ))}
    </div>
  )
}

function Card({ children }) {
  return (
    <div style={{ background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', padding: '10px 14px' }}>
      {children}
    </div>
  )
}

export default function OrgsPanel({ embedded = false, isOpen = true, onClose }) {
  const [orgs, setOrgs] = useState([])
  const [loadingOrgs, setLoadingOrgs] = useState(true)
  const [selected, setSelected] = useState(null) // org name
  const [tab, setTab] = useState('overview')
  const [data, setData] = useState({}) // { [tab]: payload }
  const [tabLoading, setTabLoading] = useState(false)
  const [events, setEvents] = useState([])
  const [actionBusy, setActionBusy] = useState(null) // id of in-flight approve/deny/answer

  const loadOrgs = useCallback(async (silent = false) => {
    if (!silent) setLoadingOrgs(true)
    try {
      const res = await api.listOrgs()
      setOrgs(itemsOf(res))
    } finally {
      if (!silent) setLoadingOrgs(false)
    }
  }, [])

  useEffect(() => { if (isOpen) loadOrgs() }, [isOpen, loadOrgs])

  // ── Tab data loading ──
  const loadTab = useCallback(async (orgName, tabId) => {
    if (!orgName) return
    setTabLoading(true)
    try {
      let payload = null
      switch (tabId) {
        case 'overview':  payload = await api.getOrgStatus(orgName); break
        case 'questions': payload = await api.getOrgQuestions(orgName); break
        case 'gates':     payload = await api.getOrgGates(orgName); break
        case 'logs':      payload = await api.getOrgLogs(orgName); break
        case 'costs':     payload = await api.getOrgCosts(orgName); break
        case 'flow':      payload = await api.getOrgFlow(orgName); break
        case 'decisions': payload = await api.getOrgDecisions(orgName); break
        default: break
      }
      setData(prev => ({ ...prev, [tabId]: payload }))
    } finally {
      setTabLoading(false)
    }
  }, [])

  useEffect(() => {
    if (selected) loadTab(selected, tab)
  }, [selected, tab, loadTab])

  // ── Live event tail ──
  useEffect(() => {
    if (!selected) return
    setEvents([])
    api.streamOrgEvents(selected)
    const offEvent = onOrgEvent((payload) => {
      if (payload?.orgName !== selected) return
      setEvents(prev => {
        const next = [...prev, payload.event]
        return next.length > 300 ? next.slice(-300) : next
      })
    })
    const offClosed = onOrgEventsClosed(() => {})
    return () => {
      offEvent()
      offClosed()
      api.stopOrgEvents(selected)
    }
  }, [selected])

  const selectOrg = (name) => {
    setSelected(name)
    setTab('overview')
    setData({})
  }

  const refreshTab = () => loadTab(selected, tab)

  // ── Actions ──
  const doAction = async (id, fn, successMsg) => {
    setActionBusy(id)
    try {
      const res = await fn()
      if (res?.error) notify('org action', res.error)
      else if (successMsg) notify('org action', successMsg)
      await refreshTab()
    } catch (e) {
      notify('org action', e.message || String(e))
    } finally {
      setActionBusy(null)
    }
  }

  const containerStyle = embedded
    ? { width: 420, flexShrink: 0, background: '#060b13', borderLeft: '1px solid rgba(0,180,216,0.1)', display: 'flex', flexDirection: 'column', overflow: 'hidden' }
    : { flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }

  if (embedded && !isOpen) return null

  return (
    <div style={containerStyle}>
      {embedded && (
        <div style={{ padding: '10px 12px', borderBottom: '1px solid rgba(0,180,216,0.1)', display: 'flex', alignItems: 'center', gap: 8, flexShrink: 0 }}>
          <Building2 size={13} style={{ color: 'var(--text-muted)' }} />
          <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, fontWeight: 700, color: '#e2e8f0', flex: 1, letterSpacing: 1 }}>ORGS</span>
          <button onClick={() => loadOrgs()} title="Refresh" style={{ background: 'transparent', border: 'none', cursor: 'pointer', color: 'var(--text-muted)', padding: 2, display: 'flex' }}>
            <RefreshCw size={12} />
          </button>
          {onClose && (
            <button onClick={onClose} title="Close panel" style={{ background: 'transparent', border: 'none', cursor: 'pointer', color: 'var(--text-muted)', padding: 2, display: 'flex' }}>
              <X size={14} />
            </button>
          )}
        </div>
      )}

      <div style={{ flex: 1, display: 'flex', overflow: 'hidden' }}>
        {/* Org list */}
        <div style={{ width: embedded ? 130 : 220, flexShrink: 0, borderRight: '1px solid var(--border)', overflowY: 'auto', padding: 8 }}>
          {loadingOrgs ? (
            <div style={{ display: 'flex', justifyContent: 'center', padding: 16 }}><div className="spinner" /></div>
          ) : orgs.length === 0 ? (
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', padding: 8, textAlign: 'center' }}>No orgs found.</div>
          ) : (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
              {orgs.map(o => (
                <button
                  key={o.name}
                  onClick={() => selectOrg(o.name)}
                  style={{
                    display: 'flex', alignItems: 'center', gap: 6, textAlign: 'left',
                    background: selected === o.name ? 'var(--elevated)' : 'transparent',
                    border: selected === o.name ? '1px solid var(--border-active)' : '1px solid transparent',
                    borderRadius: 'var(--radius)', padding: '6px 8px', cursor: 'pointer',
                  }}
                >
                  <StatusDot status={o.status} />
                  <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10.5, color: 'var(--text)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{o.name}</span>
                </button>
              ))}
            </div>
          )}
        </div>

        {/* Detail */}
        <div style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
          {!selected ? (
            <div className="empty-state" style={{ flex: 1 }}>
              <div className="empty-state-icon"><Building2 size={30} /></div>
              <div className="empty-state-title">Select an org</div>
              <div className="empty-state-desc">Pick an org from the list to see its status, questions, and gates.</div>
            </div>
          ) : (
            <>
              <div style={{ display: 'flex', gap: 4, padding: '8px', borderBottom: '1px solid var(--border)', overflowX: 'auto', flexShrink: 0 }}>
                {TABS.map(t => {
                  const Icon = t.icon
                  const active = tab === t.id
                  return (
                    <button
                      key={t.id}
                      onClick={() => setTab(t.id)}
                      style={{
                        display: 'flex', alignItems: 'center', gap: 4, whiteSpace: 'nowrap',
                        fontFamily: 'var(--font-mono)', fontSize: 10, padding: '5px 8px', borderRadius: 'var(--radius)',
                        background: active ? 'var(--elevated)' : 'transparent',
                        border: active ? '1px solid var(--border-active)' : '1px solid transparent',
                        color: active ? 'var(--text)' : 'var(--text-muted)', cursor: 'pointer',
                      }}
                    >
                      <Icon size={11} /> {t.label}
                    </button>
                  )
                })}
              </div>

              <div style={{ flex: 1, overflowY: 'auto', padding: 12, display: 'flex', flexDirection: 'column', gap: 10 }}>
                {tabLoading && <div style={{ display: 'flex', justifyContent: 'center', padding: 8 }}><div className="spinner" /></div>}

                {!tabLoading && tab === 'overview' && (
                  <>
                    <Card><KVBlock obj={data.overview} /></Card>
                    <div>
                      <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1, marginBottom: 6 }}>Live events</div>
                      <div style={{ display: 'flex', flexDirection: 'column', gap: 4, maxHeight: 260, overflowY: 'auto' }}>
                        {events.length === 0 && <div style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)' }}>Waiting for events…</div>}
                        {events.map((e, i) => (
                          <div key={i} style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-secondary)', background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius)', padding: '4px 8px', overflowWrap: 'anywhere' }}>
                            <span style={{ color: 'var(--cyan)' }}>{e.type}</span>{e.from ? ` ${e.from}→${e.to || '?'}` : ''}{e.subject ? `: ${e.subject}` : ''}{e.msg ? ` ${e.msg}` : ''}
                          </div>
                        ))}
                      </div>
                    </div>
                  </>
                )}

                {!tabLoading && tab === 'questions' && (
                  itemsOf(data.questions).length === 0
                    ? <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}>No pending questions.</div>
                    : itemsOf(data.questions).map((q, i) => {
                      const id = q.id || q.question_id || String(i)
                      return (
                        <Card key={id}>
                          <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                            <KVBlock obj={q} />
                            <QuestionAnswer
                              busy={actionBusy === id}
                              onSubmit={(answer) => doAction(id, () => api.answerOrgQuestion(selected, id, answer), 'Answered')}
                            />
                          </div>
                        </Card>
                      )
                    })
                )}

                {!tabLoading && tab === 'gates' && (
                  itemsOf(data.gates).length === 0
                    ? <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}>No pending gates.</div>
                    : itemsOf(data.gates).map((g, i) => {
                      const id = g.id || g.gate_id || String(i)
                      return (
                        <Card key={id}>
                          <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                            <KVBlock obj={g} />
                            <div style={{ display: 'flex', gap: 6 }}>
                              <button className="btn btn-primary btn-sm" disabled={actionBusy === id}
                                onClick={() => doAction(id, () => api.gateApproveOrgAction(selected, id, ''), 'Gate approved')}>
                                <CheckCircle2 size={11} /> Approve
                              </button>
                              <button className="btn btn-danger btn-sm" disabled={actionBusy === id}
                                onClick={() => doAction(id, () => api.gateRejectOrgAction(selected, id, ''), 'Gate rejected')}>
                                <XCircle size={11} /> Reject
                              </button>
                            </div>
                          </div>
                        </Card>
                      )
                    })
                )}

                {!tabLoading && tab === 'logs' && (
                  itemsOf(data.logs).length === 0
                    ? <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}>No log entries.</div>
                    : itemsOf(data.logs).map((e, i) => (
                      <div key={i} style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-secondary)', background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius)', padding: '4px 8px' }}>
                        <span style={{ color: 'var(--cyan)' }}>{e.type}</span>{e.from ? ` ${e.from}→${e.to || '?'}` : ''}{e.subject ? `: ${e.subject}` : ''}
                      </div>
                    ))
                )}

                {!tabLoading && tab === 'costs' && (
                  <>
                    {itemsOf(data.costs).length === 0
                      ? <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}>No cost data.</div>
                      : itemsOf(data.costs).map((c, i) => (
                        <Card key={i}><KVBlock obj={c} /></Card>
                      ))}
                    {data.costs?.totals && <Card><KVBlock obj={data.costs.totals} /></Card>}
                  </>
                )}

                {!tabLoading && tab === 'flow' && (
                  itemsOf(data.flow?.edges).length === 0
                    ? <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}>No flow data.</div>
                    : itemsOf(data.flow?.edges).map((e, i) => (
                      <div key={i} style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-secondary)', background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius)', padding: '6px 10px' }}>
                        {e.from} → {e.to}{e.subject ? `: ${e.subject}` : ''}
                      </div>
                    ))
                )}

                {!tabLoading && tab === 'decisions' && (
                  itemsOf(data.decisions).length === 0
                    ? <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}>No decisions recorded.</div>
                    : itemsOf(data.decisions).map((d, i) => (
                      <Card key={i}><KVBlock obj={d} /></Card>
                    ))
                )}
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  )
}

function QuestionAnswer({ onSubmit, busy }) {
  const [text, setText] = useState('')
  return (
    <div style={{ display: 'flex', gap: 6 }}>
      <input
        type="text"
        value={text}
        onChange={e => setText(e.target.value)}
        placeholder="Type an answer…"
        style={{ flex: 1, fontFamily: 'var(--font-mono)', fontSize: 11, background: 'var(--elevated)', border: '1px solid var(--border-bright)', borderRadius: 'var(--radius)', color: 'var(--text)', padding: '6px 8px', outline: 'none' }}
      />
      <button
        className="btn btn-primary btn-sm"
        disabled={busy || !text.trim()}
        onClick={() => { onSubmit(text.trim()); setText('') }}
      >
        Answer
      </button>
    </div>
  )
}
