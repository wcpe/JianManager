/**
 * 运行时与制品契约（FR-082 / FR-301，ADR-097）。
 * 与后端 model 对齐的纯数据类型，供 `@jianmanager/ui` 内视图逻辑与应用侧 api 共用。
 */
import type { AssetInfo, AssetType } from './asset-contracts'

/** 引用某 JDK 的实例（引用关系下钻 / 删除占用方提示，FR-082）。 */
export interface JDKRefInstance {
  id: number
  uuid: string
  name: string
  status: string
  /** direct=按具体 JDK 绑定；major=按 Java 大版本解析到本 JDK。 */
  binding: 'direct' | 'major'
}

/** 跨节点 JDK 矩阵的一项 = 一个节点上的一个 JDK + 其引用实例。 */
export interface JDKMatrixItem {
  id: number
  nodeId: number
  nodeName: string
  nodeOnline: boolean
  vendor: string
  majorVersion: number
  version: string
  arch: string
  path: string
  managed: boolean
  instances: JDKRefInstance[]
  refCount: number
}

/** 制品按类型分组（每组含占用/去重/冷热统计）。 */
export interface AssetTypeGroup {
  type: AssetType
  items: AssetInfo[]
  count: number
  totalSize: number
  referencedCount: number
  hotCount: number
  archivedCount: number
  externalCount: number
  lostCount: number
}

/**
 * 跨节点多运行时矩阵项（FR-301 加性扩展）：type 区分 jdk / nodejs / python（预留）。
 * type=jdk 行 name=厂商且携带引用实例；其它类型当前无引用消费者，instances 恒空。
 */
export interface RuntimeMatrixEntry {
  id: number
  nodeId: number
  nodeName: string
  nodeOnline: boolean
  type: string
  name: string
  majorVersion: number
  version: string
  arch: string
  path: string
  managed: boolean
  instances: JDKRefInstance[]
  refCount: number
}
