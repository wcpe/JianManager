/**
 * 实例工作区页眉视图模型（FR-180，增强 FR-069/166）。
 *
 * 把页眉的「角色 → 图标/徽标语义、状态 → 色调、生命周期 → 主操作集」等判定抽成纯函数，
 * 与 `WorkspaceToolbar` 的 JSX 解耦，便于单测覆盖（沿用控制台「纯逻辑 .ts + .test.ts」约定）。
 * 色调复用 FR-061 `instanceStatusLevel` 与 FR-163 `Tone`，保证页眉与卡片/徽章同色系。
 */
import { Box, Boxes, Route } from 'lucide-react'
import { instanceStatusLevel, type StatusLevel } from '@/lib/threshold'
import type { Tone } from '@/lib/tone'

/** 群组服角色（FR-032）：proxy / backend / 其余按 universal 处理。 */
export type InstanceRoleKind = 'proxy' | 'backend' | 'universal'

/** 角色徽标渲染元信息：identity 图标 + i18n 文案键 + 徽标语义类（与 FR-136 RoleBadge 同色）。 */
export interface RoleMeta {
  kind: InstanceRoleKind
  /** 身份图标（proxy=路由、backend=方块、universal=多方块）。 */
  icon: typeof Route
  /** 角色名 i18n key（复用 networks.role_* 文案，避免重复维护）。 */
  labelKey: string
  /** 角色徽标的描边 + 文字色类；universal 用中性，避免喧宾夺主。 */
  badgeClass: string
}

/** 把后端 role 字段归一为三态枚举（未知值按 universal）。 */
export function roleKind(role: string): InstanceRoleKind {
  if (role === 'proxy') return 'proxy'
  if (role === 'backend') return 'backend'
  return 'universal'
}

/**
 * 角色 → 页眉渲染元信息（FR-180）。
 * proxy 走主色（运营关注度最高的代理），backend 走 info 次色，universal 中性。
 */
export function roleMeta(role: string): RoleMeta {
  switch (roleKind(role)) {
    case 'proxy':
      return {
        kind: 'proxy',
        icon: Route,
        labelKey: 'networks.role_proxy',
        badgeClass: 'border-primary/40 bg-accent text-primary',
      }
    case 'backend':
      return {
        kind: 'backend',
        icon: Box,
        labelKey: 'networks.role_backend',
        badgeClass: 'border-status-info/40 text-status-info',
      }
    default:
      return {
        kind: 'universal',
        icon: Boxes,
        labelKey: 'networks.role_universal',
        badgeClass: 'border-border text-muted-foreground',
      }
  }
}

/** 状态 → 徽章等级（直接复用 FR-061 映射，页眉与列表/卡片同语义）。 */
export function headerStatusLevel(status: string): StatusLevel {
  return instanceStatusLevel(status)
}

/**
 * 状态 → 页眉身份图标块色调（FR-180）。
 * 运行态走主色块（醒目「在线」），其余沿用状态等级（启停=warning、崩溃=danger、停止/未知=neutral）。
 */
export function headerIconTone(status: string): Tone {
  if (status === 'RUNNING') return 'primary'
  return instanceStatusLevel(status)
}

/** 过渡态（启动中/停止中）→ 状态点脉冲，提示「进行中」（与卡片一致）。 */
export function isTransitioning(status: string): boolean {
  return status === 'STARTING' || status === 'STOPPING'
}

/** 实例是否处于运行态（决定页眉主操作集：运行→停止/重启/强制终止，否则→启动）。 */
export function isRunning(status: string): boolean {
  return status === 'RUNNING'
}

/** 页眉次级元信息行的拼装：`类型 · 节点[:端口]`（端口 ≤0 时省略）。供页眉与测试共用。 */
export function metaLine(type: string, nodeName: string, serverPort: number): string {
  const head = [type, nodeName].filter(Boolean).join(' · ')
  return serverPort > 0 ? `${head}:${serverPort}` : head
}
