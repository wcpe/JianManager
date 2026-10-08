/**
 * @file GroupMembersDialogView：用户组成员管理的受控视图，取数/防抖/增删请求由应用容器负责。
 * @input Combobox/Button/Dialog/滚动壳原语、翻译上下文
 * @output GroupMembersDialogView、GroupMembersDialogViewProps、GroupMemberView、GroupMemberCandidate
 * @sync apps/control-plane-web/src/components/GroupMembersDialog.tsx、apps/control-plane-web/src/pages/GroupsPage.tsx
 * @since FR-502（组件受控化迁包；原 FR-156，候选服务端搜索为 FR-336）
 */
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  ScrollableDialogBody,
  scrollableDialogContentClass,
} from '@jianmanager/ui/components/scrollable-dialog'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { Combobox, type ComboboxOption } from '@jianmanager/ui/components/combobox'
import { Button } from '@jianmanager/ui/components/button'

/** 成员行展示与增删所需字段（`id` 作 React key，`userId` 作增删主键）。 */
export interface GroupMemberView {
  id: number
  userId: number
  role: number
  user?: { username: string }
}

/** 候选用户所需字段（视图据 `id` 去重已入组成员、`username` 作展示名）。 */
export interface GroupMemberCandidate {
  id: number
  username: string
}

/**
 * 受控边界：成员列表与候选窗口经 props 注入，键入经 `onQueryChange` 上报（防抖与请求是容器策略），
 * 加入/移除经 `onAdd`/`onRemove` 上报，关闭经 `onClose` 上报。
 */
export interface GroupMembersDialogViewProps {
  /** 目标组名（标题插值用；组未加载时缺省按空串呈现）。 */
  groupName?: string
  /** 当前成员（容器从实时组数据取，增删后即时反映）；缺省按空列表呈现。 */
  members?: GroupMemberView[]
  /** 候选窗口（服务端搜索结果）；缺省表示尚未有结果，不渲染任何候选。 */
  candidates?: GroupMemberCandidate[]
  /** 候选总数（服务端返回），用于截断提示；不给则不提示。 */
  candidateTotal?: number
  /** 键入关键字上报（容器做 300ms 防抖后下发服务端 q）。 */
  onQueryChange: (keyword: string) => void
  /** 加入成员（容器注入 mutation；提示与缓存失效由容器负责）。 */
  onAdd: (userId: number) => void
  /** 移除成员（容器注入 mutation）。 */
  onRemove: (userId: number) => void
  /** 加入进行中（禁用加入按钮）。 */
  adding?: boolean
  /** 移除进行中（禁用移除按钮）。 */
  removing?: boolean
  /** 关闭对话框：底部按钮、遮罩与 Esc 均走此回调。 */
  onClose: () => void
}

/**
 * 管理用户组成员：列出现有成员可移除 + 选择用户加入（FR-156，兑现 FR-003）。
 * 候选走服务端搜索（FR-336）：候选窗口由容器按同一上限注入，Combobox 键入经容器 debounce 300ms 下发 q；
 * 已选成员列表来自调用方注入的实时组数据，与候选加载解耦、恒显。
 */
export function GroupMembersDialogView({
  groupName,
  members,
  candidates,
  candidateTotal,
  onQueryChange,
  onAdd,
  onRemove,
  adding = false,
  removing = false,
  onClose,
}: GroupMembersDialogViewProps) {
  const { t } = useTranslation()
  const [pick, setPick] = useState('')

  const memberRows = members ?? []
  const memberUserIds = new Set(memberRows.map((m) => m.userId))
  const options: ComboboxOption[] = (candidates ?? [])
    .filter((u) => !memberUserIds.has(u.id))
    .map((u) => ({ value: String(u.id), label: u.username }))
  // 服务端截断时提示「已显示前 N / 共 total」，引导键入缩小范围。
  // 候选缺省（容器未传 items，如 mock/后端异常返裸数组）时不提示，避免读 length 炸掉。
  const candidateHint =
    candidates && candidateTotal !== undefined && candidateTotal > candidates.length
      ? { shown: candidates.length, total: candidateTotal }
      : null

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose() }}>
      <DialogContent className={`${scrollableDialogContentClass} sm:max-w-md`}>
        <DialogHeader>
          <DialogTitle>{t('groups.manageMembers', { name: groupName ?? '' })}</DialogTitle>
        </DialogHeader>

        <ScrollableDialogBody className="space-y-4">
          <div className="space-y-2">
            {memberRows.map((m) => (
              <div key={m.id} className="flex items-center justify-between rounded border px-3 py-1.5 text-sm">
                <span>
                  {m.user?.username ?? `${t('groups.userPrefix')}${m.userId}`}
                  {m.role === 1 && <span className="ml-1 text-xs text-muted-foreground">({t('groups.admin')})</span>}
                </span>
                <Button
                  variant="ghost"
                  size="xs"
                  className="text-red-600 hover:text-red-700"
                  disabled={removing}
                  onClick={() => onRemove(m.userId)}
                >
                  {t('groups.removeMember')}
                </Button>
              </div>
            ))}
            {memberRows.length === 0 && (
              <p className="text-sm text-muted-foreground">{t('groups.noMembers')}</p>
            )}
          </div>

          <div className="space-y-1">
            <div className="flex items-center gap-2">
              <div className="flex-1">
                <Combobox
                  options={options}
                  value={pick}
                  onChange={setPick}
                  onQueryChange={onQueryChange}
                  allowCustom={false}
                  placeholder={t('groups.selectUser')}
                />
              </div>
              <Button
                size="sm"
                disabled={!pick || adding}
                onClick={() => {
                  if (pick) {
                    onAdd(Number(pick))
                    setPick('')
                  }
                }}
              >
                {t('groups.addMember')}
              </Button>
            </div>
            {candidateHint && (
              <p className="text-xs text-muted-foreground">
                {t('groups.candidateHint', { shown: candidateHint.shown, total: candidateHint.total })}
              </p>
            )}
          </div>
        </ScrollableDialogBody>

        <DialogFooter className="pt-4">
          <Button type="button" variant="outline" onClick={onClose}>
            {t('common.close')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
