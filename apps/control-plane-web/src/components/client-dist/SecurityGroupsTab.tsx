import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { GroupsTabView } from '@/components/views/client-dist/SecurityGroupsTabView'
import type { SaveSecurityGroupRequest } from '@jianmanager/ui/lib/client-dist-security-contracts'
import { useClientSecurityGroups, useCreateClientSecurityGroup } from '@/api/clientDistSecurity'

/**
 * 安全分组：顶部「新建分组」按钮 + 模态表单 + 全宽分组列表（与处置 Tab 同范式）。
 *
 * 展示层已归包（`GroupsTabView`），此处只保留取数与创建 mutation：
 * 成功/失败 toast 文案与在途状态（禁用提交）由应用侧决定，
 * 模态关闭时机由视图按 `onCreate` 返回值处理（成功关闭、失败保留输入）。
 */
export function GroupsTab() {
  const { t } = useTranslation()
  const { data, isError, isLoading } = useClientSecurityGroups()
  const createGroup = useCreateClientSecurityGroup()

  const create = async (body: SaveSecurityGroupRequest): Promise<boolean> => {
    try {
      await createGroup.mutateAsync(body)
      toast.success(t('clientDistOps.groups.toastCreated'))
      return true
    } catch {
      toast.error(t('clientDistOps.groups.toastCreateFailed'))
      return false
    }
  }

  return (
    <GroupsTabView
      groups={data ?? []}
      isLoading={isLoading}
      isError={isError}
      creating={createGroup.isPending}
      onCreate={create}
    />
  )
}
