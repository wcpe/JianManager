/**
 * 服务器行拖拽的共享契约（侧边栏 ↔ 聊天框）。
 *
 * 为什么必须共享常量而不是各写一份字面量：MIME 一旦两处不一致，
 * 拖拽**不会报错**——`dataTransfer.types.includes()` 只是恒为 false，
 * 表现为「拖了没反应」，且没有任何日志。这类静默失配是最难查的一种。
 *
 * 只带实例 id（不塞整行快照）：拖拽源与落点可能属于不同渲染分支，
 * 传快照会让「用旧数据加芯片」成为可能；只传 id 则由落点按 id 取权威数据。
 */
export const SERVER_DRAG_MIME = 'application/x-jm-instance-id'

/**
 * 判断一次拖拽是否携带服务器。
 *
 * `dataTransfer.getData()` 在 `dragover` 阶段**取不到值**（只有 `drop` 才可读），
 * 因此判断「能不能放」只能看 `types`。这是 HTML5 拖拽的规范行为，
 * 不是实现差异——把 dragOver 写成读 getData 会让落点永远不激活。
 */
export function isServerDrag(types: readonly string[] | undefined): boolean {
  return Array.isArray(types) ? types.includes(SERVER_DRAG_MIME) : false
}

/** 从 drop 事件读出实例 id；读不到或不合法返回 0。 */
export function readServerDragId(dataTransfer: DataTransfer | null): number {
  if (!dataTransfer) return 0
  const raw = dataTransfer.getData(SERVER_DRAG_MIME)
  const id = Number.parseInt(raw, 10)
  return Number.isFinite(id) && id > 0 ? id : 0
}
