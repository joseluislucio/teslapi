import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import tailwindcss from '@tailwindcss/vite'

// El build va embebido en el binario de Go (internal/api/dist).
export default defineConfig({
  plugins: [svelte(), tailwindcss()],
  build: {
    outDir: '../daemon/internal/api/dist',
    emptyOutDir: true,
    target: 'es2022',
    chunkSizeWarningLimit: 400,
  },
  server: {
    proxy: {
      '/api': { target: 'http://localhost:8080', changeOrigin: true },
      '/media': { target: 'http://localhost:8080', changeOrigin: true },
    },
  },
})
