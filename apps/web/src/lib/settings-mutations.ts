export type SettingsSaveState = {
  status: 'idle' | 'saving' | 'saved' | 'error' | 'uncertain'
  error?: string
}

export type SettingsOperation = {
  write: () => Promise<void>
  reconcile?: () => Promise<'applied' | 'not-applied' | 'unknown'>
}

type Job = { resource: string; key: string; operation: SettingsOperation; error?: string }

// A resource is saved serially, while feedback belongs to the field or local
// form. Queued edits of the same field coalesce; edits of other fields survive.
export function createSettingsMutations(report: (key: string, state: SettingsSaveState) => void, successDuration = 2200) {
  const queues = new Map<string, Promise<unknown>>()
  const jobs = new Map<string, Job>()
  const states = new Map<string, SettingsSaveState>()
  function set(job: Job, state: SettingsSaveState) {
    if (jobs.get(job.key) !== job) return
    states.set(job.key, state)
    report(job.key, state)
  }
  function complete(job: Job) {
    set(job, { status: 'saved' })
    setTimeout(() => {
      if (states.get(job.key)?.status === 'saved') set(job, { status: 'idle' })
    }, successDuration)
  }
  async function reconcile(job: Job) {
    try {
      const result = await job.operation.reconcile?.()
      if (result === 'applied') { complete(job); return true }
      set(job, { status: result === 'not-applied' ? 'error' : 'uncertain', error: job.error })
    } catch { set(job, { status: 'uncertain', error: job.error }) }
    return false
  }
  function enqueue<T>(resource: string, task: () => Promise<T>) {
    const pending = (queues.get(resource) || Promise.resolve()).then(task)
    const settled = pending.then(() => undefined, () => undefined)
    queues.set(resource, settled)
    void settled.then(() => { if (queues.get(resource) === settled) queues.delete(resource) })
    return pending
  }
  function run(resource: string, field: string, operation: SettingsOperation) {
    const job: Job = { resource, key: `${resource}/${field}`, operation }
    jobs.set(job.key, job)
    set(job, { status: 'saving' })
    return enqueue(resource, async () => {
      if (jobs.get(job.key) !== job) return false
      try {
        await operation.write()
        complete(job)
        return true
      } catch (error) {
        job.error = error && typeof error === 'object' && 'message' in error ? String(error.message) : ''
        // Structured API errors are definite rejections. A lost response may
        // follow a committed write: read it back before offering another write.
        if (error && typeof error === 'object' && 'code' in error && typeof error.code === 'string') {
          set(job, { status: 'error', error: job.error })
          return false
        }
        set(job, { status: 'uncertain', error: job.error })
        return reconcile(job)
      }
    })
  }
  async function retry(key: string) {
    const job = jobs.get(key)
    if (!job || states.get(key)?.status === 'saving') return
    if (states.get(key)?.status === 'uncertain') {
      set(job, { status: 'saving' })
      await enqueue(job.resource, () => reconcile(job))
    } else await run(job.resource, key.slice(job.resource.length + 1), job.operation)
  }
  return { run, retry }
}
