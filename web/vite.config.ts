import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  // history 模式下必须用绝对根路径：相对路径在 /models 这类深层 URL
  // 刷新时会解析错位置，导致资源 404。
  base: '/',
  plugins: [vue(), tailwindcss()],
  build: {
    // 直接产出到 Go 侧 embed 目录，避免手工复制
    outDir: '../internal/admin/dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 1200,
  },
  server: {
    port: 5173,
    proxy: {
      // 开发期直连 Go 管理面，避免 CORS
      '/admin/api': 'http://127.0.0.1:8081',
    },
  },
})
