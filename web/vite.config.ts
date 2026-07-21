import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'path'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/buckley.ipc.v1.BuckleyIPC': {
        target: 'http://localhost:4488',
        changeOrigin: true,
      },
      '/api': {
        target: 'http://localhost:4488',
        changeOrigin: true,
      },
      '/ws': {
        target: 'ws://localhost:4488',
        ws: true,
      },
    },
  },
  build: {
    outDir: '../pkg/ipc/ui',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        // Stable names keep embedded asset verification reproducible across builders.
        entryFileNames: 'assets/[name].js',
        chunkFileNames: 'assets/[name].js',
        assetFileNames: 'assets/[name].[ext]',
      },
    },
  },
})
