import assert from 'node:assert/strict'
import test from 'node:test'
import { requireReleaseCandidate } from './check-release-candidate.mjs'

const sourceSHA = 'a'.repeat(40)
const repository = 'oines/astral'
const token = 'private-test-token'
const run = (changes = {}) => ({ id: 123, path: '.github/workflows/release.yml', head_sha: sourceSHA, head_branch: 'main', event: 'workflow_dispatch', status: 'completed', conclusion: 'success', ...changes })
const jobs = () => [
  { name: 'Verify complete preview inventory and update feeds', status: 'completed', conclusion: 'success' },
  { name: 'publish', status: 'completed', conclusion: 'skipped' },
]
const json = value => new globalThis.Response(JSON.stringify(value), { headers: { 'Content-Type': 'application/json' } })

function transport(runs, jobList = jobs()) {
  const calls = []
  return {
    calls,
    request: async (url, options) => {
      calls.push(url)
      assert.equal(options.method, 'GET', 'Candidate validation must never mutate GitHub')
      assert.equal(options.redirect, 'error', 'The Actions token must not follow redirects')
      assert.equal(options.headers.Authorization, `Bearer ${token}`)
      const parsed = new URL(url)
      assert.equal(parsed.origin, 'https://api.github.com')
      if (parsed.pathname === `/repos/${repository}/actions/workflows/release.yml/runs`) {
        assert.equal(parsed.searchParams.get('head_sha'), sourceSHA)
        assert.equal(parsed.searchParams.get('branch'), 'main')
        assert.equal(parsed.searchParams.get('event'), 'workflow_dispatch')
        assert.equal(parsed.searchParams.get('status'), 'success')
        return json({ workflow_runs: typeof runs === 'function' ? runs(parsed.searchParams.get('page')) : runs })
      }
      assert.match(parsed.pathname, /^\/repos\/oines\/astral\/actions\/runs\/\d+\/jobs$/)
      assert.equal(parsed.searchParams.get('filter'), 'latest')
      return json({ jobs: typeof jobList === 'function' ? jobList(parsed.searchParams.get('page')) : jobList })
    },
  }
}
const verify = request => requireReleaseCandidate({ sourceSHA, repository, token, fetch: request })

test('accepts only the same-source successful main no-tag candidate with preview success and publication skipped', async () => {
  const { request, calls } = transport([run()])
  assert.equal(await verify(request), 'https://github.com/oines/astral/actions/runs/123')
  assert.equal(calls.length, 2)
})

test('rechecks source, event, branch, workflow and completed success instead of trusting API filters', async () => {
  for (const change of [{ head_sha: 'b'.repeat(40) }, { event: 'push' }, { head_branch: 'codex/candidate' }, { status: 'in_progress' }, { conclusion: 'cancelled' }, { conclusion: 'failure' }, { path: '.github/workflows/ci.yml' }, { id: 0 }]) {
    const { request, calls } = transport([run(change)])
    await assert.rejects(verify(request), /No successful no-tag Release candidate/)
    assert.equal(calls.length, 1, 'An unrelated run must not supply candidate jobs')
  }
})

test('a successful tagged manual Release is not an empty-tag candidate', async () => {
  const taggedJobs = jobs().map(job => ({ ...job, conclusion: job.name === 'publish' ? 'success' : 'skipped' }))
  await assert.rejects(verify(transport([run()], taggedJobs).request), /No successful no-tag Release candidate/)
})

test('missing, failed, running or ambiguous preview/publication evidence never permits a tag', async () => {
  const [preview, publication] = jobs()
  for (const list of [[], [preview], [publication], [{ ...preview, conclusion: 'failure' }, publication], [{ ...preview, status: 'in_progress' }, publication], [preview, { ...publication, conclusion: 'success' }], [preview, preview, publication], [preview, publication, publication]]) {
    await assert.rejects(verify(transport([run()], list).request), /No successful no-tag Release candidate/)
  }
})

test('finds a valid candidate after unrelated runs and reads the complete paginated job inventory', async () => {
  const [preview, publication] = jobs()
  const unrelated = Array.from({ length: 100 }, (_, index) => run({ id: index + 1, head_sha: 'b'.repeat(40) }))
  const otherJobs = Array.from({ length: 99 }, (_, index) => ({ name: `other-${index}`, status: 'completed', conclusion: 'success' }))
  const { request, calls } = transport(page => page === '1' ? unrelated : [run()], page => page === '1' ? [preview, ...otherJobs] : [publication])
  assert.equal(await verify(request), 'https://github.com/oines/astral/actions/runs/123')
  assert.equal(calls.length, 4)
})

test('read failures and invalid evidence fail closed without exposing tokens or upstream body text', async () => {
  for (const request of [
    async () => { throw new Error(`Network failure with ${token}`) },
    async () => new globalThis.Response(`upstream body ${token}`, { status: 403 }),
    async () => new globalThis.Response(`invalid JSON ${token}`),
    async () => json({ workflow_runs: null }),
  ]) {
    await assert.rejects(verify(request), error => {
      assert.doesNotMatch(error.message, /private-test-token|upstream body|invalid JSON private/)
      return /GitHub Actions candidate/.test(error.message)
    })
  }
  for (const options of [{ sourceSHA: '' }, { repository: '../another-host' }, { token: '' }]) {
    await assert.rejects(requireReleaseCandidate({ sourceSHA, repository, token, fetch: () => { throw new Error('No API call expected') }, ...options }), /SOURCE_SHA|GITHUB_REPOSITORY|actions:read/)
  }
})
