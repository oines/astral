import { defineConfig } from '@hey-api/openapi-ts'

export default defineConfig({
  input: './spec/openapi/astral.json',
  output: 'packages/sdk/src',
  plugins: [
    '@hey-api/typescript',
    '@hey-api/sdk',
    '@hey-api/client-fetch',
    {
      name: '@pinia/colada',
      $hooks: {
        operations: {
          isMutation: operation => operation.path.endsWith('/events') || operation.path.endsWith('/rollout') ? false : undefined,
        },
      },
    },
  ],
})
