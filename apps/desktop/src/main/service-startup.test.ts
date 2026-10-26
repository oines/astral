import { describe, expect, it } from 'vitest'
import { maintenanceDeadline } from './service'
describe('managed startup maintenance budget', () => {
  it('extends startup only for the exact managed process doing data maintenance', () => {
    expect(maintenanceDeadline(1000, 42, { pid: 42, phase: 'preparing-data' })).toBe(1_201_000)
    expect(maintenanceDeadline(1000, 42, undefined)).toBeUndefined()
    expect(maintenanceDeadline(1000, 42, { pid: 41, phase: 'preparing-data' })).toBeUndefined()
    expect(maintenanceDeadline(1000, 42, { pid: 42, phase: 'ready' })).toBeUndefined()
    expect(maintenanceDeadline(1000, undefined, { pid: 42, phase: 'preparing-data' })).toBeUndefined()
  })
})
