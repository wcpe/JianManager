/// <reference types="vite/client" />

/** 前端版本号，由 Vite `define` 在构建时注入（见 vite.config.ts）。 */
declare const __APP_VERSION__: string

interface ImportMetaEnv {
  /** mock 模式开关：VITE_MOCK=1 时 main.tsx 启 MSW worker、整站打到内存假后端（FR-196）。 */
  readonly VITE_MOCK?: string
}
