import type { LucideIcon } from 'lucide-react'
import {
  Activity as ActivityIcon,
  Bot,
  Coins,
  DatabaseBackup,
  FileCog,
  FolderTree,
  Gauge,
  HeartPulse,
  LayoutDashboard,
  Network,
  Puzzle,
  TerminalSquare,
  Users,
} from 'lucide-react'
import type { CardType } from '@/lib/console/workspace-card'
import type { Capability } from './capabilities'

/**
 * 服务器控制台（FR-269）的页签注册表与深链解析。
 * 从应用侧 `InstanceConsolePage` 拆出的纯逻辑：常量表 + 三个纯函数，无 React 与取数依赖。
 */

/** 控制台页签键。 */
export type TabKey = 'overview' | 'terminal' | 'resource' | 'metrics' | 'players' | 'plugins' | 'backup' | 'business' | 'bot' | 'bcTopology' | 'process' | 'health' | 'config'

/** 资源页（页签名「文件配置」）的三个分段。 */
export type ResourceSegment = 'files' | 'config' | 'env'

/** 页签 → 工作台卡片类型（供「在编排台打开」用）。 */
export const TAB_CARD_TYPE: Partial<Record<TabKey, CardType>> = {
  terminal: 'terminal',
  metrics: 'metrics',
  plugins: 'plugins',
  business: 'business',
  bot: 'bot',
}

// Tab 全量登记表（FR-445/448）：这是「有哪些 Tab、顺序如何、图标标签是什么」的唯一登记处；
// **实际渲染的可见集合由能力画像 `capabilities` 过滤**（见 useInstanceCapabilities），不再硬编码角色分支。
// Tab 重组（FR-413）：原「环境变量」并入「文件配置」的一个分段；分隔线按职能分组（运行/配置/观测/运营）。
export const TAB_KEYS: TabKey[] = ['overview', 'terminal', 'resource', 'plugins', 'metrics', 'players', 'business', 'bot', 'backup', 'bcTopology', 'process', 'health', 'config']

/** 在该 key 之前插入分组分隔线。 */
export const TAB_GROUP_BREAK: ReadonlySet<TabKey> = new Set<TabKey>(['resource', 'metrics', 'business', 'bcTopology', 'process'])

export const TAB_LABEL_KEY: Record<TabKey, string> = {
  overview: 'serverConsole.overview',
  terminal: 'serverConsole.console',
  resource: 'serverConsole.filesConfig',
  metrics: 'serverConsole.metrics',
  players: 'serverConsole.players',
  plugins: 'serverConsole.plugins',
  backup: 'serverConsole.backupSchedule',
  business: 'serverConsole.business',
  bot: 'serverConsole.bot',
  bcTopology: 'serverConsole.bcTopology',
  process: 'serverConsole.process',
  health: 'serverConsole.health',
  config: 'serverConsole.config',
}

export const TAB_ICON: Record<TabKey, LucideIcon> = {
  overview: LayoutDashboard,
  terminal: TerminalSquare,
  resource: FolderTree,
  metrics: ActivityIcon,
  players: Users,
  plugins: Puzzle,
  backup: DatabaseBackup,
  business: Coins,
  bot: Bot,
  bcTopology: Network,
  process: Gauge,
  health: HeartPulse,
  config: FileCog,
}

/**
 * 能力 → Tab 映射（FR-445）：画像 `capabilities` 中的每项**若对应一个 Tab**则落成 Tab。
 * 注意 `files` 能力对应 `resource` Tab（页签名「文件配置」），二者命名不同是历史包袱。
 * 动作级能力（如 `clone`：可克隆，用于行菜单显隐）刻意**不登记**，故为 Partial，
 * 会被 `visibleTabsFor` 过滤掉，不产生幽灵页签。
 */
export const CAPABILITY_TAB: Partial<Record<Capability, TabKey>> = {
  overview: 'overview',
  terminal: 'terminal',
  files: 'resource',
  plugins: 'plugins',
  metrics: 'metrics',
  players: 'players',
  business: 'business',
  bot: 'bot',
  backup: 'backup',
  bcTopology: 'bcTopology',
  process: 'process',
  health: 'health',
  config: 'config',
}

/** 按画像能力集合推导有序可见 Tab（画像为空回退全量，保证非白屏）。 */
export function visibleTabsFor(capabilities: Capability[]): TabKey[] {
  const tabs = capabilities.map((c) => CAPABILITY_TAB[c]).filter((k): k is TabKey => !!k)
  return tabs.length > 0 ? tabs : TAB_KEYS
}

/** 解析深链 `?tab=` 为活跃页签（未知值回退 overview）。 */
export function readActiveTab(searchParams: URLSearchParams): TabKey {
  const tab = searchParams.get('tab')
  // 旧深链兼容（FR-413）：`?tab=env` 曾是独立页签，现落到「文件配置」的环境变量分段。
  if (tab === 'env') return 'resource'
  return TAB_KEYS.includes(tab as TabKey) ? (tab as TabKey) : 'overview'
}

/** 解析资源页分段（含旧深链兼容）。 */
export function readResourceSegment(searchParams: URLSearchParams): ResourceSegment {
  // 旧深链兼容（FR-413）：`?tab=env` 落到「环境变量」分段；`?seg=config` 落到「关键配置」分段（FR-451）。
  if (searchParams.get('tab') === 'env') return 'env'
  const seg = searchParams.get('seg')
  return seg === 'env' || seg === 'config' ? seg : 'files'
}
