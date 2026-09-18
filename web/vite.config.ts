/// <reference types="vitest/config" />
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// dev server 将 /api 代理到 Go 后端（cmd/polydb-server，默认 127.0.0.1:8080），
// 避免浏览器跨域；控制面 body 是 msgpack 二进制，http-proxy 原样透传。
export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.{test,spec}.{ts,tsx}'],
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: process.env.POLYDB_SERVER_URL || 'http://127.0.0.1:8080',
        changeOrigin: true,
      },
      '/ws': {
        target: process.env.POLYDB_SERVER_URL || 'ws://127.0.0.1:8080',
        changeOrigin: true,
        ws: true,
      },
    },
  },
});