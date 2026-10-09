/** 单个插件/模组（与后端 service.PluginInfo 对应）。 */
export interface PluginInfo {
  /** 展示用文件名（已剥离 .disabled 后缀，按目录以 .jar 或 .zip 结尾）。 */
  name: string
  /** 所在目录：plugins | mods | resourcepacks | datapacks。 */
  dir: string
  /** 是否启用（true=原文件名，false=原文件名追加 .disabled）。 */
  enabled: boolean
  /** 字节数。 */
  size: number
  /** 修改时间（Unix 秒）。 */
  modTime: number
  /** 可选版本号：由后端解析 plugin.yml / mod metadata / pack.mcmeta 后返回。 */
  version?: string
  /** 可选作者：后端解析元信息后返回。 */
  author?: string
  /** 可选依赖摘要：后端解析元信息后返回。 */
  dependencies?: string[]
}

export interface PluginBatchDeployRequest {
  /** 从制品库选择的 type=plugin 资产 ID。 */
  assetIds: number[]
  /** 目标实例与筛选条件；ids 与 filter 二选一。 */
  target: {
    ids?: number[]
    filter?: {
      nodeId?: number
      status?: string
      role?: string
      q?: string
    }
  }
  destination?: 'plugins' | 'mods'
  overwrite?: boolean
}