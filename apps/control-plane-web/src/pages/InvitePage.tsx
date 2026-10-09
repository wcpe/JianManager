// 视图已迁至 @jianmanager/ui（ADR-097）；本层只读 URL fragment 令牌、发接受请求并清理 URL。
import { Link } from 'react-router'
import api from '@/api/client'
import { InvitePageView } from '@/components/views/auth/InvitePageView'

/**
 * 公开邀请接受页（FR-405）容器：令牌仅从 fragment 读取，避免跟随首个请求进入服务端日志；
 * 成功后先把 URL 中的令牌抹掉（history.replaceState），交视图展示「已接受」与去登录入口。
 * 保留同路径默认导出，路由表无需改动。
 */
export default function InvitePage() {
  return (
    <InvitePageView
      onSubmit={async ({ username, password }) => {
        const token = window.location.hash.slice(1)
        // 令牌缺失不发请求：抛错后由视图以原文案 `users.invitationInvalid` 兜底提示。
        if (!token) throw new Error('missing-invitation-token')
        await api.post('/auth/invitations/accept', { token, username, password })
        window.history.replaceState(null, '', '/invite')
      }}
      renderLoginLink={({ to, children }) => <Link to={to}>{children}</Link>}
    />
  )
}
