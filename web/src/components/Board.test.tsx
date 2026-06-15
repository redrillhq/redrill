import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import type { DrillView } from '../api'
import { api } from '../api'
import { Board } from './Board'

vi.mock('../api', () => ({
  api: { drills: vi.fn(), runs: vi.fn(), run: vi.fn(), trigger: vi.fn() },
}))

const mockApi = vi.mocked(api)

const provenDrill: DrillView = {
  drill: 'app-db',
  source: 'pg-dumps',
  headline_level: 'l1',
  stale: false,
  max_proof_age_seconds: 864000,
  last_proven: '2026-06-15T00:00:00Z',
  proofs: { l1: '2026-06-15T00:00:00Z' },
}

beforeEach(() => {
  vi.clearAllMocks()
  mockApi.runs.mockResolvedValue([])
})

describe('Board', () => {
  it('renders a card per drill and the within-SLA banner', async () => {
    mockApi.drills.mockResolvedValue([provenDrill])
    render(<Board />)
    expect(await screen.findByText('app-db')).toBeInTheDocument()
    expect(screen.getByText(/1 of 1 datasets proven within SLA/)).toBeInTheDocument()
  })

  it('counts stale drills as outside SLA', async () => {
    mockApi.drills.mockResolvedValue([{ ...provenDrill, stale: true }])
    render(<Board />)
    expect(await screen.findByText(/0 of 1 datasets proven within SLA/)).toBeInTheDocument()
  })

  it('triggers a run when "Run now" is clicked', async () => {
    mockApi.drills.mockResolvedValue([provenDrill])
    mockApi.trigger.mockResolvedValue('started')
    render(<Board />)
    await screen.findByText('app-db')
    fireEvent.click(screen.getByRole('button', { name: 'Run now' }))
    expect(mockApi.trigger).toHaveBeenCalledWith('app-db')
    expect(await screen.findByText('Run started')).toBeInTheDocument()
  })
})
