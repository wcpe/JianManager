import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus } from 'lucide-react'
import { toast } from 'sonner'
import {
  useClientSecurityGroups,
  useCreateClientSecurityGroup,
  type SecurityTargetType,
} from '@/api/clientDistSecurity'
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
import { EmptyState, fmtTime } from './security-shared'

/**
 * 安全分组：顶部「新建分组」按钮 + 模态表单 + 全宽分组列表（与处置 Tab 同范式）。
 */

const TARGET_OPTIONS: SecurityTargetType[] = ['ip', 'key', 'channel', 'machine', 'install', 'player']

function targetTypeLabel(target: SecurityTargetType, t: (key: string) => string): string {
  if (target === 'channel') return t('clientDistOps.groups.targetChannel')
  if (target === 'player') return t('clientDistOps.groups.targetPlayer')
  return target
}

export function GroupsTab() {
  const { t } = useTranslation()
  const { data, isError, isLoading } = useClientSecurityGroups()
  const createGroup = useCreateClientSecurityGroup()
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

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return
    createGroup.mutate(
      { name: name.trim(), kind: 'manual', targetType, enabled: true, rule: null, actionPolicy: null },
      {
        onSuccess: () => {
          toast.success(t('clientDistOps.groups.toastCreated'))
          setOpen(false)
        },
        onError: () => toast.error(t('clientDistOps.groups.toastCreateFailed')),
      },
    )
  }

  const groups = data ?? []

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
            <Button type="submit" form="ops-create-group-form" disabled={createGroup.isPending || !name.trim()}>
              {t('clientDistOps.groups.createGroup')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
