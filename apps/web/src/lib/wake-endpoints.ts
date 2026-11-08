import { deleteWakeEndpoint, listWakeEndpoints, updateWakeEndpoint, type AstralInternalDomainWakeEndpoint, type AstralInternalWakePatch } from '@astral/sdk'
import { request } from './api'

export type WakeEndpoint = AstralInternalDomainWakeEndpoint
export type WakePatch = Omit<AstralInternalWakePatch, 'revision'>
export const wakeAPI = {
  list: (agentID?: string) => request(listWakeEndpoints({ query: agentID ? { agent_id: agentID } : undefined })),
  update: (endpoint: WakeEndpoint, input: WakePatch) => request(updateWakeEndpoint({ path: { id: endpoint.id }, body: { revision: endpoint.revision, ...input } })),
  remove: (endpoint: WakeEndpoint) => request(deleteWakeEndpoint({ path: { id: endpoint.id }, query: { revision: endpoint.revision } })),
}

export function wakeBlocked(endpoint: WakeEndpoint, now: number) {
  return !!endpoint.blocked_until && Date.parse(endpoint.blocked_until) > now
}

const shellQuote = (value: string) => `'${value.replaceAll("'", "'\\''")}'`
export function wakeExample(endpoint: WakeEndpoint, eventID: string, text: string) {
  return ['curl --fail-with-body', shellQuote(endpoint.trigger_url || ''), '-H', "'Content-Type: application/json'", '--data-raw', shellQuote(JSON.stringify({ event_id: eventID, text }))].join(' ')
}
