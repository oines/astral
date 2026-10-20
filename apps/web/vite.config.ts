import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwind from '@tailwindcss/vite'

export default defineConfig({
  plugins: [vue(), tailwind()],
  resolve: {
    alias: { '@': new URL('./src', import.meta.url).pathname },
    dedupe: ['vue', 'vee-validate', 'pinia', 'vue-router', 'markstream-vue'],
  },
  server: {
    proxy: {
      '/api': { target: 'http://127.0.0.1:4545', changeOrigin: false },
      '/auth': { target: 'http://127.0.0.1:4545', changeOrigin: false },
      '/openapi.json': { target: 'http://127.0.0.1:4545', changeOrigin: false },
    },
  },
  build: { chunkSizeWarningLimit: 1000 },
})
