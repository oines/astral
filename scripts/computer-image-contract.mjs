import { createHash } from 'node:crypto'
import { fileURLToPath } from 'node:url'

// Pure shared contract: importing release metadata must not start image CLI work.
export const imageRepository = 'ghcr.io/oines/astral-computer'
export const recipePath = fileURLToPath(new URL('../internal/platform/computer-image/Dockerfile', import.meta.url))
export const lockPath = fileURLToPath(new URL('../internal/platform/computer-image/image-lock.json', import.meta.url))
export const platforms = ['linux/amd64', 'linux/arm64']
export const digestPattern = /^sha256:[a-f0-9]{64}$/
const hash = bytes => createHash('sha256').update(bytes).digest('hex')

export function recipeHash(bytes) {
  // The raw Dockerfile is the complete local build input. Never silently add a
  // COPY from the checkout without extending this versioned hash contract.
  const recipe = bytes.toString().replace(/\\\r?\n/g, ' ')
  for (const line of recipe.split('\n')) {
    if (/^\s*ADD\s/i.test(line) || /^\s*COPY\s/i.test(line) && !/^\s*COPY\s+--from=[\w.-]+\s/i.test(line)) throw new Error('Environment recipes may only COPY from named upstream stages, never the local checkout.')
    if (/^\s*ENTRYPOINT\s/i.test(line)) throw new Error('The environment image must not contain an application ENTRYPOINT.')
  }
  return hash(bytes)
}

export function validateLock(lock, recipeSHA) {
  if (lock?.schema_version !== 1 || lock.image !== imageRepository || !digestPattern.test(lock.digest || '') ||
      lock.recipe_sha256 !== recipeSHA || !/^[a-f0-9]{64}$/.test(recipeSHA || '') ||
      !new RegExp(`^env-${recipeSHA.slice(0, 12)}-[1-9][0-9]*-[1-9][0-9]*$`).test(lock.tag || '') ||
      !Array.isArray(lock.platforms) || lock.platforms.length !== 2 || [...lock.platforms].sort().join(',') !== platforms.join(',') ||
      !/^[a-f0-9]{40}$/.test(lock.source_sha || '') || !/^https:\/\/github\.com\/oines\/astral\/actions\/runs\/[1-9][0-9]*$/.test(lock.build_run_url || '') ||
      lock.tag.split('-').at(-2) !== lock.build_run_url.split('/').at(-1) ||
      typeof lock.created_at !== 'string' || !Number.isFinite(Date.parse(lock.created_at)) || new Date(lock.created_at).toISOString() !== lock.created_at) {
    throw new Error('Computer environment lock is missing, malformed, or does not match the current Dockerfile. Publish and verify a new environment lock.')
  }
  return lock
}

export function validateImageConfig(config, architecture, recipeSHA, sourceSHA) {
  const settings = config?.config
  if (config?.os !== 'linux' || config.architecture !== architecture || !settings ||
      settings.Entrypoint != null && (!Array.isArray(settings.Entrypoint) || settings.Entrypoint.length !== 0) ||
      settings.Cmd != null && (!Array.isArray(settings.Cmd) || settings.Cmd.some(value => typeof value !== 'string' || /astral/i.test(value))) ||
      settings.Labels?.['io.astral.environment.recipe-sha256'] !== recipeSHA ||
      settings.Labels?.['io.astral.environment.kind'] !== 'agent-tools' ||
      settings.Labels?.['org.opencontainers.image.revision'] !== sourceSHA) {
    throw new Error(`Published linux/${architecture} is not the matching daemon-free tools environment.`)
  }
}
