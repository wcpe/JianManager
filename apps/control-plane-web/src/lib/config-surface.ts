/** 受管配置项来源：内联值 / 文件引用（二态互斥）。 */
export type ConfigSourceKind = 'inline' | 'file'

/** 受管配置项清单中的一行。 */
export interface ConfigSurfaceItem {
  itemKey: string
  source: ConfigSourceKind
  /** 是否已在登记表显式声明；false=旧实例的隐含默认（启动项内联 / props 文件隐含）。 */
  registered: boolean
  inlineValue?: string
  filePath?: string
  fileKey?: string
  /** 生效值：file 项为解析预览，inline 项即值本身。 */
  effectiveValue: string
  effectiveSource: 'inline' | 'file'
  editable: boolean
  previewError?: string
  /** 分组（如「启动参数」「网络与身份」），供 UI 分节。 */
  group?: string
  description?: string
  /** 字段类型（bool / int / string / enum…），供表单渲染控件。 */
  type?: string
  choices?: string[]
}

/** 按项更新来源的请求体。 */
export interface ConfigSurfaceUpdateItem {
  itemKey: string
  source: ConfigSourceKind
  /** 显式内联值；留空时后端按迁移语义推导（file→inline 取文件现值）。 */
  inlineValue?: string
  filePath?: string
  fileKey?: string
  message?: string
}