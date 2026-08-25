import { useState, useEffect, useCallback } from 'react'
import { RefreshCw, Bot, MessageSquare, KeyRound, Terminal } from 'lucide-react'
import { api } from '../services/api.js'
import AIChatPanel from '../components/AIChatPanel.jsx'
import AIProviders from './AIProviders.jsx'

function statusColor(installed) {
  return installed ? 'var(--green-neon)' : 'var(--text-muted)'
}

function RuntimeTile({ agent, onChat }) {
  const [hov, setHov] = useState(false)
  return (
    <div
      onMouseEnter={() => setHov(true)}
      onMouseLeave={() => setHov(false)}
      style={{
        background: agent.installed ? 'linear-gradient(145deg,var(--elevated),var(--surface))' : 'var(--surface)',
        border: agent.installed ? '1px solid var(--border-active)' : hov ? '1px solid var(--border-bright)' : '1px solid var(--border)',
        borderRadius: 'var(--radius-lg)',
        padding: '16px 12px 12px',
        display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 8,
        transition: 'all var(--transition)',
        boxShadow: agent.installed ? 'var(--shadow-glow)' : 'none',
        minWidth: 0,
      }}
    >
      <Bot size={22} style={{ color: agent.installed ? 'var(--cyan)' : 'var(--text-muted)', opacity: agent.installed ? 1 : 0.5 }} />
      <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, fontWeight: 600, color: agent.installed ? 'var(--text)' : 'var(--text-secondary)', textAlign: 'center' }}>
        {agent.id}
      </span>
      <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
        <span style={{ width: 6, height: 6, borderRadius: '50%', background: statusColor(agent.installed), boxShadow: agent.installed ? '0 0 5px var(--green-neon)' : 'none', flexShrink: 0 }} />
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 9, color: 'var(--text-muted)' }}>
          {agent.installed ? (agent.version || 'installed') : 'not installed'}
        </span>
      </div>
      {agent.installed ? (
        <button className="btn btn-sm" onClick={() => onChat(agent.id)} style={{ gap: 4, marginTop: 2 }}>
          <MessageSquare size={11} /> Chat
        </button>
      ) : (
        agent.install_hint && (
          <div style={{ fontFamily: 'var(--font-mono)', fontSize: 8.5, color: 'var(--text-muted)', textAlign: 'center', lineHeight: 1.4, maxWidth: 140 }}>
            {agent.install_hint}
          </div>
        )
      )}
    </div>
  )
}

export default function Agents() {
  const [tab, setTab] = useState('agents')
  const [agents, setAgents] = useState([])
  const [scanError, setScanError] = useState(null)
  const [loading, setLoading] = useState(true)
  const [chatOpen, setChatOpen] = useState(false)
  const [chatRuntime, setChatRuntime] = useState('')

  const loadAgents = useCallback(async (silent = false) => {
    if (!silent) setLoading(true)
    try {
      const res = await api.scanAgentRuntimes()
      if (!res || res.error) {
        setScanError(res?.error || 'Unable to reach monomind.')
        setAgents([])
      } else {
        setScanError(null)
        setAgents(res.agents || [])
      }
    } finally {
      if (!silent) setLoading(false)
    }
  }, [])

  useEffect(() => { loadAgents() }, [loadAgents])

  const openChat = (runtimeID) => {
    setChatRuntime(runtimeID)
    setChatOpen(true)
  }

  const installedCount = agents.filter(a => a.installed).length

  return (
    <div style={{ display: 'flex', height: '100%', overflow: 'hidden' }}>
      <div style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
        <div className="page-header">
          <div className="page-header-left">
            <div className="page-title">Agents</div>
            <div className="page-subtitle">
              {loading ? 'Loading…' : scanError ? 'monomind unavailable' : `${installedCount} / ${agents.length} runtimes installed`}
            </div>
          </div>
          <div className="page-header-right" style={{ display: 'flex', gap: 6 }}>
            <button className="btn btn-ghost btn-sm" onClick={() => loadAgents()} style={{ gap: 5 }}><RefreshCw size={12} /> Refresh</button>
          </div>
        </div>

        <div style={{ display: 'flex', gap: 4, padding: '8px 16px 0', borderBottom: '1px solid var(--border)' }}>
          <button
            onClick={() => setTab('agents')}
            style={{
              display: 'flex', alignItems: 'center', gap: 5, fontFamily: 'var(--font-mono)', fontSize: 11,
              padding: '7px 12px', borderRadius: 'var(--radius) var(--radius) 0 0',
              background: tab === 'agents' ? 'var(--surface)' : 'transparent',
              border: tab === 'agents' ? '1px solid var(--border)' : '1px solid transparent',
              borderBottom: tab === 'agents' ? '1px solid var(--surface)' : 'none',
              color: tab === 'agents' ? 'var(--text)' : 'var(--text-muted)', cursor: 'pointer',
              marginBottom: -1,
            }}
          >
            <Terminal size={11} /> Agents
          </button>
          <button
            onClick={() => setTab('providers')}
            style={{
              display: 'flex', alignItems: 'center', gap: 5, fontFamily: 'var(--font-mono)', fontSize: 11,
              padding: '7px 12px', borderRadius: 'var(--radius) var(--radius) 0 0',
              background: tab === 'providers' ? 'var(--surface)' : 'transparent',
              border: tab === 'providers' ? '1px solid var(--border)' : '1px solid transparent',
              borderBottom: tab === 'providers' ? '1px solid var(--surface)' : 'none',
              color: tab === 'providers' ? 'var(--text)' : 'var(--text-muted)', cursor: 'pointer',
              marginBottom: -1,
            }}
          >
            <KeyRound size={11} /> Providers (legacy)
          </button>
        </div>

        {tab === 'agents' ? (
          <div className="page-body" style={{ flex: 1, overflow: 'auto' }}>
            {loading ? (
              <div className="empty-state"><div className="spinner" /></div>
            ) : scanError ? (
              <div className="empty-state">
                <div className="empty-state-icon"><Bot size={36} /></div>
                <div className="empty-state-title">
                  {/not found/i.test(scanError) ? 'monomind not installed' : 'monomind is out of date'}
                </div>
                <div className="empty-state-desc" style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                  <span>
                    Agents run through the local monomind engine. {/not found/i.test(scanError)
                      ? <>Install it with <code>npm install -g @monoes/monomindcli</code>.</>
                      : <>Update it with <code>npm install -g @monoes/monomindcli@latest</code>.</>}
                  </span>
                  <code style={{ fontSize: 10, color: 'var(--text-muted)', wordBreak: 'break-word', textAlign: 'left' }}>{scanError}</code>
                </div>
              </div>
            ) : agents.length === 0 ? (
              <div className="empty-state">
                <div className="empty-state-icon"><Bot size={36} /></div>
                <div className="empty-state-title">No agent runtimes detected</div>
                <div className="empty-state-desc">monomind is installed but found no known agent CLIs on this machine.</div>
              </div>
            ) : (
              <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(140px, 1fr))', gap: 10, paddingBottom: 24 }}>
                {agents.map(a => (
                  <RuntimeTile key={a.id} agent={a} onChat={openChat} />
                ))}
              </div>
            )}
          </div>
        ) : (
          <div style={{ flex: 1, overflow: 'hidden' }}>
            <AIProviders embedded />
          </div>
        )}
      </div>

      {tab === 'agents' && (
        <AIChatPanel
          workflowID="general"
          isOpen={chatOpen}
          initialRuntime={chatRuntime}
          canvasMode={false}
          onClose={() => setChatOpen(false)}
        />
      )}
    </div>
  )
}
