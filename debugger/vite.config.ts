import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import path from 'path'

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@carbon/react': path.resolve(__dirname, 'node_modules/@carbon/react'),
      '@carbon/styles': path.resolve(__dirname, 'node_modules/@carbon/styles'),
    },
  },
  css: {
    preprocessorOptions: {
      scss: {
        includePaths: ['node_modules'],
      },
    },
  },
  server: {
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/api/, ''),
      },
      '/kernel-api': { target: 'http://localhost:8090', changeOrigin: true, rewrite: (path) => path.replace(/^\/kernel-api/, '') },
    },
  },
})
