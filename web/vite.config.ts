import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  base: '/admin/',
  plugins: [react()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '^/admin/(?!assets/|@vite/|@fs/|node_modules/).+': {
        target: 'http://localhost:8765',
        changeOrigin: true,
      },
    },
  },
})
