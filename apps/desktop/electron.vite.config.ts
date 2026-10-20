import { defineConfig } from 'electron-vite'
import { fileURLToPath } from 'node:url'

const entry = (name: string) => fileURLToPath(new URL(`src/${name}/index.ts`, import.meta.url))
export default defineConfig({
  main: { build: { rolldownOptions: { input: entry('main') } } },
  preload: {
    build: {
      externalizeDeps: false,
      rolldownOptions: { input: entry('preload'), output: { format: 'cjs', entryFileNames: 'index.cjs' } },
    },
  },
  // The Go Server serves the shared apps/web build. There is no second renderer.
})
