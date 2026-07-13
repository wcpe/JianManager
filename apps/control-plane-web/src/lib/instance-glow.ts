/**
 * 实例状态 → 外圈状态光晕 class（FR-315）。
 * RUNNING 绿柔光 / STARTING·STOPPING 琥珀呼吸 / CRASHED 红警示 / 其余（STOPPED 等）无光晕。
 * 取色经 status 语义 token（--color-status-*），双主题自适配；soft=true 为卡片弱化版。
 * 自包含纯函数，便于单测状态→光晕映射。
 */
export function instanceStatusGlowClass(status: string, soft = false): string {
  let kind: string
  switch (status) {
    case 'RUNNING':
      kind = 'running'
      break
    case 'STARTING':
    case 'STOPPING':
      kind = 'transitioning'
      break
    case 'CRASHED':
      kind = 'crashed'
      break
    default:
      return ''
  }
  return soft
    ? `jm-status-glow jm-status-glow-${kind} jm-status-glow--soft`
    : `jm-status-glow jm-status-glow-${kind}`
}
