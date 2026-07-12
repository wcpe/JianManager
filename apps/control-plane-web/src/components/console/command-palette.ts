/**
 * 命令面板纯检索逻辑（FR-241）。组件层负责数据源与执行动作，本模块只做匹配/排序/截断，便于单测。
 */

/** 一条可检索目标。key 编码目标类型与标识，组件据此执行跳转/动作（如 `instance:2`/`page:/nodes`）。 */
export interface PaletteEntry {
  kind: 'instance' | 'node' | 'page' | 'command'
  /** 稳定唯一键，形如 `<kind>:<id|path>`；组件解析以执行动作。 */
  key: string
  /** 主文案（实例/节点名、页面名、操作名）。 */
  label: string
  /** 次要文案（host/UUID/状态等）。 */
  sublabel?: string
  /** 实例状态（仅 kind=instance），供组件着色状态点。 */
  status?: string
}

export interface PaletteSources {
  instances: { id: number; name: string; uuid: string; status: string; nodeId?: number }[]
  nodes: { id: number; name: string; host: string }[]
  pages: { to: string; label: string }[]
  commands: { id: string; label: string }[]
  /** 页眉节点作用域：只收敛实例结果；节点本身仍可全局搜索以便切换作用域。 */
  nodeScopeId?: number | null
}

/** 子串命中（大小写不敏感）。空查询恒真（用于默认列表）。 */
function hit(haystack: string, q: string): boolean {
  if (!q) return true
  return haystack.toLowerCase().includes(q)
}

/**
 * 按查询在四类目标内检索，返回扁平有序列表（实例 → 节点 → 页面 → 操作），每类截断到 limitPer。
 * 扁平顺序即键盘上下移动顺序。空查询返回默认（前若干实例 + 全部页面 + 操作），便于「打开即见常用」。
 */
export function searchPalette(rawQuery: string, src: PaletteSources, limitPer = 8): PaletteEntry[] {
  const q = rawQuery.trim().toLowerCase()
  const out: PaletteEntry[] = []

  for (const i of src.instances) {
    if (src.nodeScopeId != null && i.nodeId !== src.nodeScopeId) continue
    if (hit(i.name, q) || hit(i.uuid, q)) {
      out.push({ kind: 'instance', key: `instance:${i.id}`, label: i.name, sublabel: i.uuid.slice(0, 8), status: i.status })
      if (countKind(out, 'instance') >= limitPer) break
    }
  }
  for (const n of src.nodes) {
    if (hit(n.name, q) || hit(n.host, q)) {
      out.push({ kind: 'node', key: `node:${n.id}`, label: n.name, sublabel: n.host })
      if (countKind(out, 'node') >= limitPer) break
    }
  }
  for (const p of src.pages) {
    if (hit(p.label, q)) {
      out.push({ kind: 'page', key: `page:${p.to}`, label: p.label })
      if (countKind(out, 'page') >= limitPer) break
    }
  }
  for (const c of src.commands) {
    if (hit(c.label, q)) {
      out.push({ kind: 'command', key: `command:${c.id}`, label: c.label })
    }
  }
  return out
}

function countKind(list: PaletteEntry[], kind: PaletteEntry['kind']): number {
  let n = 0
  for (const e of list) if (e.kind === kind) n++
  return n
}
