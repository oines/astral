import { isDirectInvocation } from './release-version.mjs'
import { URLSearchParams } from 'node:url'

const workflow = '.github/workflows/release.yml'
const previewJob = 'Verify complete preview inventory and update feeds'
const publicationJob = 'publish'
const pageSize = 100
const pageLimit = 10

// Read-only Actions evidence: an ordinary successful manual run is insufficient,
// because a manual run can also select an existing tag and publish a Release.
export async function requireReleaseCandidate({ sourceSHA, repository, token, fetch: request = globalThis.fetch }) {
  if (!/^[a-f0-9]{40}$/.test(sourceSHA || '')) throw new Error('SOURCE_SHA must identify the exact release source commit.')
  if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository || '') || repository.split('/').some(part => part === '.' || part === '..')) throw new Error('GITHUB_REPOSITORY is required.')
  if (typeof token !== 'string' || !token) throw new Error('A job-scoped GitHub token with actions:read is required.')
  const prefix = `https://api.github.com/repos/${repository}`
  async function api(route) {
    let response
    try {
      response = await request(`${prefix}${route}`, {
        method: 'GET', redirect: 'error', signal: AbortSignal.timeout(15_000),
        headers: { Authorization: `Bearer ${token}`, Accept: 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28', 'User-Agent': 'Astral-release-candidate-check' },
      })
    } catch { throw new Error('GitHub Actions candidate evidence could not be read.') }
    if (!response.ok) throw new Error(`GitHub Actions candidate evidence failed (HTTP ${response.status}).`)
    try { return await response.json() } catch { throw new Error('GitHub Actions candidate evidence is not valid JSON.') }
  }
  function page(value, field) {
    const items = value?.[field]
    if (!Array.isArray(items) || items.length > pageSize) throw new Error('GitHub Actions candidate inventory is invalid.')
    return items
  }
  async function candidateJobs(id) {
    const jobs = []
    for (let current = 1; current <= pageLimit; current++) {
      const items = page(await api(`/actions/runs/${id}/jobs?filter=latest&per_page=${pageSize}&page=${current}`), 'jobs')
      jobs.push(...items)
      if (items.length < pageSize) return jobs
    }
    throw new Error('GitHub Actions candidate jobs exceed the verification limit.')
  }
  for (let current = 1; current <= pageLimit; current++) {
    const query = new URLSearchParams({ branch: 'main', event: 'workflow_dispatch', status: 'success', head_sha: sourceSHA, per_page: String(pageSize), page: String(current) })
    const runs = page(await api(`/actions/workflows/release.yml/runs?${query}`), 'workflow_runs')
    for (const run of runs) {
      if (!Number.isSafeInteger(run?.id) || run.id <= 0 || run.head_sha !== sourceSHA || run.head_branch !== 'main' || run.event !== 'workflow_dispatch' ||
          run.status !== 'completed' || run.conclusion !== 'success' || typeof run.path !== 'string' || run.path.split('@')[0] !== workflow) continue
      const jobs = await candidateJobs(run.id)
      const preview = jobs.filter(job => job?.name === previewJob)
      const publication = jobs.filter(job => job?.name === publicationJob)
      if (preview.length === 1 && preview[0].status === 'completed' && preview[0].conclusion === 'success' &&
          publication.length === 1 && publication[0].status === 'completed' && publication[0].conclusion === 'skipped') {
        return `https://github.com/${repository}/actions/runs/${run.id}`
      }
    }
    if (runs.length < pageSize) break
  }
  throw new Error('No successful no-tag Release candidate exists for this exact source on main. Run release.yml from main with an empty tag and wait for every candidate job before creating the version tag.')
}

if (await isDirectInvocation(import.meta.url)) {
  requireReleaseCandidate({ sourceSHA: process.env.SOURCE_SHA, repository: process.env.GITHUB_REPOSITORY, token: process.env.GH_TOKEN || process.env.GITHUB_TOKEN })
    .then(url => console.log(`Verified successful no-tag Release candidate: ${url}`))
    .catch(error => { console.error(error.message); process.exitCode = 1 })
}
