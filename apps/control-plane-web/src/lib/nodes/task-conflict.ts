/**
 * 「同类重型任务已在跑」（HTTP 409 TASK_EXCLUSIVE_BUSY）的统一呈现。
 *
 * 后端对 JDK 安装 / Node.js 安装 / 全局包安装加了**同节点同互斥组单在途**闸门：
 * 第二次提交会被 409 拒绝，并回带占用中的任务（existingTask）。
 *
 * 【为什么前端要专门识别这一个错误码】它和"请求失败"不是一回事：
 *  - 失败 → 用户该重试；
 *  - 被占用 → 重试没用，得**先处置正在跑的那个**（等它完成，或去任务中心强制停止）。
 * 混成一句 toast.error(message) 会让用户反复点，正是本次要消除的体验。
 *
 * 所以这里把它翻译成「一句结论 + 一个可执行动作」：直接给出去任务中心的链接。
 */

/** 任务占用冲突的结构化描述。 */
export interface TaskConflict {
  /** 人读的一句话（优先取后端 message，它已包含占用者标题与进度）。 */
  message: string
  /** 占用中的任务 id（可空：后端未回带时）。 */
  taskId?: string
  /** 占用中任务的标题（可空）。 */
  taskTitle?: string
  /** 占用中任务的进度（0~100，可空）。 */
  progress?: number
}

/** 后端 409 响应体的形状（只取本文件用到的字段）。 */
interface BusyResponseBody {
  error?: string
  message?: string
  existingTask?: {
    taskId?: string
    title?: string
    progress?: number
    state?: string
  }
}

/**
 * 从一次 mutation 错误里解析「任务占用」；不是此类错误则返回 null。
 *
 * 判定依据是后端的 `error` 码而非 HTTP 状态：409 在别处也可能出现，
 * 只有 `TASK_EXCLUSIVE_BUSY` 才是「去任务中心处置」这个语义。
 */
export function parseTaskConflict(err: unknown): TaskConflict | null {
  const resp = (err as { response?: { status?: number; data?: BusyResponseBody } })?.response
  const body = resp?.data
  if (!body || body.error !== 'TASK_EXCLUSIVE_BUSY') return null
  return {
    message: body.message || '该节点已有同类任务在跑，请等它完成或先停止它',
    taskId: body.existingTask?.taskId,
    taskTitle: body.existingTask?.title,
    progress: body.existingTask?.progress,
  }
}

/** 任务中心路径（带 taskId 时直接定位到那一条）。 */
export function tasksHref(conflict: TaskConflict): string {
  return conflict.taskId ? `/tasks?task=${encodeURIComponent(conflict.taskId)}` : '/tasks'
}

/**
 * 面向用户的完整提示文案（toast 正文）。
 *
 * 有占用者信息时把「谁在占着 + 进度」写进去——比"任务冲突"有用得多；
 * 没有时退回后端 message（它至少说明了原因）。
 */
export function conflictText(conflict: TaskConflict): string {
  if (conflict.taskTitle) {
    const pct = typeof conflict.progress === 'number' ? `，进度 ${conflict.progress}%` : ''
    return `该节点正在「${conflict.taskTitle}」${pct}；请等它完成，或到任务中心强制停止后再试`
  }
  return conflict.message
}
