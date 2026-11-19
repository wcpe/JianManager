/**
 * `log.acq.path_overrides` 的可视化编辑支持。
 *
 * 【这个字段为什么需要专门的转换层】后端的契约是一个 JSON 对象文本：
 *   `{"inst:1":"proxy.log.0","inst:7":"logs/server.log"}`
 * 但它的**真实语义**只是"某个实例的日志在哪个路径"。让用户手写 JSON 有三个问题：
 *  ① 引号/逗号/转义都得手敲，错一个字符整串失效；
 *  ② 键必须是规范的 `inst:<数字>`，但界面从没告诉过用户这个格式；
 *  ③ 删一条要小心别把相邻的逗号留下。
 * 所以界面用「实例 + 路径」的成对行编辑，提交前再序列化回 JSON。
 *
 * 【为什么放在 lib 而不是组件里】解析/序列化/去重/校验都是纯逻辑，且边界情况多
 * （空串、非法 JSON、重复实例、缺实例、空路径）。放这里可以单测，不必靠 UI 测。
 */

/** 一行「实例 → 日志路径」的编辑态。 */
export interface PathOverrideRow {
  /** 稳定行 key（删除中间行时不能让 React 复用错行的输入态）。 */
  key: string
  /** 实例 ID；空串表示这一行用户还没选实例。 */
  instanceId: string
  /** 相对实例工作目录的日志路径。 */
  path: string
}

let rowSeq = 0
/** 生成行 key：只为 React 列表稳定，不参与任何业务判定。 */
export function newRowKey(): string {
  rowSeq += 1
  return `por-${rowSeq}`
}

/** 造一行空编辑态。 */
export function emptyRow(): PathOverrideRow {
  return { key: newRowKey(), instanceId: '', path: '' }
}

/**
 * 解析后端存的 JSON 文本为编辑行。
 *
 * 容错：空串、非法 JSON、非对象一律返回空数组——与后端 `parsePathOverrides`
 * 的口径一致（非法配置不生效）。这里不抛错，因为渲染期不该因为一份坏配置而崩页。
 * 键不像 `inst:<数字>` 的条目也一并丢弃：它不是本字段的合法键，留着会让用户
 * 看到一个"神秘行"，反而更难理解。
 */
export function parsePathOverrides(raw: string | undefined | null): PathOverrideRow[] {
  const text = (raw ?? '').trim()
  if (!text) return []
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch {
    return []
  }
  if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) return []
  const rows: PathOverrideRow[] = []
  for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
    const m = /^inst:(\d+)$/.exec(k)
    if (!m || typeof v !== 'string') continue
    /*
      必须校验「回拼后与原文一致」：正则能匹配 `inst:007`，但后端按 `inst:<数字>`
      精确匹配实例，`007` 这类前导零永远命中不了任何实例——是个永远不会生效的配置。
      settings-form.ts 的校验已有同一条判据，这里必须同口径，
      否则出现「设置页报非法、采集面板却接受并静默失效」。
    */
    if (k !== `inst:${Number(m[1])}`) continue
    rows.push({ key: newRowKey(), instanceId: m[1] ?? '', path: v })
  }
  // 按实例 ID 数值序排列：后端 map 无序，若不排序，每次刷新行序都会跳。
  rows.sort((a, b) => Number(a.instanceId) - Number(b.instanceId))
  return rows
}

/**
 * 把编辑行序列化回后端要的 JSON 文本。
 *
 * 规则与后端 parsePathOverrides 对齐：
 *  - 实例与路径任一为空的行跳过（半填的行不算配置）；
 *  - 同一实例重复时**后者覆盖前者**（Map 语义），不报错——
 *    但 UI 侧会先用 duplicateInstanceIds 提示，让用户在提交前就知道会丢一条。
 *  - 结果为空对象时返回空串（而不是 `{}`）：后端把空串当作"无覆盖"，
 *    与"显式写了个空对象"在语义上不同（前者回落到更大范围的值）。
 */
export function serializePathOverrides(rows: readonly PathOverrideRow[]): string {
  const m = new Map<string, string>()
  for (const r of rows) {
    const id = r.instanceId.trim()
    const p = r.path.trim()
    if (!id || !p) continue
    m.set(id, p)
  }
  if (m.size === 0) return ''
  const out: Record<string, string> = {}
  for (const [id, p] of [...m.entries()].sort((a, b) => Number(a[0]) - Number(b[0]))) {
    out[`inst:${id}`] = p
  }
  return JSON.stringify(out)
}

/** 找出被重复填写的实例 ID（后者会覆盖前者，提交前必须让用户看到）。 */
export function duplicateInstanceIds(rows: readonly PathOverrideRow[]): string[] {
  const seen = new Set<string>()
  const dup = new Set<string>()
  for (const r of rows) {
    const id = r.instanceId.trim()
    if (!id) continue
    if (seen.has(id)) dup.add(id)
    seen.add(id)
  }
  return [...dup]
}

/** 单行校验结果：返回 i18n 键，null 表示合法。 */
export type RowError = string | null

/**
 * 校验一行。
 *
 * 路径规则与 `settings-form.validateSettingDraft('log.acq.path_overrides', …)` 同口径：
 * 必须是**相对实例工作目录**的路径，拒绝绝对路径与 `..` 逃逸——后端也会拒，
 * 但当场提示能省一次往返。两个入口（设置页 / 节点采集面板）共用这条规则，
 * 否则同一个非法值会在一处当场报错、在另一处要提交后才失败。
 */
export function validateRow(r: PathOverrideRow): RowError {
  const id = r.instanceId.trim()
  const p = r.path.trim()
  // 整行空白不报错（用户刚点"添加"还没填）。提交时这类行会被跳过。
  if (!id && !p) return null
  if (!id) return 'logAcqOverride.instanceRequired'
  if (!p) return 'logAcqOverride.pathRequired'
  if (p.startsWith('/') || /^[A-Za-z]:/.test(p)) return 'logAcqOverride.pathMustBeRelative'
  /*
    折叠 `..` 后判断是否真的逃出工作目录——与 settings-form.ts 里
    `validateSettingDraft('log.acq.path_overrides', …)` 的实现**逐句对齐**。

    【为什么不是"见 .. 就拒"】`logs/../a.log` 折叠后是 `a.log`，仍在工作目录内，
    是合法路径。若这里见到 `..` 就拒，而设置页允许，同一个值会在两个入口
    给出不同结论——那正是这次要把口径统一掉的东西。
    只有折叠过程中栈被弹空（`../x`、`a/../../x`）才算逃逸。
  */
  const parts: string[] = []
  for (const seg of p.replace(/\\/g, '/').split('/')) {
    if (seg === '' || seg === '.') continue
    if (seg === '..') {
      if (parts.length === 0) return 'logAcqOverride.pathEscapes'
      parts.pop()
      continue
    }
    parts.push(seg)
  }
  return null
}

/** 整表是否全部合法（用于禁用保存按钮）。 */
export function allRowsValid(rows: readonly PathOverrideRow[]): boolean {
  const dups = duplicateInstanceIds(rows)
  if (dups.length > 0) return false
  return rows.every((r) => validateRow(r) === null)
}
