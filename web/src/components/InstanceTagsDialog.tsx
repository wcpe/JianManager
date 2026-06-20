import { useState, type FormEvent, type KeyboardEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useUpdateInstance } from '@/api/instances'
import { ENV_TAG_PREFIX } from '@/components/console/instance-grouping'
import { Badge } from '@/components/ui/badge'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

interface InstanceTagsDialogProps {
  instanceId: number
  instanceName: string
  /** 当前标签集合（含 env: 环境标签 + 自由标签）。 */
  tags: string[]
  onClose: () => void
}

/** 内置环境维度选项（FR-047 复用 Tags 的 env: 约定）。无环境用哨兵值。 */
const ENV_NONE = '__none__'
const ENV_VALUES = ['dev', 'test', 'prod']

/**
 * 实例标签编辑器（FR-047）：环境维度（dev/test/prod，单选，写为 `env:` 前缀标签）
 * + 自由标签（chip 增删）。保存时合并为标签数组，经 PUT /instances/:id 持久化。
 */
export default function InstanceTagsDialog({ instanceId, instanceName, tags, onClose }: InstanceTagsDialogProps) {
  const { t } = useTranslation()
  const update = useUpdateInstance()

  // 拆分既有标签为「环境」与「自由标签」两部分分别编辑。
  const existingEnv = tags.find((tg) => tg.startsWith(ENV_TAG_PREFIX))?.slice(ENV_TAG_PREFIX.length) ?? ''
  const [env, setEnv] = useState<string>(existingEnv || ENV_NONE)
  const [freeTags, setFreeTags] = useState<string[]>(tags.filter((tg) => !tg.startsWith(ENV_TAG_PREFIX)))
  const [draft, setDraft] = useState('')

  const addDraft = () => {
    const v = draft.trim()
    if (!v) return
    // 输入 env: 前缀的标签时归一到环境选择，避免双份环境标签。
    if (v.startsWith(ENV_TAG_PREFIX)) {
      setEnv(v.slice(ENV_TAG_PREFIX.length) || ENV_NONE)
      setDraft('')
      return
    }
    if (!freeTags.includes(v)) setFreeTags((prev) => [...prev, v])
    setDraft('')
  }

  const onDraftKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter' || e.key === ',') {
      e.preventDefault()
      addDraft()
    }
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const merged = [...freeTags]
    if (env !== ENV_NONE) merged.unshift(`${ENV_TAG_PREFIX}${env}`)
    update.mutate(
      { id: instanceId, body: { tags: merged } },
      {
        onSuccess: () => {
          toast.success(t('grouping.tagsSaved'))
          onClose()
        },
      },
    )
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
      <div className="bg-background border rounded-lg p-6 w-full max-w-md shadow-lg">
        <h2 className="text-lg font-bold mb-4">{t('grouping.tagsTitle', { name: instanceName })}</h2>
        <form onSubmit={submit} className="space-y-4">
          <div>
            <label className="text-sm font-medium">{t('grouping.environment')}</label>
            <Select value={env} onValueChange={setEnv}>
              <SelectTrigger className="w-full mt-1">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ENV_NONE}>{t('grouping.envNone')}</SelectItem>
                {ENV_VALUES.map((e) => (
                  <SelectItem key={e} value={e}>
                    {t(`grouping.env_${e}`, { defaultValue: e })}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div>
            <label className="text-sm font-medium">{t('grouping.freeTags')}</label>
            <div className="mt-1 flex flex-wrap gap-1 min-h-8">
              {freeTags.length === 0 && <span className="text-xs text-muted-foreground py-1">{t('grouping.noTags')}</span>}
              {freeTags.map((tg) => (
                <Badge key={tg} variant="secondary" className="font-normal gap-1">
                  {tg}
                  <button
                    type="button"
                    onClick={() => setFreeTags((prev) => prev.filter((x) => x !== tg))}
                    className="text-muted-foreground hover:text-foreground"
                    aria-label={t('grouping.removeTag')}
                  >
                    ×
                  </button>
                </Badge>
              ))}
            </div>
            <div className="mt-2 flex gap-2">
              <input
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                onKeyDown={onDraftKey}
                placeholder={t('grouping.addTagPlaceholder')}
                className="flex-1 px-3 py-2 border rounded-md bg-background text-sm"
              />
              <button
                type="button"
                onClick={addDraft}
                className="px-3 py-2 text-sm border rounded-md hover:bg-accent"
              >
                {t('grouping.addTag')}
              </button>
            </div>
          </div>

          <div className="flex justify-end gap-2 pt-2">
            <button type="button" onClick={onClose} className="px-4 py-2 text-sm border rounded-md hover:bg-accent">
              {t('common.cancel')}
            </button>
            <button
              type="submit"
              disabled={update.isPending}
              className="px-4 py-2 text-sm bg-primary text-primary-foreground rounded-md disabled:opacity-50"
            >
              {update.isPending ? t('common.saving') : t('common.save')}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
