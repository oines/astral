import { describe, expect, it } from 'vitest'
import type { Computer } from './types'
import { computerEnvironment, computerKind, computerStorage, computerSummary, containerIdentity } from './computer-info'

const computer: Computer = { id: 'computer', name: 'Development', kind: 'container', local: false, online: true, revoked: false, revision: 1, created_at: '', updated_at: '', hostname: 'orbstack', os: 'linux', arch: 'aarch64', shell: '/bin/bash', home: '/root', root: '/var/lib/astral' }

describe('Computer information scopes', () => {
  it('keeps a display name, container name and host identity distinct', () => {
    const current = { ...computer, container: { name: 'astral-example', host: 'Mac-mini', image: 'astral-computer:123', volume: 'astral-example-data', mount_path: '/var/lib/astral' } }
    expect(computerSummary(current, key => key)).toBe('computerInfo.kind.container · Linux · ARM64 · astral-example')
    expect(containerIdentity(current)).toContainEqual({ key: 'computerInfo.containerName', value: 'astral-example', copy: true })
    expect(containerIdentity(current)).toContainEqual({ key: 'computerInfo.host', value: 'Mac-mini', copy: true })
    expect(computerEnvironment(current)).toContainEqual({ key: 'computerInfo.hostname', value: 'orbstack' })
  })

  it('does not use an internal hostname as a missing container name or host', () => {
    expect(containerIdentity(computer).every(fact => fact.value === undefined)).toBe(true)
    expect(computerKind({ ...computer, kind: 'remote', local: false })).toBe('remote')
    expect(computerKind({ ...computer, kind: 'local', local: true })).toBe('local')
  })

  it('keeps the reported data directory distinct from the operating-system user home', () => {
    expect(computerStorage(computer)).toContainEqual({ key: 'computerInfo.userDirectory', value: '/root', copy: true })
    expect(computerStorage(computer)).toContainEqual({ key: 'computerInfo.dataDirectory', value: '/var/lib/astral', copy: true })
    expect(computerStorage({ ...computer, root: undefined })).toContainEqual({ key: 'computerInfo.dataDirectory', value: undefined, copy: true })
  })
})
