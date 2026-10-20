import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  test: { include: ['apps/**/*.test.ts', 'packages/**/*.test.ts'], environment: 'node' },
})
