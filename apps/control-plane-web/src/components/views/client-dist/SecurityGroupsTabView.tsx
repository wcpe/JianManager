import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus } from 'lucide-react'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Panel } from '@jianmanager/ui/components/panel'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@jianmanager/ui/components/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import type {
  ClientSecurityGroup,
  SaveSecurityGroupRequest,
  SecurityTargetType,
} from '@/lib/client-dist-security-contracts'
import { EmptyState, fmtTime } from '@/components/views/client-dist/security-format'

/**
 * 安全分组：顶部「新建分组」按钮 + 模态表单 + 全宽分组列表（与处置 Tab 同范式）。
 *
 * 受控视图（ADR-097 b 范式）：不取数、不发请求、不弹 toast——
 * 列表数据与加载/错误态经 props 注入（应用侧容器调 `useClientSecurityGroups`），
 * 创建动作经 `onCreate` 上报请求体并由其返回值告知成败；
 * 成功/失败文案与 toast 属应用侧决策，关闭模态的时机由本组件的编辑语义决定
 * （成功才关闭、失败保留输入便于重试）。表单校验与「打开即重置」仍留在包内（UI 状态）。
 */

const TARGET_OPTIONS: SecurityTargetType[] = ['ip', 'key', 'channel', 'machine', 'install', 'player']

function targetTypeLabel(target: SecurityTargetType, t: (key: string) => string): string {
  if (target === 'channel') return t('clientDistOps.groups.targetChannel')
  if (target === 'player') return t('clientDistOps.groups.targetPlayer')
  return target
}

/** 安全分组 Tab 的注入契约（列表数据来源：`useClientSecurityGroups`）。 */
export interface GroupsTabViewProps {
  /** 分组列表（空数组即空态）。 */
  groups: ClientSecurityGroup[]
  /** 取数中；仅在无行时展示「加载中」文案。 */
  isLoading: boolean
  /** 取数失败；判定优先于空态。 */
  isError: boolean
  /** 创建在途（禁用提交按钮，避免重复提交）。 */
  creating?: boolean
  /** 新建分组：上报请求体，返回是否成功（成功则由视图关闭模态）。 */
  onCreate: (body: SaveSecurityGroupRequest) => Promise<boolean>
}

export function GroupsTabView({
  groups,
  isLoading,
  isError,
  creating = false,
  onCreate,
}: GroupsTabViewProps) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [targetType, setTargetType] = useState<SecurityTargetType>('ip')
  const [prevOpen, setPrevOpen] = useState(open)

  // 打开时重置表单（渲染期，避免 effect 内同步 setState）
  if (open !== prevOpen) {
    setPrevOpen(open)
    if (open) {
      setName('')
      setTargetType('ip')
    }
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return
    const ok = await onCreate({ name: name.trim(), kind: 'manual', targetType, enabled: true, rule: null, actionPolicy: null })
    if (ok) setOpen(false)
  }

  return (
    <div className="space-y-4" data-testid="ops-groups">
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" onClick={() => setOpen(true)} data-testid="action-open-create-group">
          <Plus className="size-4" />
          {t('clientDistOps.groups.createGroup')}
        </Button>
      </div>

      <Panel title={t('clientDistOps.groups.title')}>
        {isError ? (
          <EmptyState text={t('clientDistOps.groups.error')} />
        ) : groups.length === 0 ? (
          <EmptyState text={isLoading ? t('common.loading') : t('clientDistOps.groups.empty')} />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('common.name')}</TableHead>
                <TableHead>{t('common.type')}</TableHead>
                <TableHead>{t('clientDistOps.groups.colTarget')}</TableHead>
                <TableHead>{t('common.enabled')}</TableHead>
                <TableHead>{t('clientDistOps.groups.colUpdated')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {groups.map((group) => (
                <TableRow key={group.id}>
                  <TableCell className="font-medium">{group.name}</TableCell>
                  <TableCell>{group.kind}</TableCell>
                  <TableCell>{targetTypeLabel(group.targetType, t)}</TableCell>
                  <TableCell>
                    <Badge variant={group.enabled ? 'default' : 'outline'}>
                      {group.enabled ? t('clientDistOps.groups.enabled') : t('clientDistOps.groups.disabled')}
                    </Badge>
                  </TableCell>
                  <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{fmtTime(group.updatedAt)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </Panel>

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className={scrollableDialogContentClass}>
          <DialogHeader>
            <DialogTitle>{t('clientDistOps.groups.newGroupTitle')}</DialogTitle>
            <DialogDescription>{t('clientDistOps.groups.newGroupDesc', '创建手动安全分组，用于后续批量处置。')}</DialogDescription>
          </DialogHeader>
          <form id="ops-create-group-form" onSubmit={submit}>
            <ScrollableDialogBody className="space-y-3">
              <label className="flex flex-col gap-1 text-sm">
                {t('common.name')}
                <Input
                  required
                  autoFocus
                  placeholder={t('clientDistOps.groups.phGroupName')}
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                />
              </label>
              <label className="flex flex-col gap-1 text-sm">
                {t('clientDistOps.groups.colTarget')}
                <Select value={targetType} onValueChange={(v) => setTargetType(v as SecurityTargetType)}>
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {TARGET_OPTIONS.map((opt) => (
                      <SelectItem key={opt} value={opt}>
                        {targetTypeLabel(opt, t)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </label>
            </ScrollableDialogBody>
          </form>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" form="ops-create-group-form" disabled={creating || !name.trim()}>
              {t('clientDistOps.groups.createGroup')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
