import OrgsPanel from '../components/OrgsPanel.jsx'

export default function Orgs() {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', overflow: 'hidden' }}>
      <div className="page-header">
        <div className="page-header-left">
          <div className="page-title">Orgs</div>
          <div className="page-subtitle">Local agent organizations (monomind Org Runtime v2)</div>
        </div>
      </div>
      <div className="page-body" style={{ flex: 1, overflow: 'hidden', display: 'flex' }}>
        <OrgsPanel />
      </div>
    </div>
  )
}
