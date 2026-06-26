import { useQuery } from '@tanstack/react-query'

/** 单条依赖的许可信息（构建期由 scripts/gen-licenses.mjs 扫描生成，FR-135）。 */
export interface LicenseDependency {
  name: string
  version: string
  license: string
  author: string
  url: string
  /** 来源：web/bot-worker 为 npm，go 为 go.mod。 */
  scope: 'web' | 'bot-worker' | 'go'
  ecosystem: 'npm' | 'go'
  /** 运行时依赖 vs 开发依赖（Go 依赖一律 runtime）。 */
  type: 'runtime' | 'dev'
  /** 许可证全文（可能为空）。 */
  licenseText: string
}

/** 许可清单（`web/public/licenses.json`）。 */
export interface LicensesManifest {
  generatedAt: string
  dependencies: LicenseDependency[]
}

/**
 * 读取构建期生成的依赖许可清单（FR-135）。
 * 走静态资源 `/licenses.json`（非 `/api`，故用原生 fetch 而非 axios client）；
 * 数据构建期生成、不手维护，缺失时返回空清单走页面空态。
 */
export function useLicenses() {
  return useQuery({
    queryKey: ['licenses'],
    queryFn: async (): Promise<LicensesManifest> => {
      const res = await fetch(`${import.meta.env.BASE_URL}licenses.json`, { cache: 'no-cache' })
      if (!res.ok) throw new Error(`licenses.json ${res.status}`)
      return res.json()
    },
    staleTime: Infinity,
    retry: false,
  })
}
