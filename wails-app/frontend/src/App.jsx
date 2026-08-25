import { useState, useEffect, useCallback } from 'react'
import Sidebar from './components/Sidebar.jsx'
import StatusBar from './components/StatusBar.jsx'
import Toasts from './components/Toasts.jsx'
import ErrorBoundary from './components/ErrorBoundary.jsx'
import ConfirmHost from './components/ConfirmDialog.jsx'
import Dashboard from './pages/Dashboard.jsx'
import Actions from './pages/Actions.jsx'
import People from './pages/People.jsx'
import Profile from './pages/Profile.jsx'
import PostDetail from './pages/PostDetail.jsx'
import Connections from './pages/Connections.jsx'
import Communications from './pages/Communications.jsx'
import Agents from './pages/Agents.jsx'
import Orgs from './pages/Orgs.jsx'
import Logs from './pages/Logs.jsx'
import NodeRunner from './pages/NodeRunner.jsx'
import SettingsPage from './pages/Settings.jsx'
import ImageVault from './pages/ImageVault.jsx'
import Vault from './pages/Vault.jsx'
import HumanInLoop from './pages/HumanInLoop.jsx'
import { api, onLogEntry, onActionComplete } from './services/api.js'

export default function App() {
  const [activePage, setActivePage] = useState('dashboard')
  const [navData, setNavData] = useState(null) // extra data passed to a page (e.g., executionId)
  const [profileId, setProfileId] = useState(null)
  const [postId, setPostId] = useState(null)
  const [dbConnected, setDbConnected] = useState(false)
  const [stats, setStats] = useState(null)
  const [logs, setLogs] = useState([])
  const [peopleRefreshKey, setPeopleRefreshKey] = useState(0)

  const openProfile = useCallback((id) => {
    setProfileId(id)
    setActivePage('profile')
  }, [])

  const closeProfile = useCallback(() => {
    setActivePage('people')
    setProfileId(null)
  }, [])

  const openPost = useCallback((id) => {
    setPostId(id)
    setActivePage('postDetail')
  }, [])

  const closePost = useCallback(() => {
    setPostId(null)
    setActivePage('profile')
  }, [])

  const navigate = useCallback((page, data) => {
    if (page !== 'postDetail') setPostId(null)
    if (page !== 'profile' && page !== 'postDetail') setProfileId(null)
    setNavData(data || null)
    setActivePage(page)
  }, [])

  // Initial data load
  useEffect(() => {
    const checkDB = async () => {
      const connected = await api.isDBConnected()
      setDbConnected(!!connected)
    }
    const loadStats = async () => {
      const s = await api.getDashboardStats()
      if (s) setStats(s)
    }
    const loadLogs = async () => {
      const l = await api.getLogs()
      if (l) setLogs(l)
    }
    checkDB()
    loadStats()
    loadLogs()
  }, [])

  // Live log streaming
  useEffect(() => {
    const off = onLogEntry((entry) => {
      setLogs(prev => {
        const next = [...prev, entry]
        return next.length > 500 ? next.slice(-500) : next
      })
    })
    return off
  }, [])

  // Action completion refresh
  useEffect(() => {
    const off = onActionComplete(async () => {
      const s = await api.getDashboardStats()
      if (s) setStats(s)
      setPeopleRefreshKey(k => k + 1)
    })
    return off
  }, [])

  const refreshStats = useCallback(async () => {
    const s = await api.getDashboardStats()
    if (s) setStats(s)
  }, [])

  const pages = {
    dashboard: <Dashboard stats={stats} onRefresh={refreshStats} onNavigate={navigate} />,
    noderunner: <NodeRunner onNavigate={navigate} navData={navData} />,
    actions: <Actions onRefresh={refreshStats} />,
    hil: <HumanInLoop />,
    people:    <People key={peopleRefreshKey} onProfile={openProfile} />,
    communications: <Communications onProfile={openProfile} />,
    profile:   <Profile id={profileId} onBack={closeProfile} onOpenURL={api.openURL} onOpenPost={openPost} />,
    postDetail: <PostDetail id={postId} onBack={closePost} onOpenURL={api.openURL} />,
    connections: <Connections onRefresh={refreshStats} />,
    vault: <ImageVault />,
    secretsVault: <Vault />,
    ai: <Agents />,
    orgs: <Orgs />,
    logs:      <Logs logs={logs} onClear={() => { api.clearLogs(); setLogs([]) }} />,
    settings:  <SettingsPage onNavigate={setActivePage} />,
  }

  return (
    <div className="app-layout">
      <Sidebar
        activePage={activePage}
        onNavigate={navigate}
        stats={stats}
        dbConnected={dbConnected}
      />
      <main className="main-content">
        <ErrorBoundary key={activePage}>
          {pages[activePage] || pages.dashboard}
        </ErrorBoundary>
      </main>
      <StatusBar stats={stats} dbConnected={dbConnected} />
      <Toasts />
      <ConfirmHost />
    </div>
  )
}
