import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwind from '@tailwindcss/vite'

const serverURL = process.env.ASTRAL_WEB_PROXY || 'http://127.0.0.1:4545'

export default defineConfig({
  plugins: [vue(), tailwind()],
  resolve: {
    alias: { '@': new URL('./src', import.meta.url).pathname },
    dedupe: ['vue', 'vee-validate', 'pinia', 'vue-router', 'markstream-vue'],
  },
  server: {
    proxy: {
      '/api': { target: serverURL, changeOrigin: false },
      '/auth': { target: serverURL, changeOrigin: false },
      '/openapi.json': { target: serverURL, changeOrigin: false },
    },
  },
  build: { chunkSizeWarningLimit: 1000 },
})
