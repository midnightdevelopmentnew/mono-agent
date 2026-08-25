// Thin wrapper around Wails Go bindings with error handling.
import * as GoApp from '../wailsjs/go/main/App'
import { EventsOn, EventsOff } from '../wailsjs/runtime/runtime'

// Global error bus. Read methods degrade to safe defaults ([]/null/0) so pages
// keep rendering, but every failure is also broadcast on `api:error` so a toast
// can surface it — otherwise a dead backend is indistinguishable from empty data.
const errorBus = new EventTarget()

export function onApiError(callback) {
  const handler = (e) => callback(e.detail)
  errorBus.addEventListener('api:error', handler)
  return () => errorBus.removeEventListener('api:error', handler)
}

function reportError(op, e) {
  const message = e?.message || String(e)
  console.warn(`API error (${op}):`, e)
  errorBus.dispatchEvent(new CustomEvent('api:error', { detail: { op, message } }))
}

// notify surfaces a message on the same toast bus for non-API failures, so UI
// code can report errors without a blocking native alert().
export function notify(op, message) {
  errorBus.dispatchEvent(new CustomEvent('api:error', { detail: { op, message: String(message) } }))
}

// Wrap a binding call so failures are reported and fall back to `fallback`.
const guard = (op, fallback) => (e) => { reportError(op, e); return fallback }

