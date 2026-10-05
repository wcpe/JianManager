import ControlledDangerConfirm, {
  type DangerConfirmProps as ControlledDangerConfirmProps,
} from '@jianmanager/ui/components/views/DangerConfirm'
import { useDangerPermission } from '@/lib/danger'

/**
 * 危险操作确认弹窗的应用接线层（ADR-097）。
 *
 * 弹窗本体已迁入组件库并受控——它不自行判定权限，只接收 `allowed`。本层只做一件事：
 * 读当前登录用户的角色等级，把门禁结果注入。保留同名同签名的默认导出，
 * 使既有 40+ 处调用点无需改动（调用点关心的是「弹个确认框」，不是「权限从哪来」）。
 */
export type DangerConfirmProps = Omit<ControlledDangerConfirmProps, 'allowed'>

export default function DangerConfirm({ scope, ...rest }: DangerConfirmProps) {
  const { allowed } = useDangerPermission(scope ?? 'group')
  return <ControlledDangerConfirm {...rest} scope={scope} allowed={allowed} />
}
