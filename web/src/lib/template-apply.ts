/**
 * 模板应用纯逻辑（FR-154）：把「模板」升级为应用市场所需的派生/校验逻辑下沉为纯函数，
 * 便于单测且与 React/i18n 解耦。两类能力：
 * 1. 变量占位（`{{var}}`）的提取 / 填充 / 校验——支撑「含占位变量时的填充预览」。
 * 2. 市场展示元数据派生（类型 label/图标/语义色、Java·RAM 需求推断）——支撑封面卡片。
 */
import type { Tone } from '@/lib/tone'

/** 占位变量语法：`{{ name }}`，变量名限 [A-Za-z0-9_]。全局匹配。 */
const VAR_RE = /\{\{\s*([A-Za-z0-9_]+)\s*\}\}/g

/**
 * 从 startCommand 中提取占位变量名（去重、保持首次出现顺序）。
 * 仅识别 `{{var}}` 且变量名为 [A-Za-z0-9_]；非法占位（含连字符等）被忽略。
 */
export function extractVariables(startCommand: string): string[] {
  const out: string[] = []
  const seen = new Set<string>()
  for (const m of startCommand.matchAll(VAR_RE)) {
    const name = m[1]
    if (!seen.has(name)) {
      seen.add(name)
      out.push(name)
    }
  }
  return out
}

/**
 * 用 values 替换 startCommand 中的占位变量，返回填充后的命令。
 * - 未在 values 中提供的变量保持原占位（预览未填态可见缺口）。
 * - 提供空串视为「已填为空」，占位被移除。
 */
export function fillTemplate(startCommand: string, values: Record<string, string>): string {
  return startCommand.replace(VAR_RE, (whole, name: string) =>
    Object.prototype.hasOwnProperty.call(values, name) ? values[name] : whole,
  )
}

/** 校验错误集：变量名 → i18n messageKey（仿 cron.ts 的 messageKey 约定，保持纯逻辑）。 */
export type VariableErrors = Record<string, string>

/**
 * 校验占位变量是否均已填（非空白）。缺失项返回必填 messageKey。
 * 不做格式校验——值含义由模板作者决定，仅保证「全部填了」。
 */
export function validateVariableValues(vars: string[], values: Record<string, string>): VariableErrors {
  const errs: VariableErrors = {}
  for (const v of vars) {
    if (!(values[v] ?? '').trim()) {
      errs[v] = 'templates.market.variableRequired'
    }
  }
  return errs
}

/**
 * 从 startCommand 解析最大堆（-Xmx）大小，返回 MiB；无 -Xmx 返回 null。
 * 支持 G/M/K（大小写不敏感）单位；不足 1 MB 向上取整为 1。
 */
export function parseMaxHeapMb(startCommand: string): number | null {
  const m = startCommand.match(/-Xmx(\d+)([gmk])/i)
  if (!m) return null
  const n = Number(m[1])
  switch (m[2].toLowerCase()) {
    case 'g':
      return n * 1024
    case 'm':
      return n
    case 'k':
      return Math.max(1, Math.ceil(n / 1024))
    default:
      return null
  }
}

/** 规范化类型 key：小写、连字符/空格转下划线，便于识别（minecraft_java / Minecraft-Java 同义）。 */
function normType(type: string): string {
  return type.trim().toLowerCase().replace(/[-\s]+/g, '_')
}

/** 内置核心类型的展示与运行时需求画像。未命中走 generic 回退。 */
interface TypeProfile {
  typeLabel: string
  icon: MarketIcon
  tone: Tone
  java: string | null
  /** 无 -Xmx 时的 RAM 兜底建议（含「+」），null 表示不给建议。 */
  defaultRam: string | null
}

/** 市场卡片图标语义名（页面侧映射到 lucide 图标，纯逻辑不依赖组件）。 */
export type MarketIcon = 'cube' | 'route' | 'flame' | 'grid' | 'package'

const TYPE_PROFILES: Record<string, TypeProfile> = {
  minecraft_java: { typeLabel: 'Minecraft Java', icon: 'cube', tone: 'primary', java: 'Java 21', defaultRam: '2G+' },
  paper: { typeLabel: 'Paper', icon: 'cube', tone: 'primary', java: 'Java 21', defaultRam: '4G+' },
  purpur: { typeLabel: 'Purpur', icon: 'cube', tone: 'primary', java: 'Java 21', defaultRam: '4G+' },
  spigot: { typeLabel: 'Spigot', icon: 'cube', tone: 'primary', java: 'Java 17', defaultRam: '2G+' },
  velocity: { typeLabel: 'Velocity', icon: 'route', tone: 'info', java: 'Java 17', defaultRam: '1G+' },
  bungeecord: { typeLabel: 'BungeeCord', icon: 'route', tone: 'info', java: 'Java 17', defaultRam: '1G+' },
  forge: { typeLabel: 'Forge', icon: 'flame', tone: 'warning', java: 'Java 17', defaultRam: '6G+' },
  neoforge: { typeLabel: 'NeoForge', icon: 'flame', tone: 'warning', java: 'Java 21', defaultRam: '6G+' },
  fabric: { typeLabel: 'Fabric', icon: 'grid', tone: 'info', java: 'Java 21', defaultRam: '4G+' },
}

/** 市场卡片所需的派生展示元数据。 */
export interface MarketMeta {
  /** 类型展示名（命中内置则美化，否则用原始 type）。 */
  typeLabel: string
  /** 封面图标语义名。 */
  icon: MarketIcon
  /** 图标块/封面语义色调。 */
  tone: Tone
  /** Java 运行时建议（如 `Java 21`），无法判断为 null。 */
  java: string | null
  /** RAM 需求（优先从 -Xmx 推断，否则按类型兜底），无法判断为 null。 */
  ram: string | null
}

/** 模板中与市场展示相关的输入子集（解耦 API 类型，便于测试）。 */
export interface MarketTemplateInput {
  name: string
  type: string
  startCommand: string
  description?: string
}

/**
 * 把 -Xmx 推断的堆大小或类型兜底建议格式化为 RAM 需求字符串（如 `4G+`）。
 * 优先级：startCommand 的 -Xmx（向上取整到 GB）> 类型默认建议 > null。
 */
export function formatRamRequirement(input: Pick<MarketTemplateInput, 'startCommand' | 'type'>): string | null {
  const heapMb = parseMaxHeapMb(input.startCommand)
  if (heapMb != null) {
    const gb = Math.max(1, Math.ceil(heapMb / 1024))
    return `${gb}G+`
  }
  return TYPE_PROFILES[normType(input.type)]?.defaultRam ?? null
}

/**
 * 由模板派生应用市场展示元数据：类型 label/图标/语义色 + Java/RAM 需求推断。
 * 命中内置核心类型走画像，否则回退 generic（包装图标 + 原始 type 文案 + 无运行时建议）。
 */
export function deriveMarketMeta(input: MarketTemplateInput): MarketMeta {
  const profile = TYPE_PROFILES[normType(input.type)]
  if (!profile) {
    return {
      typeLabel: input.type,
      icon: 'package',
      tone: 'primary',
      java: null,
      ram: formatRamRequirement(input),
    }
  }
  return {
    typeLabel: profile.typeLabel,
    icon: profile.icon,
    tone: profile.tone,
    java: profile.java,
    ram: formatRamRequirement(input),
  }
}