export const api = {
  getDashboardStats:    () => GoApp.GetDashboardStats().catch(guard('dashboard stats', null)),
  listWorkflows:        () => GoApp.ListWorkflows().catch(guard('list workflows', [])),
  runWorkflow:          (id) => GoApp.RunWorkflow(id).catch(e => { reportError('run workflow', e); return `error: ${e}` }),
  setWorkflowActive:    (id, active) => GoApp.SetWorkflowActive(id, active),
  getRecentExecutions:  (limit = 20) => GoApp.GetRecentExecutions(limit).catch(guard('recent executions', [])),
  getWorkflowExecutions:(id, limit = 20) => GoApp.GetWorkflowExecutions(id, limit).catch(guard('workflow executions', [])),
  getExecutionDetail:   (id) => GoApp.GetExecutionDetail(id).catch(guard('execution detail', null)),
  cancelWorkflow:       (id) => GoApp.CancelWorkflow(id).catch(e => { reportError('cancel workflow', e); return `error: ${e}` }),
  getActions:       (platform = '', state = '', limit = 0) => GoApp.GetActions(platform, state, limit).catch(guard('get actions', null)),
  getAction:        (id) => GoApp.GetAction(id).catch(guard('get action', null)),
  createAction:     (req) => GoApp.CreateAction(req),
  updateActionState:(id, state) => GoApp.UpdateActionState(id, state),
  deleteAction:     (id) => GoApp.DeleteAction(id),
  updateActionParams:(id, params) => GoApp.UpdateActionParams(id, params),
  executeAction:    (id) => GoApp.ExecuteAction(id),
  getTargets:       (actionId) => GoApp.GetActionTargets(actionId).catch(guard('get targets', null)),
  addTarget:        (actionId, link, platform) => GoApp.AddActionTarget(actionId, link, platform),
  getPeople:        (platform = '', search = '', limit = 50, offset = 0) => GoApp.GetPeople(platform, search, limit, offset).catch(guard('get people', null)),
  getPeopleCount:   (platform = '', search = '') => GoApp.GetPeopleCount(platform, search).catch(guard('people count', 0)),
  getSessions:      () => GoApp.GetSessions().catch(guard('get sessions', null)),
  deleteSession:    (id) => GoApp.DeleteSession(id),
  getSocialLists:   () => GoApp.GetSocialLists().catch(guard('social lists', null)),
  getTemplates:     () => GoApp.GetTemplates().catch(guard('templates', null)),
  getLogs:          () => GoApp.GetLogs().catch(guard('logs', [])),
  clearLogs:        () => GoApp.ClearLogs(),
  getAvailableActionTypes: () => GoApp.GetAvailableActionTypes().catch(guard('action types', {})),
  getDBPath:        () => GoApp.GetDBPath().catch(guard('db path', '')),
  exportData:       () => GoApp.ExportData(),
  isDBConnected:    () => GoApp.IsDBConnected().catch(() => false),
  openURL:          (url) => GoApp.OpenURL(url).catch(console.warn),
  getPersonDetail:      (id) => GoApp.GetPersonDetail(id).catch(guard('person detail', null)),
  getPersonInteractions:(id) => GoApp.GetPersonInteractions(id).catch(guard('person interactions', [])),
  getPersonPosts:   (personId) => GoApp.GetPersonPosts(personId).catch(guard('person posts', [])),
  getPersonMessages:(personId) => GoApp.GetPersonMessages(personId).catch(guard('person messages', [])),
  getAllPersonMessages:(limit) => GoApp.GetAllPersonMessages(limit ?? 200).catch(guard('all messages', [])),
  composePersonMessage:(personId, connectionId, subject, body, asDraft) => GoApp.ComposePersonMessage(personId, connectionId, subject, body, asDraft),
  getDraftPersonMessages: () => GoApp.GetDraftPersonMessages().catch(guard('draft messages', [])),
  sendDraftPersonMessage: (id) => GoApp.SendDraftPersonMessage(id),
  rejectDraftPersonMessage: (id) => GoApp.RejectDraftPersonMessage(id),
  getLatestPersonStatus: (personId) => GoApp.GetLatestPersonStatus(personId).catch(guard('latest status', null)),
  addPersonStatus:       (personId, text) => GoApp.AddPersonStatus(personId, text).catch(guard('add status', null)),
  getPersonStatusHistory:(personId, limit) => GoApp.GetPersonStatusHistory(personId, limit ?? 0).catch(guard('status history', [])),
  getPostDetail:    (postId)   => GoApp.GetPostDetail(postId).catch(guard('post detail', null)),
  getPostComments:  (postId)   => GoApp.GetPostComments(postId).catch(guard('post comments', [])),
  getAllTags:            ()  => GoApp.GetAllTags().catch(guard('tags', [])),
  getPersonTags:        (personId) => GoApp.GetPersonTags(personId).catch(guard('person tags', [])),
  addPersonTag:         (personId, name, color) => GoApp.AddPersonTag(personId, name, color).catch(guard('add tag', null)),
  removePersonTag:      (personId, tagId) => GoApp.RemovePersonTag(personId, tagId).catch(e => reportError('remove tag', e)),
  getPeopleTagsMap:     (ids) => GoApp.GetPeopleTagsMap(ids).catch(guard('tags map', {})),
  listConnections:      (platform = '') => GoApp.ListConnections(platform).catch(guard('list connections', [])),
  listPlatforms:        (connectVia = '') => GoApp.ListPlatformsJSON(connectVia).then(s => JSON.parse(s)).catch(guard('list platforms', [])),
  testConnection:       (id) => GoApp.TestConnection(id).catch(e => { reportError('test connection', e); return `error: ${e}` }),
  testSession:          (id) => GoApp.TestSession(id).catch(e => { reportError('test session', e); return `error: ${e}` }),
  removeConnection:     (id) => GoApp.RemoveConnection(id).catch(e => { reportError('remove connection', e); return `error: ${e}` }),
  getConnectionsForPlatform: (platformID) => GoApp.GetConnectionsForPlatform(platformID).catch(guard('connections for platform', [])),
  saveConnectionDirect: (platformID, method, fieldValues) =>
    GoApp.SaveConnectionDirect(platformID, method, JSON.stringify(fieldValues))
      .catch(e => { reportError('save connection', e); return `error: ${e}` }),
  connectPlatformOAuth:   (platformID)                       => GoApp.ConnectPlatformOAuth(platformID),
  loginSocial:            (platform)                         => GoApp.LoginSocial(platform),
  confirmSocialLogin:     (platform)                         => GoApp.ConfirmSocialLogin(platform),
  getOAuthCredentials:    (platformID)                       => GoApp.GetOAuthCredentials(platformID).catch(guard('oauth credentials', '')),
  setOAuthCredentials:    (platformID, clientID, clientSecret) => GoApp.SetOAuthCredentials(platformID, clientID, clientSecret),
  // AI Providers
  listAIProviders:    () => GoApp.ListAIProviders().then(s => JSON.parse(s)).catch(guard('list AI providers', [])),
  saveAIProvider:     (provider) => GoApp.SaveAIProvider(JSON.stringify(provider)).then(s => JSON.parse(s)),
  deleteAIProvider:   (id) => GoApp.DeleteAIProvider(id).then(s => JSON.parse(s)),
  testAIProvider:     (id) => GoApp.TestAIProvider(id).then(s => JSON.parse(s)),
  getAIModels:        (providerID) => GoApp.GetAIModels(providerID).then(s => JSON.parse(s)).catch(guard('AI models', [])),
  getAIRegistry:      () => GoApp.GetAIRegistry().then(s => JSON.parse(s)).catch(guard('AI registry', [])),
  // AI Chat
  streamAIChat:       (workflowID, message, providerID, model) => GoApp.StreamAIChat(workflowID, message, providerID, model).then(s => JSON.parse(s)),
  stopAIChat:         (workflowID) => GoApp.StopAIChat(workflowID).then(s => JSON.parse(s)).catch(guard('stop AI chat', null)),
  getAIChatHistory:   (workflowID) => GoApp.GetAIChatHistory(workflowID).then(s => JSON.parse(s)).catch(guard('chat history', [])),
  clearAIChatHistory: (workflowID) => GoApp.ClearAIChatHistory(workflowID).then(s => JSON.parse(s)),
  // Agent Chat (monomind delegation — local AI agent runtimes)
  scanAgentRuntimes:  () => GoApp.ScanAgentRuntimes().then(s => JSON.parse(s)).catch(guard('scan agent runtimes', null)),
  streamAgentChat:    (workflowID, message, runtime, model, canvas = true) => GoApp.StreamAgentChat(workflowID, message, runtime, model, canvas).then(s => JSON.parse(s)),
  stopAgentChat:      (workflowID) => GoApp.StopAgentChat(workflowID).then(s => JSON.parse(s)).catch(guard('stop agent chat', null)),
  // Orgs (monomind Org Runtime v2)
  listOrgs:           () => GoApp.ListOrgs().then(s => JSON.parse(s)).catch(guard('list orgs', null)),
  getOrgStatus:       (name = '') => GoApp.GetOrgStatus(name).then(s => JSON.parse(s)).catch(guard('org status', null)),
  getOrgLogs:         (name) => GoApp.GetOrgLogs(name).then(s => JSON.parse(s)).catch(guard('org logs', null)),
  getOrgReport:       (name, all = false) => GoApp.GetOrgReport(name, all).then(s => JSON.parse(s)).catch(guard('org report', null)),
  getOrgCosts:        (name) => GoApp.GetOrgCosts(name).then(s => JSON.parse(s)).catch(guard('org costs', null)),
  getOrgFlow:         (name) => GoApp.GetOrgFlow(name).then(s => JSON.parse(s)).catch(guard('org flow', null)),
  getOrgQuestions:    (name) => GoApp.GetOrgQuestions(name).then(s => JSON.parse(s)).catch(guard('org questions', null)),
  getOrgGates:        (name) => GoApp.GetOrgGates(name).then(s => JSON.parse(s)).catch(guard('org gates', null)),
  getOrgDecisions:    (name) => GoApp.GetOrgDecisions(name).then(s => JSON.parse(s)).catch(guard('org decisions', null)),
  getOrgMemoryStats:  (name) => GoApp.GetOrgMemoryStats(name).then(s => JSON.parse(s)).catch(guard('org memory stats', null)),
  answerOrgQuestion:  (name, questionID, answer) => GoApp.AnswerOrgQuestion(name, questionID, answer).then(s => JSON.parse(s)),
  approveOrgAction:   (name, role, action) => GoApp.ApproveOrgAction(name, role, action).then(s => JSON.parse(s)),
  denyOrgAction:      (name, role, action) => GoApp.DenyOrgAction(name, role, action).then(s => JSON.parse(s)),
  gateApproveOrgAction: (name, gateID, resolution = '') => GoApp.GateApproveOrgAction(name, gateID, resolution).then(s => JSON.parse(s)),
  gateRejectOrgAction:  (name, gateID, resolution = '') => GoApp.GateRejectOrgAction(name, gateID, resolution).then(s => JSON.parse(s)),
  streamOrgEvents:    (orgName) => GoApp.StreamOrgEvents(orgName).then(s => JSON.parse(s)),
  stopOrgEvents:      (orgName) => GoApp.StopOrgEvents(orgName).then(s => JSON.parse(s)).catch(guard('stop org events', null)),
}

