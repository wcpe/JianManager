import type { LogEntry } from '@/lib/console/console-log-types'

/**
 * 日志来源标识的解析与显示名反查。
 *
 * # 为何要解析字符串而不是只看 instanceId
 *
 * 事件只带 `log_source_id`（形如 `inst:2/file` / `node:1/file`），
 * 而 `instance_id` / `node_id` 在联邦信封里不一定存在（不同版本、不同采集模式）。
 * `log_source_id` 则是采集侧必然写入的流标签，所以它才是可靠的真源。
 * 只看 instanceId 会让一半的行退化成「实例」两个字。
 */

/** 解析结果：类别 + 数字 id（取不到时为 null）。 */
export interface ParsedLogSource {
  kind: 'instance' | 'worker' | 'control_plane' | 'unknown'
  id: number | null
  /** 流名（file / stdout / stderr），取不到为 undefined。 */
  stream?: string
}

/**
 * 从 `log_source_id` 一类字符串里解析出类别与 id。
 *
 * 兼容的前缀与后端 `federationSourcePrefix` 一致（两侧须同步修改）：
 *   - `inst:` / `instance:` → 实例
 *   - `node:` / `worker:`   → 节点
 *   - `control_plane:` / `cp:` → 平台
 */
export function parseLogSource(raw: string | undefined | null): ParsedLogSource {
  if (!raw) return { kind: 'unknown', id: null }
  const m = /^(inst|instance|node|worker|control_plane|cp):(\d+)(?:\/(.*))?$/.exec(raw)
  if (!m) {
    // 无前缀时回退：裸类别值（旧数据）原样识别，id 未知。
    if (raw === 'instance' || raw === 'worker' || raw === 'control_plane') {
      return { kind: raw, id: null }
    }
    return { kind: 'unknown', id: null }
  }
  const [, prefix, idText, stream] = m
  const kind = prefix === 'inst' || prefix === 'instance'
    ? 'instance'
    : prefix === 'node' || prefix === 'worker'
      ? 'worker'
      : 'control_plane'
  return { kind, id: Number(idText), stream: stream || undefined }
}

/** 名称反查所需的最小数据（只取用得到的字段，便于测试构造）。 */
export interface SourceNameLookup {
  instances?: { id: number; name: string }[]
  nodes?: { id: number; name: string }[]
}

/**
 * 给一条日志算「具体是谁」显示名。
 *
 * 取不到名称时**必须**退回带 id 的坦率写法（`实例 #2`），而不是退回泛称「实例」：
 * 后者会让人以为是「某个未指明实例」，而实际上 id 是已知的，只是列表没拉到。
 * 显示带 id 也便于用户直接在该实例页面搜索定位。
 */
export function resolveSourceLabel(
  log: Pick<LogEntry, 'source' | 'sourceRef' | 'instanceId' | 'nodeId'>,
  lookup: SourceNameLookup,
): { kind: ParsedLogSource['kind']; id: number | null; name: string | null } {
  // 优先解析原始来源串（真源），再退回结构化字段。
  const parsed = parseLogSource(log.sourceRef)
  const kind = parsed.kind !== 'unknown' ? parsed.kind : (log.source as ParsedLogSource['kind'])
  const id = parsed.id
    ?? (kind === 'instance' ? (log.instanceId || null)
      : kind === 'worker' ? (log.nodeId || null)
        : null)

  let name: string | null = null
  if (id !== null) {
    const list = kind === 'instance' ? lookup.instances : kind === 'worker' ? lookup.nodes : undefined
    name = list?.find((entry) => entry.id === id)?.name ?? null
  }
  return { kind, id, name }
}
