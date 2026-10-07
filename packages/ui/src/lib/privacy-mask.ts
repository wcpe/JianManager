/**
 * 管理端隐私脱敏（FR-360）。
 * 供安全中心/监控日志/后续 CSV 导出（FR-361）复用。
 */

/** playerName：超过 max 时尾部截断并加省略号（入库上限 32，展示默认 16）。 */
export function maskPlayerName(name: string | null | undefined, max = 16): string {
  if (!name) return ''
  if (name.length <= max) return name
  return `${name.slice(0, Math.max(1, max - 1))}…`
}

/**
 * machineId / installId：前 keepHead + … + 后 keepTail；
 * 过短（≤ keepHead+keepTail）则全掩为 ***。
 */
export function maskMachineId(id: string | null | undefined, keepHead = 6, keepTail = 4): string {
  if (!id) return ''
  if (id.length <= keepHead + keepTail) return '***'
  return `${id.slice(0, keepHead)}…${id.slice(-keepTail)}`
}

/** installId 与 machineId 同规则。 */
export function maskInstallId(id: string | null | undefined): string {
  return maskMachineId(id)
}
