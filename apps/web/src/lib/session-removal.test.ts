import { describe, expect, it } from 'vitest'
import { createSessionCatalog } from './session-removal'

describe('reconciling deleted session trees', () => {
  it('follows internal delegation relationships without deleting ordinary Fork copies', () => {
    const catalog = createSessionCatalog()
    catalog.observe([
      { id: 'root', agent_id: 'bot' },
      { id: 'child', agent_id: 'bot', subagent_parent_id: 'root' },
      { id: 'grandchild', agent_id: 'bot', subagent_parent_id: 'child' },
      { id: 'fork', agent_id: 'bot' },
    ])
    expect([...catalog.tree('root')]).toEqual(['root', 'child', 'grandchild'])
    catalog.forget(catalog.tree('child'))
    expect(catalog.observe([{ id: 'root', agent_id: 'bot' }, { id: 'fork', agent_id: 'bot' }])).toEqual([])
  })

  it('does not mistake a live queued child absent from the catalog for a deletion', () => {
    const catalog = createSessionCatalog()
    catalog.observe([{ id: 'root', agent_id: 'bot' }])
    catalog.remember([{ id: 'queued', agent_id: 'bot', subagent_parent_id: 'root' }])
    expect(catalog.observe([{ id: 'root', agent_id: 'bot' }])).toEqual([])
    expect([...catalog.tree('root')]).toEqual(['root', 'queued'])
    catalog.observe([{ id: 'root', agent_id: 'bot' }, { id: 'queued', agent_id: 'bot', subagent_parent_id: 'root' }])
    expect(catalog.observe([{ id: 'root', agent_id: 'bot' }])).toEqual(['queued'])
  })

  it('retains a removed ancestor relationship until the caller clears its entire tree', () => {
    const catalog = createSessionCatalog()
    catalog.observe([{ id: 'root', agent_id: 'bot' }, { id: 'child', agent_id: 'bot', subagent_parent_id: 'root' }])
    catalog.remember([{ id: 'grandchild', agent_id: 'bot', subagent_parent_id: 'child' }])
    expect(catalog.observe([])).toEqual(['root', 'child'])
    expect([...catalog.tree('root')]).toEqual(['root', 'child', 'grandchild'])
  })
})
