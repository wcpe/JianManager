/**
 * 聊天草稿的按会话保存（FR-519 §5.1「草稿按会话保存，切会话恢复」）。
 *
 * 为什么用 sessionStorage 而不是 localStorage：
 *  - 草稿可能含未发出的敏感内容（实例名、粘贴的日志片段）。localStorage 会留到用户
 *    手动清理为止；sessionStorage 随标签页关闭即消失，泄漏窗口小得多。
 *  - 「跨标签继续写」不是需求；同一标签内切会话、切路由后再回来才是。
 *
 * key 里带 conversationId：新会话用 `new`，这样「新会话的草稿」不会被某个旧会话抢走。
 * 上限 8KiB：草稿是便捷功能，不该变成能写爆 storage 的入口（写失败会抛 QuotaExceeded，
 * 而那时连正常输入都会被中断——比丢草稿更糟）。
 */

const DRAFT_PREFIX = 'jm-assistant-draft:'
const MAX_DRAFT_CHARS = 8192

/**
 * 当前草稿归属的用户 id（0 表示未登录/未知）。
 *
 * ## 为什么草稿 key 必须带 userId（FR-519「换用户不串号」）
 *
 * 此前 key 只有 `conversationId`（新会话是字面量 `new`）。于是：
 * 用户 A 在「新会话」里打了半句没发 → 登出 → 用户 B 登录 → B 打开新会话，
 * key 同样是 `jm-assistant-draft:new`，**B 的输入框会直接回填 A 的草稿**。
 * 具体会话 id 因为全局自增通常不重叠，但 `new` 这一条**必然跨用户碰撞**。
 *
 * sessionStorage 不随登出清空，所以只靠 `logout()` 清理也不够——必须让 key 本身
 * 带上归属，做到「即使旧内容还在，也不会被另一个用户读到」。
 */
let draftScopeUserId = 0

/** 设置当前草稿归属用户（登录/换用户时由 auth 流程调用）。 */
export function setDraftScopeUserId(userId: number): void {
  draftScopeUserId = userId > 0 ? userId : 0
}

function keyOf(conversationId: number | null): string {
  const conv = conversationId === null ? 'new' : String(conversationId)
  // 未登录时用 `anon` 而不是省略：省略会让「未登录写的草稿」与「某用户的草稿」
  // 落到同一个键上，等于又开了一条串号路径。
  const owner = draftScopeUserId > 0 ? String(draftScopeUserId) : 'anon'
  return `${DRAFT_PREFIX}${owner}:${conv}`
}

/** 读草稿（无草稿或不可用返回空串）。 */
export function loadDraft(conversationId: number | null): string {
  try {
    return sessionStorage.getItem(keyOf(conversationId)) ?? ''
  } catch {
    // 隐私模式/被禁用：静默退化为「不保存」，不影响聊天本身。
    return ''
  }
}

/** 写草稿（空内容即删除对应键，避免留下无意义的空串）。 */
export function saveDraft(conversationId: number | null, text: string): void {
  try {
    const key = keyOf(conversationId)
    if (text === '') {
      sessionStorage.removeItem(key)
      return
    }
    sessionStorage.setItem(key, text.slice(0, MAX_DRAFT_CHARS))
  } catch {
    // 配额或权限问题：忽略。丢草稿是可接受的降级，打断输入不可接受。
  }
}

/** 清草稿（发送成功后调用，避免下轮又把旧内容填回来）。 */
export function clearDraft(conversationId: number | null): void {
  saveDraft(conversationId, '')
}

// ── 进程内共享（同一会话可能有多个视图同时存在） ──────────────────────

/**
 * sessionStorage 只解决「关掉再回来」。它不解决**同一标签页内两个视图并存**：
 * 首页的对话面板与顶栏浮窗各自调用一次 `useAssistantChat`，如果草稿只放在各自
 * 的 `useState` 里，用户在首页写了半句再打开浮窗，浮窗是空的——同一会话两份草稿。
 *
 * 因此这里加一层模块级 store：写入即广播，所有视图看到同一份。key 仍是 conversationId，
 * 所以切会话时各自取到的还是属于自己的草稿。
 */
const draftCache = new Map<number | null, string>()
const draftListeners = new Set<() => void>()

function notifyDraft() {
  for (const l of draftListeners) l()
}

/** 订阅草稿变化（`useSyncExternalStore` 用）。 */
export function subscribeDraft(listener: () => void): () => void {
  draftListeners.add(listener)
  return () => draftListeners.delete(listener)
}

/** 读取某会话的草稿（首次访问回填自 sessionStorage）。 */
export function getDraft(conversationId: number | null): string {
  const cached = draftCache.get(conversationId)
  if (cached !== undefined) return cached
  const loaded = loadDraft(conversationId)
  draftCache.set(conversationId, loaded)
  return loaded
}

/** 写入草稿：更新缓存 + 持久化 + 广播。 */
export function setDraftShared(conversationId: number | null, text: string): void {
  if (draftCache.get(conversationId) === text) return
  draftCache.set(conversationId, text)
  saveDraft(conversationId, text)
  notifyDraft()
}

/** 清草稿（发送成功后）：缓存与持久化一起清。 */
export function clearDraftShared(conversationId: number | null): void {
  if (draftCache.get(conversationId) === '') {
    // 缓存已是空串，但持久化可能还有残留：仍然清一次。
    clearDraft(conversationId)
    return
  }
  draftCache.set(conversationId, '')
  clearDraft(conversationId)
  notifyDraft()
}

/**
 * 切换/清空用户作用域（FR-519「换用户不串号」）。
 *
 * 做两件事，缺一不可：
 *  1. 把**模块级缓存与订阅清空**——缓存按 conversationId 索引，登出后如果不清，
 *     新用户的同名 key（尤其 `new`）会读到上一个用户的内存草稿；
 *  2. 把**上一个用户的持久化草稿删掉**——sessionStorage 不随登出消失，
 *     留给下一个人读到就是泄漏。key 里已带 owner，理论上读不到；
 *     但留着等于把别人的未发送内容长期存在浏览器里，删掉更干净。
 *
 * 顺序：先删旧作用域的存储，再切换作用域并清缓存。
 */
export function resetDraftScope(nextUserId: number): void {
  const prevOwner = draftScopeUserId > 0 ? String(draftScopeUserId) : 'anon'
  const prefix = `${DRAFT_PREFIX}${prevOwner}:`
  try {
    // 先收集再删：边遍历边删 sessionStorage 会漏项。
    const doomed: string[] = []
    for (let i = 0; i < sessionStorage.length; i++) {
      const k = sessionStorage.key(i)
      if (k && k.startsWith(prefix)) doomed.push(k)
    }
    for (const k of doomed) sessionStorage.removeItem(k)
  } catch {
    // 隐私模式/被禁用：没有持久化可清，忽略。
  }
  draftCache.clear()
  setDraftScopeUserId(nextUserId)
  // 通知所有视图重读：否则它们仍显示上一个用户的草稿内容。
  notifyDraft()
}
