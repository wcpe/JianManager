export interface ConfigFileInfo {
  path: string
  format: string
  size: number
  updatedAt: number
  supported: boolean
}

export interface ConfigField {
  key: string
  value: string
  type: string
  description?: string
  line?: number
}

export interface ValidationIssue {
  level: string
  message: string
  path?: string
  line?: number
  key?: string
}

export interface ConfigValidationResult {
  valid: boolean
  issues: ValidationIssue[]
}

export interface ConfigReadResult {
  path: string
  format: string
  content: string
  fields: ConfigField[]
  schemaJson: string
  validation: ConfigValidationResult
}

/** 单字段 schema 元数据（与后端 service/schema.FieldSchema 对应）。 */
export interface FieldSchema {
  key: string
  type: string
  default: string
  description: string
  group?: string
  choices?: string[]
}

/** 单配置文件 schema（与后端 service/schema.ModelSchema 对应）。 */
export interface ModelSchema {
  name: string
  description: string
  format: string
  fields: Record<string, FieldSchema>
}

/** 跨文件/跨实例一致性校验告警。 */
export interface CrossCheckIssue {
  level: string
  message: string
  key?: string
}

export interface ConfigVersion {
  id: number
  filePath: string
  message: string
  authorId: number
  createdAt: string
  rollbackOfVersionId?: number
}

export interface ConfigDiff {
  fromVersionId: number
  toVersionId: number
  unifiedDiff: string
  fromContent: string
  toContent: string
}

/** 递归发现的单个配置文件（与后端 service.DiscoveredConfig 对应，FR-071）。 */
export interface DiscoveredConfig {
  path: string
  format: string
  supported: boolean
}

export interface ConfigDiscoverResult {
  files: DiscoveredConfig[]
  truncated: boolean
}