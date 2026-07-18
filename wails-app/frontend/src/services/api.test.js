import { describe, it, expect, vi, beforeEach } from 'vitest'

// Mock the generated Wails bindings and runtime so api.js can be tested in
// isolation. Each binding is a vi.fn we can make resolve or reject per test.
vi.mock('../wailsjs/go/main/App', () => ({
  GetActions: vi.fn(),
  GetDashboardStats: vi.fn(),
  ListWorkflows: vi.fn(),
  CreateAction: vi.fn(),
}))
vi.mock('../wailsjs/runtime/runtime', () => ({
  EventsOn: vi.fn(),
  EventsOff: vi.fn(),
}))

import * as GoApp from '../wailsjs/go/main/App'
import { api, onApiError } from './api.js'

describe('api error handling', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('degrades a failed read to its safe default', async () => {
    GoApp.GetActions.mockRejectedValueOnce(new Error('db locked'))
    const result = await api.getActions()
    expect(result).toBeNull() // getActions falls back to null
  })

  it('broadcasts a failure on the error bus instead of swallowing it silently', async () => {
    const events = []
    const off = onApiError((detail) => events.push(detail))

    GoApp.GetDashboardStats.mockRejectedValueOnce(new Error('boom'))
    await api.getDashboardStats()

    expect(events).toHaveLength(1)
    expect(events[0].op).toBe('dashboard stats')
    expect(events[0].message).toContain('boom')
    off()
  })

  it('passes through a successful read unchanged', async () => {
    GoApp.ListWorkflows.mockResolvedValueOnce([{ id: 'wf1' }])
    const result = await api.listWorkflows()
    expect(result).toEqual([{ id: 'wf1' }])
  })

  it('does not intercept write-path methods (they propagate to the caller)', async () => {
    GoApp.CreateAction.mockRejectedValueOnce(new Error('validation'))
    await expect(api.createAction({})).rejects.toThrow('validation')
  })
})
