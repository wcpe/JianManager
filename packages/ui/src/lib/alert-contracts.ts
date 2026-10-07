/**
 * 告警域契约（FR-085 / FR-494，ADR-097）。
 * 与后端 model 对齐的纯数据类型，供 `@jianmanager/ui` 内视图逻辑与应用侧 api 共用。
 */

/** 通道连接配置（凭证子字段经 ${ENV} 引用）。 */
export interface ChannelConfig {
  url?: string
  token?: string
  chatId?: string
  host?: string
  port?: number
  username?: string
  password?: string
  from?: string
  to?: string
  /** QQ 机器人 AppID（FR-494，明文）。 */
  appId?: string
  /** QQ 机器人密钥（FR-494，须 ${ENV_VAR} 引用）。 */
  appSecret?: string
  /** QQ 投递目标类型：仅 c2c=单聊（FR-494；group 已被平台拒绝，前端不再暴露）。 */
  targetType?: string
  /** QQ 目标 user_openid（单聊）（FR-494，明文）；扫码绑定时自动填入。 */
  targetId?: string
  /** QQ 开放平台 API 根地址（FR-494，可选，留空取后端默认值）。 */
  baseUrl?: string
}

/** 通知通道（FR-085）。 */
export interface AlertChannelInfo {
  id: number
  uuid: string
  name: string
  type: string
  enabled: boolean
  config: string
  createdAt: string
}

/** 扫码结果回填载荷：appSecret 位置放的是后端返回的**引用名**（secretEnv），不是明文密钥。 */
export interface QQBindFill {
  appId: string
  /** 形如 ${QQ-102000001} 的 ${ENV} 引用名（密钥已由 CP 落盘，前端不接触明文）。 */
  secretEnv: string
  /** 单聊目标 user_openid。 */
  userOpenid: string
}
