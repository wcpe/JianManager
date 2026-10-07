/**
 * 客户端分发域契约（FR-086 / FR-187 / FR-192，ADR-097）。
 * 与后端 model 对齐的纯数据类型，供 `@jianmanager/ui` 内视图逻辑与应用侧 api 共用。
 */

/** 客户端分发频道（FR-086）。每服一个，作为 manifest/制品端点的对外标识。 */
export interface ClientChannel {
  id: number
  /** 频道 slug（对外标识、URL 段）。 */
  channelId: string
  name: string
  description: string
  /** 当前 latest 版本指针占位（FR-088 编排）；0=未发布。 */
  currentVersion: number
  /** 频道下密钥数量（仅列表返回）。 */
  keyCount?: number
  createdAt: string
  updatedAt: string
}

/** 拉取密钥元数据（无明文）。明文仅创建/轮换时一次性返回，见 ClientKeyWithSecret。 */
export interface ClientPullKey {
  id: number
  name: string
  /** 明文前缀（如 jmck_ab12），仅供识别。 */
  keyPrefix: string
  revoked: boolean
  expiresAt: string | null
  lastUsedAt: string | null
  createdAt: string
  /** 是否可查看明文（= 后端存有可逆加密副本 KeyEnc；FR-192）。老哈希密钥为 false。 */
  revealable?: boolean
}

/** 频道详情（含密钥元数据列表）。 */
export interface ClientChannelDetail extends ClientChannel {
  keys: ClientPullKey[]
}

/** 创建/轮换密钥的响应：在元数据之外额外带一次性明文 `key`。 */
export interface ClientKeyWithSecret extends ClientPullKey {
  /** 一次性明文密钥；仅本次响应返回，不可二次读取。 */
  key: string
}
