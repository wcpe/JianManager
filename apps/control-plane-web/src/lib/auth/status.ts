/**
 * 用户令牌状态的展示档位。
 *
 * 单独成文件而非与页面同文件：页面组件必须只导出组件，否则 React Fast Refresh
 * 无法可靠热替换（eslint react-refresh/only-export-components）。测试要覆盖这段
 * 映射，就必须让它可导入，因此放到 lib 下。
 *
 * 返回值对齐 @jianmanager/ui 的 StatusLevel；与 AgentTokensPageView 的同类映射
 * 保持同一套取值，避免两个令牌页面状态着色不一致。
 */
export function userTokenStatusLevel(status: 'active' | 'revoked' | 'expired'): 'success' | 'warning' | 'danger' {
  switch (status) {
    case 'active':
      return 'success'
    case 'revoked':
      return 'danger'
    default:
      // expired 与任何未预期值都落到 warning：不是错误，但需要用户处理。
      return 'warning'
  }
}
