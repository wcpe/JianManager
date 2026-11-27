/**
 * 设置表单的**草稿模型**。
 *
 * 【为什么需要一层独立模型】改造前每个设置面板各自维护草稿：
 * 采集面板是 `Record<string,string>` + 与原始值比较，设置页是另一套。
 * 一旦引入"结构化字段"（如成对行编辑的 `path_overrides`），
 * 「值是字符串」这个假设就破了：半填的行序列化后是空串，而空串又被当成"无覆盖"，
 * 用户刚添加的行会闪一下消失。
 *
 * 这里的做法是把两种草稿分开：
 *  - **字符串字段**：直接存字符串，脏判定是「与原始值不同」；
 *  - **结构化字段**（kv-pairs 等）：存结构（行数组），脏判定由字段自己给，
 *    提交时再序列化成后端要的字符串。
 *
 * 两者都在同一份 draft 里，但**不互相反推**——这正是之前反复失败的那一点。
 */

import { usesCustomEditor } from './field-types'
import { serializePathOverrides, type PathOverrideRow } from './path-overrides'

/** 一条设置项的服务端快照（与各页面 API 的形状对齐后由调用方映射）。 */
export interface SettingItem {
  key: string
  /** 平台/全局默认值（用于展示"继承值"）。 */
  platform: string
  /** 本作用域的覆盖值（空串=未覆盖）。 */
  override: string
  overridden: boolean
}

/** 结构化的键值对行（目前只有 kv-pairs 用）。 */
export interface KvPairRow {
  /** 稳定行 key（删除非末行时防止 React 复用错行输入态）。 */
  rowKey?: string
  key: string
  value: string
}

/** 一份草稿：字符串字段与结构化字段分开存。 */
export interface SettingsDraft {
  /** 普通字符串字段的草稿值（键 → 值）。 */
  text: Record<string, string>
  /** 结构化字段的草稿（键 → 行数组）。 */
  kv: Record<string, KvPairRow[]>
}

/** 空草稿。 */
export function emptyDraft(): SettingsDraft {
  return { text: {}, kv: {} }
}

/** 某字段当前的**展示值**：草稿优先，否则回落到覆盖值/平台值。 */
export function shownText(item: SettingItem, draft: SettingsDraft): string {
  const fromDraft = draft.text[item.key]
  if (fromDraft !== undefined) return fromDraft
  return item.overridden ? item.override : item.platform
}

/**
 * 组装**要提交的键值**（只含真正改动的）。
 *
 * 【为什么结构化字段也要进这里】后端契约是字符串（JSON 文本），
 * 所以序列化必须发生在这里——但**编辑态不进这里**，只提交"完整"的行，
 * 半填行被序列化函数跳过。这样"用户正在编辑"与"能提交什么"就分开了。
 */
export function buildSubmitValues(
  items: readonly SettingItem[],
  draft: SettingsDraft,
  serializeKv: (rows: readonly KvPairRow[]) => string,
): Record<string, string> {
  const out: Record<string, string> = {}

  for (const it of items) {
    const original = it.overridden ? it.override : it.platform

    if (usesCustomEditor(it.key)) {
      const rows = draft.kv[it.key]
      if (rows === undefined) continue // 没动过这个字段
      const next = serializeKv(rows)
      if (next !== original) out[it.key] = next
      continue
    }

    const next = draft.text[it.key]
    if (next !== undefined && next !== original) out[it.key] = next
  }

  return out
}

/** 当前是否存在未保存改动。 */
export function hasChanges(
  items: readonly SettingItem[],
  draft: SettingsDraft,
  serializeKv: (rows: readonly KvPairRow[]) => string,
): boolean {
  return Object.keys(buildSubmitValues(items, draft, serializeKv)).length > 0
}

/** 判断结构化草稿是否不含正在编辑中的空行/半填行或重复键。 */
function hasOnlyCompleteUniqueRows(rows: readonly KvPairRow[]): boolean {
  const seen = new Set<string>()
  for (const r of rows) {
    const k = r.key.trim()
    const v = r.value.trim()
    if (!k || !v) return false
    if (seen.has(k)) return false
    seen.add(k)
  }
  return true
}

/**
 * 保存成功后清理草稿：**只清除仍是本次提交值的键**。
 *
 * 【为什么不能整表清空】PUT 在途期间输入控件并不禁用，用户在别处的新输入会被一起抹掉，
 * 界面当场回退成服务端旧值且没有任何提示——用户以为改动已生效，实际从未提交。
 */
export function clearSubmitted(
  draft: SettingsDraft,
  submitted: Record<string, string>,
  items: readonly SettingItem[],
  serializeKv: (rows: readonly KvPairRow[]) => string,
): SettingsDraft {
  const next: SettingsDraft = { text: { ...draft.text }, kv: { ...draft.kv } }
  for (const [k, v] of Object.entries(submitted)) {
    const it = items.find((x) => x.key === k)
    if (!it) continue
    if (usesCustomEditor(k)) {
      // 结构化字段：不仅序列化结果要等于提交值，还必须确认没有正在输入中的空行/半填行/重复行，
      // 否则 PUT 返回时会把用户刚新增但尚未填完的新行直接抹掉。
      const rows = next.kv[k]
      if (rows !== undefined && hasOnlyCompleteUniqueRows(rows) && serializeKv(rows) === v) {
        delete next.kv[k]
      }
    } else if (next.text[k] === v) {
      delete next.text[k]
    }
  }
  return next
}

/** 把一个字段的草稿行映射成 path_overrides 编辑器的行形态（优先保留稳定 rowKey）。 */
export function toOverrideRows(rows: readonly KvPairRow[]): PathOverrideRow[] {
  return rows.map((r, i) => ({ key: r.rowKey ?? `kvrow-${i}`, instanceId: r.key, path: r.value }))
}

/** 反向映射：编辑器的行变回草稿行。**保留原串与稳定 rowKey**（不 trim），否则用户打不出临时的空格。 */
export function fromOverrideRows(rows: readonly PathOverrideRow[]): KvPairRow[] {
  return rows.map((r) => ({ rowKey: r.key, key: r.instanceId, value: r.path }))
}

/**
 * kv-pairs 字段的默认序列化器（提交给后端的字符串）。
 *
 * 【为什么放在 lib 而不是视图里】提交（`buildSubmitValues`）、脏判定（`hasChanges`）、
 * 清草稿（`clearSubmitted`）与渲染**必须用同一个序列化器**——否则"有改动"与
 * "提交什么"会各说各话。放进 lib 复用，也避开「组件文件不导出非组件值」的 lint 约束。
 *
 * 【已知前提】`SettingsDraft.kv` 目前只服务 `log.acq.path_overrides` 一个字段，
 * 签名 `(rows) => string` 不含字段键。将来若出现第二个 kv-pairs 字段，
 * 必须把它改成 `(key, rows) => string` 并按字段分派，否则会拿实例路径的规则
 * 去序列化别的结构化字段。
 */
export function defaultSerializeKv(rows: readonly KvPairRow[]): string {
  return serializePathOverrides(toOverrideRows(rows))
}
