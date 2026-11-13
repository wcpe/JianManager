import { useTranslation } from 'react-i18next'
import { RefreshCw, FileText } from 'lucide-react'
import { Badge, Button, Panel, Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui'
import { EmptyState } from '@jianmanager/ui/components/empty-state'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import { ScrollableDialogBody, scrollableDialogContentClass } from '@jianmanager/ui/components/scrollable-dialog'
import { useAssistantSkills, useReloadAssistantSkills, type AssistantSkill } from '@/api/assistant'
import { useState } from 'react'

/** 
 * 技能管理面板。
 *
 * 技能是「目录 + SKILL.md」的纯文本指令，随仓库发布、也支持在数据根里手工增删。
 * 因此这里做两件必要的事：
 *  1) 展示正文——管理员必须能预览「模型会被告知什么」才谈得上启停判断；
 *  2) 展示被跳过的技能与原因——只回「有哪些」，改坏一个 SKILL.md 就表现为「它凭空消失」。
 */
export function AssistantSkillsPanel({ enabled = true }: { enabled?: boolean }) {
  const { t } = useTranslation()
  const skillsQuery = useAssistantSkills(enabled)
  const reload = useReloadAssistantSkills()
  const [viewing, setViewing] = useState<AssistantSkill | null>(null)

  const skills = skillsQuery.data?.skills ?? []
  const skipped = skillsQuery.data?.skipped ?? {}
  const skippedEntries = Object.entries(skipped)

  return (
    <Panel
      title={t('assistant.skillsTitle', '技能')}
      bodyClassName="p-0"
      actions={
        <Button size="sm" variant="outline" disabled={reload.isPending} onClick={() => void reload.mutateAsync()}>
          <RefreshCw className="size-4" />
          {reload.isPending ? t('assistant.skillsReloading', '扫描中…') : t('assistant.skillsReload', '重新扫描')}
        </Button>
      }
    >
      {skillsQuery.isLoading ? (
        <p className="px-4 py-6 text-center text-sm text-muted-foreground">{t('common.loading', '加载中…')}</p>
      ) : skillsQuery.isError ? (
        <p className="px-4 py-6 text-center text-sm text-destructive" role="alert">
          {t('assistant.skillsError', '读取技能失败。')}
        </p>
      ) : skills.length === 0 ? (
        <EmptyState
          title={t('assistant.skillsEmpty', '还没有技能')}
          description={t(
            'assistant.skillsEmptyDesc',
            '把技能放到数据根的 var/skills/<名称>/SKILL.md，然后点「重新扫描」。',
          )}
        />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('assistant.skillName', '名称')}</TableHead>
              <TableHead>{t('assistant.skillDesc', '用途')}</TableHead>
              <TableHead>{t('assistant.skillVersion', '版本')}</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {skills.map((s) => (
              <TableRow key={s.name}>
                <TableCell className="font-mono text-xs">{s.name}</TableCell>
                <TableCell className="max-w-96 truncate text-sm" title={s.description}>
                  {s.description}
                </TableCell>
                <TableCell>
                  <Badge variant="secondary">{s.version || '—'}</Badge>
                </TableCell>
                <TableCell className="text-right">
                  {/* 可访问名落在真按钮上：只给容器加 aria-label 会让 getByLabelText 命中容器。 */}
                  <Button
                    size="sm"
                    variant="ghost"
                    aria-label={t('assistant.skillView', '查看技能 {{name}}', { name: s.name })}
                    onClick={() => setViewing(s)}
                  >
                    <FileText className="size-4" />
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      {/* 被跳过的技能必须可见：否则「我放了技能但没了」无从解释。 */}
      {skippedEntries.length > 0 && (
        <div className="border-t px-4 py-3">
          <p className="text-xs font-medium text-destructive">
            {t('assistant.skillsSkipped', '以下条目未能装载：')}
          </p>
          <ul className="mt-1 space-y-1">
            {skippedEntries.map(([name, reason]) => (
              <li key={name} className="text-xs text-muted-foreground">
                <span className="font-mono">{name}</span> — {reason}
              </li>
            ))}
          </ul>
        </div>
      )}

      <Dialog open={viewing !== null} onOpenChange={(open) => !open && setViewing(null)}>
        <DialogContent className={scrollableDialogContentClass}>
          <DialogHeader>
            <DialogTitle>{viewing?.name}</DialogTitle>
          </DialogHeader>
          <ScrollableDialogBody>
            {viewing?.whenToUse && (
              <p className="mb-2 text-xs text-muted-foreground">
                {t('assistant.skillWhenToUse', '适用时机')}：{viewing.whenToUse}
              </p>
            )}
            <pre className="whitespace-pre-wrap break-words text-xs">{viewing?.instructions}</pre>
          </ScrollableDialogBody>
        </DialogContent>
      </Dialog>
    </Panel>
  )
}
