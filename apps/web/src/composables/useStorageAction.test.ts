import { reactive } from 'vue'
import { expect, it, vi } from 'vitest'
import { useStorageAction } from './useStorageAction'

it('discards a failed destructive confirmation and waits for a successful read before a new attempt', async () => {
  let finish!: () => void
  const scope = reactive({
    ready: true,
    reload: vi.fn(async () => {
      scope.ready = false
      await new Promise<void>(resolve => { finish = resolve })
    }),
  })
  const action = useStorageAction(scope)
  const write = vi.fn(async () => { throw new Error('Response lost') })
  action.confirm('clear', true)
  const pending = action.run('clear', write)
  await vi.waitFor(() => expect(scope.reload).toHaveBeenCalledOnce())
  expect(action.confirming.value).toBe('')
  expect(action.errors.clear).toBe('Response lost')
  expect(action.busy.value).toBe('clear')
  finish()
  await pending
  action.confirm('clear', true)
  await action.run('clear', write)
  expect(action.confirming.value).toBe('')
  expect(write).toHaveBeenCalledTimes(1)
  scope.ready = true
  expect(action.confirming.value).toBe('')
  action.confirm('clear', true)
  expect(action.confirming.value).toBe('clear')
})
