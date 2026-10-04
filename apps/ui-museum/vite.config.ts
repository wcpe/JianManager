import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// @jianmanager/ui 经 pnpm workspace 真依赖解析（源码 exports，Vite 直接转译），不再 alias（FR-283）。
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    port: 5174,
    // 监听所有网络接口：默认 127.0.0.1 只有本机可达，而外壳预览需要从局域网设备查看
    // （手机/平板验证响应式、同事评审）。这是纯本地开发服务器，不含生产数据。
    host: true,
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
