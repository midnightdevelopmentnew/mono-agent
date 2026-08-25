import { useState, useEffect, useRef, useCallback } from 'react'
import { X, Send, Trash2, ChevronDown, ChevronRight, Loader, Square } from 'lucide-react'
import { api, onAIChunk, onAITool, onAIError } from '../services/api.js'

// ── Tool call card (collapsible) ───────────────────────────────────────────────
function ToolCallCard({ tool, args, result }) {
  const [open, setOpen] = useState(false)
  return (
    <div style={{
      background: '#020509',
      border: '1px solid rgba(0,180,216,0.12)',
      borderRadius: 8,
      marginTop: 6,
      overflow: 'hidden',
    }}>
      <div
        onClick={() => setOpen(o => !o)}
        style={{
          display: 'flex', alignItems: 'center', gap: 6,
          padding: '6px 10px',
          cursor: 'pointer',
          userSelect: 'none',
        }}
      >
        {open ? <ChevronDown size={10} color="#00b4d8" /> : <ChevronRight size={10} color="#00b4d8" />}
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: '#00b4d8', fontWeight: 600 }}>
          {tool}
        </span>
      </div>
      {open && (
        <div style={{ padding: '0 10px 8px', display: 'flex', flexDirection: 'column', gap: 6 }}>
          {args && (
            <div>
              <div style={{ fontFamily: 'var(--font-mono)', fontSize: 8, color: 'var(--text-muted)', letterSpacing: 1.5, textTransform: 'uppercase', marginBottom: 3 }}>Args</div>
              <pre style={{
                margin: 0, fontFamily: 'var(--font-mono)', fontSize: 10,
                color: '#94a3b8', whiteSpace: 'pre-wrap', wordBreak: 'break-word',
                maxHeight: 120, overflow: 'auto',
              }}>
                {typeof args === 'string' ? args : JSON.stringify(args, null, 2)}
              </pre>
            </div>
          )}
          {result && (
            <div>
              <div style={{ fontFamily: 'var(--font-mono)', fontSize: 8, color: 'var(--text-muted)', letterSpacing: 1.5, textTransform: 'uppercase', marginBottom: 3 }}>Result</div>
              <pre style={{
                margin: 0, fontFamily: 'var(--font-mono)', fontSize: 10,
                color: '#94a3b8', whiteSpace: 'pre-wrap', wordBreak: 'break-word',
                maxHeight: 120, overflow: 'auto',
              }}>
                {typeof result === 'string' ? result : JSON.stringify(result, null, 2)}
              </pre>
            </div>
          )}
        </div>
      )}
    </div>
  )
}

