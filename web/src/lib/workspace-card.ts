/**
 * 可组合工作区的卡片类型目录（FR-166 / ADR-030 取代版「可组合卡片工作区」）。
 *
 * 卡片 = 实例 × 功能。每种功能对应一个 {@link CardType}，复用既有工作区面板组件
 * （终端 / 资源[文件+配置合一] / 插件 / 监控 / 状态 / 业务·经济·背包[JBIS] / Bot）。
 * 本模块仅声明「有哪些卡、默认尺寸、i18n 键」等纯数据，渲染由 `WorkspaceCanvas` 承载，
 * 便于纯逻辑（预设序列化/校验）单测，不依赖 React。
 */

/** 工作区卡片类型（与原 `WorkspaceSegment` 同源，承载等价能力，不丢任何功能）。 */
export type CardType =
  | 'terminal'
  | 'resource'
  | 'plugins'
  | 'metrics'
  | 'serverstate'
  | 'business'
  | 'economy'
  | 'inventory'
  | 'bot'

/** 单种卡片类型的静态定义。 */
export interface CardTypeDef {
  /** 卡片类型标识。 */
  type: CardType
  /** 卡头标题 i18n 键。 */
  titleKey: string
  /** 卡片说明 i18n 键（实例库 / 添加卡片菜单用）。 */
  descKey: string
  /** 默认网格尺寸（列宽 w / 行高 h，基于 12 列网格）。 */
  defaultSize: { w: number; h: number }
  /** 最小网格尺寸，防止拖到不可用的小尺寸。 */
  minSize: { w: number; h: number }
}

/** 网格总列数（react-grid-layout cols）。 */
export const GRID_COLS = 12

/** 单行网格像素高度（react-grid-layout rowHeight）。 */
export const GRID_ROW_HEIGHT = 40

/**
 * 卡片类型目录。顺序即「添加卡片」菜单与实例库的展示顺序。
 * 终端/资源给大默认尺寸（高频主操作区），其余给中等尺寸。
 */
export const CARD_TYPES: readonly CardTypeDef[] = [
  {
    type: 'terminal',
    titleKey: 'workspace.cardTerminal',
    descKey: 'workspace.cardTerminalDesc',
    defaultSize: { w: 7, h: 11 },
    minSize: { w: 3, h: 5 },
  },
  {
    type: 'resource',
    titleKey: 'workspace.cardResource',
    descKey: 'workspace.cardResourceDesc',
    defaultSize: { w: 7, h: 11 },
    minSize: { w: 3, h: 6 },
  },
  {
    type: 'plugins',
    titleKey: 'workspace.cardPlugins',
    descKey: 'workspace.cardPluginsDesc',
    defaultSize: { w: 6, h: 10 },
    minSize: { w: 3, h: 5 },
  },
  {
    type: 'metrics',
    titleKey: 'workspace.cardMetrics',
    descKey: 'workspace.cardMetricsDesc',
    defaultSize: { w: 6, h: 9 },
    minSize: { w: 3, h: 5 },
  },
  {
    type: 'serverstate',
    titleKey: 'workspace.cardServerState',
    descKey: 'workspace.cardServerStateDesc',
    defaultSize: { w: 5, h: 9 },
    minSize: { w: 3, h: 5 },
  },
  {
    type: 'business',
    titleKey: 'workspace.cardBusiness',
    descKey: 'workspace.cardBusinessDesc',
    defaultSize: { w: 6, h: 9 },
    minSize: { w: 3, h: 5 },
  },
  {
    type: 'economy',
    titleKey: 'workspace.cardEconomy',
    descKey: 'workspace.cardEconomyDesc',
    defaultSize: { w: 6, h: 10 },
    minSize: { w: 3, h: 5 },
  },
  {
    type: 'inventory',
    titleKey: 'workspace.cardInventory',
    descKey: 'workspace.cardInventoryDesc',
    defaultSize: { w: 6, h: 10 },
    minSize: { w: 3, h: 5 },
  },
  {
    type: 'bot',
    titleKey: 'workspace.cardBot',
    descKey: 'workspace.cardBotDesc',
    defaultSize: { w: 6, h: 10 },
    minSize: { w: 3, h: 5 },
  },
] as const

/** 全部合法卡片类型集合（校验用）。 */
export const CARD_TYPE_SET: ReadonlySet<string> = new Set(CARD_TYPES.map((c) => c.type))

/** 按类型取卡片定义；未知类型返回 undefined。 */
export function cardTypeDef(type: string): CardTypeDef | undefined {
  return CARD_TYPES.find((c) => c.type === type)
}

/** 判断字符串是否为合法卡片类型。 */
export function isCardType(v: unknown): v is CardType {
  return typeof v === 'string' && CARD_TYPE_SET.has(v)
}
