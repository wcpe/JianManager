/**
 * 实例未保存草稿注册表（FR-296，ADR-067）：模块级单例，供跨服热缓存宿主
 * 做「淘汰偏好」决策——优先淘汰无未保存草稿的实例，被迫淘汰带草稿者时 toast 警示。
 *
 * 上报方（如资源管理器）在脏态变化时登记；**故意不用 effect 清理清签**——
 * `<Activity>` 隐藏会卸载 effects 但草稿 DOM 状态仍在，靠清理函数清签会误报「无草稿」。
 * 真正卸载（LRU 淘汰 / 离开控制台）由缓存宿主统一 {@link clearInstanceDrafts}。
 */

const draftsByInstance = new Map<number, Set<string>>()

/**
 * 登记/撤销某实例下某个编辑面（key，如 'resource-file' / 'resource-config'）的脏态。
 * 同一实例可有多个编辑面，任一脏即视为「有草稿」。
 */
export function reportInstanceDraft(instanceId: number, key: string, dirty: boolean): void {
  if (dirty) {
    const keys = draftsByInstance.get(instanceId) ?? new Set<string>()
    keys.add(key)
    draftsByInstance.set(instanceId, keys)
    return
  }
  const keys = draftsByInstance.get(instanceId)
  if (!keys) return
  keys.delete(key)
  if (keys.size === 0) draftsByInstance.delete(instanceId)
}

/** 该实例是否存在未保存草稿（任一编辑面脏即真）。 */
export function hasInstanceDraft(instanceId: number): boolean {
  return (draftsByInstance.get(instanceId)?.size ?? 0) > 0
}

/** 实例控制台真卸载（LRU 淘汰 / 离开控制台）时清空其草稿登记。 */
export function clearInstanceDrafts(instanceId: number): void {
  draftsByInstance.delete(instanceId)
}
