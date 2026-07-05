import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'path'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: [
      { find: '@jianmanager/ui/styles.css', replacement: path.resolve(__dirname, '../packages/ui/src/styles.css') },
      { find: /^@jianmanager\/ui\/(.+)$/, replacement: `${path.resolve(__dirname, '../packages/ui/src')}/$1` },
      { find: '@jianmanager/ui', replacement: path.resolve(__dirname, '../packages/ui/src/index.ts') },
    ],
  },
  server: {
    port: 5174,
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