// ── Message bubble ─────────────────────────────────────────────────────────────
function MessageBubble({ role, content, toolCalls, isError }) {
  const isUser = role === 'user'
  return (
    <div style={{
      display: 'flex',
      flexDirection: 'column',
      alignItems: isUser ? 'flex-end' : 'flex-start',
      marginBottom: 8,
    }}>
      <div style={{
        maxWidth: '88%',
        padding: '8px 12px',
        borderRadius: isUser ? '12px 12px 4px 12px' : '12px 12px 12px 4px',
        background: isError
          ? 'rgba(239,68,68,0.1)'
          : isUser
            ? 'rgba(0,180,216,0.15)'
            : '#0d1a28',
        border: isError
          ? '1px solid rgba(239,68,68,0.25)'
          : isUser
            ? '1px solid rgba(0,180,216,0.25)'
            : '1px solid rgba(0,180,216,0.08)',
        color: isError ? '#fca5a5' : '#e2e8f0',
      }}>
        <div style={{
          fontFamily: 'var(--font-mono)', fontSize: 11,
          lineHeight: 1.55,
          whiteSpace: 'pre-wrap',
          wordBreak: 'break-word',
        }}>
          {content}
        </div>
        {toolCalls && toolCalls.length > 0 && (
          <div style={{ marginTop: 4 }}>
            {toolCalls.map((tc, i) => (
              <ToolCallCard key={i} tool={tc.tool} args={tc.args} result={tc.result} />
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

// ── Main panel ─────────────────────────────────────────────────────────────────
export default function AIChatPanel({ workflowID, isOpen, onClose, onWorkflowCreated, initialRuntime, canvasMode = true }) {
  const [messages, setMessages]             = useState([])
  const [input, setInput]                   = useState('')
  const [streaming, setStreaming]           = useState(false)
  const [currentContent, setCurrentContent] = useState('')
  const [currentToolCalls, setCurrentToolCalls] = useState([])
  const [providers, setProviders]           = useState([])
  const [selectedProvider, setSelectedProvider] = useState('')
  const [selectedModel, setSelectedModel]   = useState('')
  const [runtimes, setRuntimes]             = useState([])
  const [selectedRuntime, setSelectedRuntime] = useState('')
  const [useAgents, setUseAgents]           = useState(false)
  const [monomindMissing, setMonomindMissing] = useState(false)

  const messagesEndRef   = useRef(null)
  const textareaRef      = useRef(null)
  const createdWfIdRef   = useRef(null)

  // ── Load local agent runtimes on mount (monomind delegation) ───────────
  useEffect(() => {
    api.scanAgentRuntimes().then(res => {
      if (!res || res.error) { setMonomindMissing(true); return }
      const installed = (res.agents || []).filter(a => a.installed)
      setRuntimes(installed)
      if (installed.length > 0) {
        const preferred = initialRuntime && installed.some(a => a.id === initialRuntime)
          ? initialRuntime
          : installed[0].id
        setSelectedRuntime(preferred)
        setUseAgents(true) // prefer local agents when any is installed
      }
    })
  }, [initialRuntime])

  // ── Load providers on mount ──────────────────────────────────────────────
  useEffect(() => {
    api.listAIProviders().then(list => {
      const active = (list || []).filter(p => p.status === 'active')
      setProviders(active)
      if (active.length > 0 && !selectedProvider) {
        setSelectedProvider(String(active[0].id))
        setSelectedModel(active[0].default_model || '')
      }
    })
  }, [])

  // ── Load chat history when workflowID changes ───────────────────────────
  useEffect(() => {
    if (!workflowID) return
    api.getAIChatHistory(workflowID).then(history => {
      if (!Array.isArray(history)) { setMessages([]); return }
      // Filter out raw tool-result messages (role=tool) — they are internal.
      // For assistant messages, parse tool_calls JSON and map to display shape.
      setMessages(
        history
          .filter(m => m.role !== 'tool')
          .map(m => {
            let toolCalls = null
            if (m.tool_calls) {
              try {
                const parsed = JSON.parse(m.tool_calls)
                if (Array.isArray(parsed) && parsed.length > 0) {
                  toolCalls = parsed.map(tc => ({
                    tool: tc.function?.name || tc.id || 'unknown',
                    args: tc.function?.arguments || '',
                    result: null,
                  }))
                }
              } catch { /* ignore malformed */ }
            }
            return {
              role: m.role,
              content: m.content || '',
              toolCalls,
            }
          })
      )
    })
  }, [workflowID])

  // ── Subscribe to streaming events ───────────────────────────────────────
  useEffect(() => {
    const offChunk = onAIChunk((data) => {
      if (data.workflowID !== workflowID) return
      if (data.done) {
        // Streaming finished — finalize the assistant message (guard against double-fire)
        setStreaming(prev => {
          if (!prev) return false // already finalized
          setCurrentContent(content => {
            const final = content + (data.content || '')
            if (!final) return '' // nothing to add
            setCurrentToolCalls(prevTC => {
              setMessages(msgs => [
                ...msgs,
                { role: 'assistant', content: final, toolCalls: prevTC.length > 0 ? prevTC : null },
              ])
              return []
            })
            return ''
          })
          // Navigate to newly created workflow after all tool calls are done
          if (createdWfIdRef.current && onWorkflowCreated) {
            const id = createdWfIdRef.current
            createdWfIdRef.current = null
            // Small delay to let final DB writes settle
            setTimeout(() => onWorkflowCreated(id), 300)
          }
          return false
        })
      } else {
        setCurrentContent(prev => prev + (data.content || ''))
      }
    })

    const offTool = onAITool((data) => {
      if (data.workflowID !== workflowID) return
      setCurrentToolCalls(prev => [
        ...prev,
        { tool: data.tool, args: data.args, result: data.result },
      ])
      // Track newly created workflow ID for auto-navigate after stream completes
      if (data.tool === 'create_workflow' && data.result) {
        try {
          const res = JSON.parse(data.result)
          if (res.workflow_id) createdWfIdRef.current = res.workflow_id
        } catch { /* ignore */ }
      }
    })

    const offError = onAIError((data) => {
      if (data.workflowID !== workflowID) return
      setStreaming(false)
      setCurrentContent('')
      setCurrentToolCalls([])
      setMessages(msgs => [
        ...msgs,
        { role: 'error', content: data.error || 'Unknown error' },
      ])
    })

    return () => { offChunk(); offTool(); offError() }
  }, [workflowID])

  // ── Auto-scroll to bottom ───────────────────────────────────────────────
  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages, currentContent, currentToolCalls])

  // ── Send message ────────────────────────────────────────────────────────
  const send = useCallback(async () => {
    const text = input.trim()
    if (!text || streaming || !workflowID) return
    if (useAgents && !selectedRuntime) return
    if (!useAgents && !selectedProvider) return

    setMessages(msgs => [...msgs, { role: 'user', content: text }])
    setInput('')
    setStreaming(true)
    setCurrentContent('')
    setCurrentToolCalls([])

    try {
      if (useAgents) {
        await api.streamAgentChat(workflowID, text, selectedRuntime, selectedModel, canvasMode)
      } else {
        await api.streamAIChat(workflowID, text, selectedProvider, selectedModel)
      }
    } catch (err) {
      setStreaming(false)
      setMessages(msgs => [
        ...msgs,
        { role: 'error', content: String(err) },
      ])
    }
  }, [input, streaming, workflowID, useAgents, selectedRuntime, selectedProvider, selectedModel, canvasMode])

  // Whether a backend is actually selected for the current mode — gates the
  // input, matching send()'s own guard (useAgents ? selectedRuntime : selectedProvider).
  const hasBackend = useAgents ? !!selectedRuntime : !!selectedProvider

  // ── Stop an in-flight stream ──────────────────────────────────────────────
  const stop = useCallback(async () => {
    if (!workflowID) return
    if (useAgents) {
      await api.stopAgentChat(workflowID)
    } else {
      await api.stopAIChat(workflowID)
    }
    setStreaming(false)
  }, [workflowID, useAgents])

  // ── Clear history ───────────────────────────────────────────────────────
  const clearHistory = useCallback(async () => {
    if (!workflowID) return
    await api.clearAIChatHistory(workflowID)
    setMessages([])
    setCurrentContent('')
    setCurrentToolCalls([])
  }, [workflowID])

  // ── Handle key down in textarea ─────────────────────────────────────────
  const handleKeyDown = (e) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      send()
    }
  }

  // ── Provider change ─────────────────────────────────────────────────────
  const handleProviderChange = (e) => {
    const id = e.target.value
    setSelectedProvider(id)
    const p = providers.find(p => String(p.id) === id)
    if (p) setSelectedModel(p.default_model || '')
  }

  if (!isOpen) return null

  return (
    <div style={{
      width: 380, flexShrink: 0,
      background: '#060b13',
      borderLeft: '1px solid rgba(0,180,216,0.1)',
      display: 'flex', flexDirection: 'column',
      overflow: 'hidden',
    }}>
      {/* ── Header ── */}
      <div style={{
        padding: '10px 12px',
        borderBottom: '1px solid rgba(0,180,216,0.1)',
        display: 'flex', alignItems: 'center', gap: 8,
        flexShrink: 0,
      }}>
        <span style={{
          fontFamily: 'var(--font-mono)', fontSize: 11, fontWeight: 700,
          color: '#e2e8f0', flex: 1, letterSpacing: 1,
        }}>
          AI ASSISTANT
        </span>
        <button
          onClick={clearHistory}
          title="Clear chat history"
          style={{
            background: 'transparent', border: 'none', cursor: 'pointer',
            color: 'var(--text-muted)', padding: 2, display: 'flex', alignItems: 'center',
            transition: 'color 100ms',
          }}
          onMouseEnter={e => e.currentTarget.style.color = '#ef4444'}
          onMouseLeave={e => e.currentTarget.style.color = 'var(--text-muted)'}
        >
          <Trash2 size={12} />
        </button>
        <button
          onClick={onClose}
          title="Close panel"
          style={{
            background: 'transparent', border: 'none', cursor: 'pointer',
            color: 'var(--text-muted)', padding: 2, display: 'flex', alignItems: 'center',
            transition: 'color 100ms',
          }}
          onMouseEnter={e => e.currentTarget.style.color = '#fff'}
          onMouseLeave={e => e.currentTarget.style.color = 'var(--text-muted)'}
        >
          <X size={13} />
        </button>
      </div>

      {/* ── Runtime / Provider / Model selectors ── */}
      <div style={{
        padding: '8px 12px',
        borderBottom: '1px solid rgba(0,180,216,0.06)',
        display: 'flex', gap: 6,
        flexShrink: 0,
      }}>
        {(runtimes.length > 0 || providers.length > 0) && (
          <select
            value={useAgents ? 'agents' : 'providers'}
            onChange={e => setUseAgents(e.target.value === 'agents')}
            title="Chat backend"
            style={{
              background: '#020509',
              border: '1px solid rgba(0,180,216,0.15)',
              borderRadius: 6,
              padding: '4px 6px',
              color: '#e2e8f0',
              fontFamily: 'var(--font-mono)', fontSize: 10,
              outline: 'none',
            }}
          >
            {runtimes.length > 0 && <option value="agents">agents</option>}
            {providers.length > 0 && <option value="providers">providers</option>}
          </select>
        )}
        {useAgents ? (
          <select
            value={selectedRuntime}
            onChange={e => setSelectedRuntime(e.target.value)}
            title="Locally installed AI agent (via monomind)"
            style={{
              flex: 1,
              background: '#020509',
              border: '1px solid rgba(0,180,216,0.15)',
              borderRadius: 6,
              padding: '4px 8px',
              color: '#e2e8f0',
              fontFamily: 'var(--font-mono)', fontSize: 10,
              outline: 'none',
            }}
          >
            {runtimes.length === 0 && (
              <option value="">
                {monomindMissing ? 'monomind missing — npm i -g @monoes/monomindcli' : 'No agent runtimes'}
              </option>
            )}
            {runtimes.map(r => (
              <option key={r.id} value={r.id}>{r.id}</option>
            ))}
          </select>
        ) : (
          <select
            value={selectedProvider}
            onChange={handleProviderChange}
            style={{
              flex: 1,
              background: '#020509',
              border: '1px solid rgba(0,180,216,0.15)',
              borderRadius: 6,
              padding: '4px 8px',
              color: '#e2e8f0',
              fontFamily: 'var(--font-mono)', fontSize: 10,
              outline: 'none',
            }}
          >
            {providers.length === 0 && <option value="">No providers</option>}
            {providers.map(p => (
              <option key={p.id} value={String(p.id)}>{p.name}</option>
            ))}
          </select>
        )}
        <input
          type="text"
          value={selectedModel}
          onChange={e => setSelectedModel(e.target.value)}
          placeholder="Model"
          style={{
            flex: 1,
            background: '#020509',
            border: '1px solid rgba(0,180,216,0.15)',
            borderRadius: 6,
            padding: '4px 8px',
            color: '#e2e8f0',
            fontFamily: 'var(--font-mono)', fontSize: 10,
            outline: 'none',
          }}
        />
      </div>

      {/* ── Messages area ── */}
      <div style={{
        flex: 1,
        overflowY: 'auto',
        padding: '12px',
        display: 'flex', flexDirection: 'column',
      }}>
        {messages.length === 0 && !streaming && (
          <div style={{
            flex: 1,
            display: 'flex', flexDirection: 'column',
            alignItems: 'center', justifyContent: 'center', gap: 8,
            color: 'var(--text-muted)',
          }}>
            <span style={{ fontSize: 24, opacity: 0.15 }}>AI</span>
            <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, textAlign: 'center', lineHeight: 1.6 }}>
              {workflowID === 'general'
                ? <>Chat with your connected AI providers.<br />Ask anything.</>
                : <>Ask the AI about your workflow,<br />request changes, or get help.</>
              }
            </span>
          </div>
        )}

        {messages.map((msg, i) => (
          <MessageBubble
            key={i}
            role={msg.role}
            content={msg.content}
            toolCalls={msg.toolCalls}
            isError={msg.role === 'error'}
          />
        ))}

        {/* Streaming assistant bubble */}
        {streaming && (currentContent || currentToolCalls.length > 0) && (
          <MessageBubble
            role="assistant"
            content={currentContent || '...'}
            toolCalls={currentToolCalls.length > 0 ? currentToolCalls : null}
          />
        )}

        {/* Streaming indicator when no content yet */}
        {streaming && !currentContent && currentToolCalls.length === 0 && (
          <div style={{
            display: 'flex', alignItems: 'center', gap: 6,
            padding: '8px 0',
          }}>
            <Loader size={12} style={{ animation: 'spin 0.7s linear infinite', color: '#00b4d8' }} />
            <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)' }}>
              Thinking...
            </span>
          </div>
        )}

        <div ref={messagesEndRef} />
      </div>

      {/* ── Input area ── */}
      <div style={{
        padding: '8px 12px 10px',
        borderTop: '1px solid rgba(0,180,216,0.1)',
        display: 'flex', flexDirection: 'column', gap: 6,
        flexShrink: 0,
      }}>
        {!hasBackend && (
          <div style={{ padding: '8px 12px', background: 'rgba(251,191,36,.08)', border: '1px solid rgba(251,191,36,.2)', borderRadius: 'var(--radius)', fontFamily: 'var(--font-mono)', fontSize: 10, color: '#fbbf24' }}>
            {useAgents ? 'Select an agent runtime above to start chatting' : 'Select an AI provider above to start chatting'}
          </div>
        )}
        <div style={{ display: 'flex', gap: 6 }}>
        <textarea
          ref={textareaRef}
          value={input}
          onChange={e => setInput(e.target.value)}
          onKeyDown={handleKeyDown}
          placeholder="Type a message..."
          rows={1}
          style={{
            flex: 1,
            background: '#020509',
            border: '1px solid rgba(0,180,216,0.15)',
            borderRadius: 8,
            padding: '8px 10px',
            color: '#e2e8f0',
            fontFamily: 'var(--font-mono)', fontSize: 11,
            outline: 'none',
            resize: 'none',
            minHeight: 36,
            maxHeight: 120,
            lineHeight: 1.4,
          }}
          onInput={e => {
            e.target.style.height = 'auto'
            e.target.style.height = Math.min(e.target.scrollHeight, 120) + 'px'
          }}
        />
        {streaming ? (
          <button
            onClick={stop}
            title="Stop generating"
            aria-label="Stop generating"
            style={{
              background: 'rgba(239,68,68,0.15)',
              border: '1px solid rgba(239,68,68,0.35)',
              borderRadius: 8,
              padding: '0 12px',
              cursor: 'pointer',
              color: '#ef4444',
              display: 'flex', alignItems: 'center',
              transition: 'all 100ms',
              flexShrink: 0,
            }}
            onMouseEnter={e => { e.currentTarget.style.background = 'rgba(239,68,68,0.25)' }}
            onMouseLeave={e => { e.currentTarget.style.background = 'rgba(239,68,68,0.15)' }}
          >
            <Square size={12} fill="#ef4444" />
          </button>
        ) : (
          <button
            onClick={send}
            disabled={!input.trim() || !hasBackend}
            title={!hasBackend ? (useAgents ? 'No runtime selected' : 'No provider selected') : 'Send message'}
            aria-label="Send message"
            style={{
              background: !input.trim() || !hasBackend ? 'rgba(0,180,216,0.05)' : 'rgba(0,180,216,0.15)',
              border: `1px solid ${!input.trim() || !hasBackend ? 'rgba(0,180,216,0.08)' : 'rgba(0,180,216,0.3)'}`,
              borderRadius: 8,
              padding: '0 12px',
              cursor: !input.trim() || !hasBackend ? 'default' : 'pointer',
              color: !input.trim() || !hasBackend ? 'var(--text-muted)' : '#00b4d8',
              display: 'flex', alignItems: 'center',
              transition: 'all 100ms',
              flexShrink: 0,
            }}
            onMouseEnter={e => { if (input.trim() && hasBackend) e.currentTarget.style.background = 'rgba(0,180,216,0.25)' }}
            onMouseLeave={e => { if (input.trim() && hasBackend) e.currentTarget.style.background = 'rgba(0,180,216,0.15)' }}
          >
            <Send size={13} />
          </button>
        )}
        </div>
      </div>
    </div>
  )
}
