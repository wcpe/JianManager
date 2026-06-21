/**
 * 轻量 JWT 解码工具——仅用于前端读取自身 access token 的非敏感声明（role/username），
 * 用于危险操作的角色门禁 UI 展示（FR-059）。
 *
 * 注意：这里只做 base64url payload 解码，不验证签名。真正的鉴权与越权拒绝由
 * Control Plane 的 RBAC 中间件强制执行（architecture-invariants：前端仅复用既有 RBAC）。
 */

/** access token 中与前端相关的声明子集。 */
export interface JwtClaims {
  /** 用户 ID。 */
  userId?: number
  /** 用户名。 */
  username?: string
  /** 角色等级：0=组成员 1=组管理员 10=平台管理员（与后端 model.UserRole 对齐）。 */
  role?: number
  /** 过期时间（Unix 秒，标准 JWT 声明）。用于前端在请求前判定 access token 是否已过期。 */
  exp?: number
}

/** base64url 解码为 UTF-8 字符串，兼容浏览器 atob。 */
function base64UrlDecode(segment: string): string {
  const normalized = segment.replace(/-/g, '+').replace(/_/g, '/')
  const padded = normalized.padEnd(normalized.length + ((4 - (normalized.length % 4)) % 4), '=')
  const binary = atob(padded)
  // 还原多字节 UTF-8（用户名可能含中文）。
  const bytes = Uint8Array.from(binary, (c) => c.charCodeAt(0))
  return new TextDecoder().decode(bytes)
}

/** 解码 JWT payload，失败返回 null（token 缺失/格式错误时调用方按未授权处理）。 */
export function decodeJwt(token: string | null | undefined): JwtClaims | null {
  if (!token) return null
  const parts = token.split('.')
  if (parts.length < 2) return null
  try {
    return JSON.parse(base64UrlDecode(parts[1])) as JwtClaims
  } catch {
    return null
  }
}

/**
 * 判断 access token 是否已过期（或在 skewSeconds 容差内即将过期）。
 * 用于请求拦截器在发请求前主动刷新过期 token，避免加载期一条无谓的 401（BUG-008）。
 * token 缺失或无 exp 声明时返回 false——交由后端鉴权与既有 401 拦截器兜底处理。
 */
export function isTokenExpired(token: string | null | undefined, skewSeconds = 5): boolean {
  const claims = decodeJwt(token)
  if (!claims?.exp) return false
  return Date.now() >= (claims.exp - skewSeconds) * 1000
}