export function onLogEntry(callback) {
  EventsOn('log:entry', callback)
  return () => EventsOff('log:entry')
}

export function onActionComplete(callback) {
  EventsOn('action:complete', callback)
  return () => EventsOff('action:complete')
}

export function onConnectionProgress(callback) {
  EventsOn('conn:progress', callback)
  return () => EventsOff('conn:progress')
}

export function onConnectionDone(callback) {
  EventsOn('conn:done', callback)
  return () => EventsOff('conn:done')
}

export function onConnectionOpened(callback) {
  EventsOn('conn:opened', callback)
  return () => EventsOff('conn:opened')
}

export function onAIChunk(callback) {
  EventsOn('ai:chunk', callback)
  return () => EventsOff('ai:chunk')
}

export function onAITool(callback) {
  EventsOn('ai:tool', callback)
  return () => EventsOff('ai:tool')
}

export function onAIError(callback) {
  EventsOn('ai:error', callback)
  return () => EventsOff('ai:error')
}

export function onOrgEvent(callback) {
  EventsOn('org:event', callback)
  return () => EventsOff('org:event')
}

export function onOrgEventsClosed(callback) {
  EventsOn('org:eventsClosed', callback)
  return () => EventsOff('org:eventsClosed')
}

export const PLATFORMS = ['INSTAGRAM', 'LINKEDIN', 'X', 'TIKTOK']
export const STATES = ['PENDING', 'RUNNING', 'PAUSED', 'COMPLETED', 'FAILED', 'CANCELLED']

export const PLATFORM_COLORS = {
  INSTAGRAM: '#e1306c',
  LINKEDIN:  '#0077b5',
  X:         '#e7e9ea',
  TIKTOK:    '#ff0050',
  EMAIL:     '#6366f1',
  TELEGRAM:  '#26a5e4',
}

export const STATE_COLORS = {
  PENDING:   '#94a3b8',
  RUNNING:   '#00f5d4',
  PAUSED:    '#eab308',
  COMPLETED: '#10b981',
  FAILED:    '#ef4444',
  CANCELLED: '#6b7280',
}
