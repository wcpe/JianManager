import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ArrowDown, ArrowUp, Copy, Plus, Trash2 } from 'lucide-react'
import type { BotLoadCommand, BotLoadCommandSchedule } from '@/lib/bot-load/bot-load-types'
import { commandScheduleToYaml, yamlToCommandSchedule } from '@/lib/bot-load/bot-load-summaries'
import { validateCommandSchedule } from '@/lib/bot-load/bot-load-validation'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { FieldLabel, FieldError } from '@jianmanager/ui/components/field-label'
import { Segments, Segment } from '@jianmanager/ui/components/layout'
import { cn } from '@jianmanager/ui'
import { Card } from '@jianmanager/ui/components/card'
import { Textarea } from '@jianmanager/ui/components/textarea'

interface CommandPlanEditorProps {
  value: BotLoadCommandSchedule
  onChange: (next: BotLoadCommandSchedule) => void
  /** path 级服务端错误 */
  serverErrors?: Array<{ path: string; message: string }>
}

/**
 * 结构化命令编排编辑器 + 可选高级 YAML。
 * 排序用上下移动按钮，不引入拖拽依赖。
 */
export function CommandPlanEditor({ value, onChange, serverErrors = [] }: CommandPlanEditorProps) {
  const { t } = useTranslation()
  const [mode, setMode] = useState<'structured' | 'yaml'>('structured')
  const [yamlText, setYamlText] = useState(() => commandScheduleToYaml(value))
  const [yamlError, setYamlError] = useState('')
  const localErrors = useMemo(() => validateCommandSchedule(value), [value])
  const errorMap = useMemo(() => {
    const m = new Map<string, string>()
    for (const e of [...localErrors, ...serverErrors]) m.set(e.path, e.message)
    return m
  }, [localErrors, serverErrors])

  const updateCmd = (index: number, patch: Partial<BotLoadCommand>) => {
    const commands = value.commands.map((c, i) => (i === index ? { ...c, ...patch } : c))
    onChange({ ...value, commands })
  }

  const move = (index: number, dir: -1 | 1) => {
    const j = index + dir
    if (j < 0 || j >= value.commands.length) return
    const commands = [...value.commands]
    const a = commands[index]
    const b = commands[j]
    // index 由列表下标传入、j 已由上方边界检查，两者恒有效；判空仅为类型收窄，避免写入 undefined 项。
    if (a === undefined || b === undefined) return
    commands[index] = b
    commands[j] = a
    onChange({ ...value, commands })
  }

  const remove = (index: number) => {
    onChange({ ...value, commands: value.commands.filter((_, i) => i !== index) })
  }

  const duplicate = (index: number) => {
    const src = value.commands[index]
    // index 由列表下标传入，恒有效；判空仅为类型收窄（否则会复制出 undefined 项）。
    if (!src) return
    const copy: BotLoadCommand = {
      ...src,
      id: `${src.id}-copy-${Date.now() % 10000}`,
      repeat: src.repeat ? { ...src.repeat } : undefined,
    }
    const commands = [...value.commands]
    commands.splice(index + 1, 0, copy)
    onChange({ ...value, commands })
  }

  const add = () => {
    const id = `cmd-${Date.now() % 100000}`
    onChange({
      ...value,
      commands: [...value.commands, { id, atMs: 0, command: '' }],
    })
  }

  const switchToYaml = () => {
    setYamlText(commandScheduleToYaml(value))
    setYamlError('')
    setMode('yaml')
  }

  const switchToStructured = () => {
    const parsed = yamlToCommandSchedule(yamlText)
    if (!parsed) {
      setYamlError(t('botsLoad.yamlParseFailed'))
      return
    }
    const errs = validateCommandSchedule(parsed)
    const firstError = errs[0]
    if (firstError) {
      setYamlError(firstError.message)
      return
    }
    onChange(parsed)
    setYamlError('')
    setMode('structured')
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        {/* 结构化 / YAML 两档收敛到分段原语。注意两个 onClick 语义并不对称，原样保留：
            「结构化」在已处于该模式时不做任何事（避免无谓重写 value）；
            「YAML」始终走 switchToYaml——它内部会校验并可能拒绝切换（把错误落到 yamlError）。 */}
        <Segments aria-label={t('botsLoad.modeStructured')}>
          <Segment active={mode === 'structured'} onClick={() => (mode === 'yaml' ? switchToStructured() : undefined)}>
            {t('botsLoad.modeStructured')}
          </Segment>
          <Segment active={mode === 'yaml'} onClick={switchToYaml}>
            {t('botsLoad.modeYaml')}
          </Segment>
        </Segments>
        <span className="text-xs text-muted-foreground">{t('botsLoad.commandPlanHint')}</span>
      </div>

      {mode === 'yaml' ? (
        <div className="space-y-2">
          {/* 组件库 Textarea 自带边框/圆角/焦点环，这里只保留等宽字体与高度/缩放相关样式 */}
          <Textarea
            aria-label={t('botsLoad.modeYaml')}
            className="min-h-64 resize-y font-mono text-sm leading-5"
            value={yamlText}
            onChange={(e) => setYamlText(e.target.value)}
            spellCheck={false}
          />
          {yamlError && <p className="text-sm text-destructive">{yamlError}</p>}
          <Button type="button" size="sm" variant="outline" onClick={switchToStructured}>
            {t('botsLoad.applyYaml')}
          </Button>
        </div>
      ) : (
        <>
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1">
              <FieldLabel htmlFor="cmd-duration">{t('botsLoad.durationMs')}</FieldLabel>
              <Input
                id="cmd-duration"
                type="number"
                value={value.durationMs}
                onChange={(e) => onChange({ ...value, durationMs: Number(e.target.value) })}
                aria-invalid={!!errorMap.get('commandSchedule.durationMs')}
              />
              <FieldError error={errorMap.get('commandSchedule.durationMs')} />
            </div>
            <div className="space-y-1">
              <FieldLabel htmlFor="cmd-jitter">{t('botsLoad.jitterMs')}</FieldLabel>
              <Input
                id="cmd-jitter"
                type="number"
                value={value.jitterMs ?? 0}
                onChange={(e) => onChange({ ...value, jitterMs: Number(e.target.value) })}
                aria-invalid={!!errorMap.get('commandSchedule.jitterMs')}
              />
              <FieldError error={errorMap.get('commandSchedule.jitterMs')} />
            </div>
          </div>

          <ul className="space-y-2" aria-label={t('botsLoad.commandList')}>
            {value.commands.map((cmd, index) => {
              const base = `commandSchedule.commands[${index}]`
              return (
                <li key={`${cmd.id}-${index}`}>
                  {/* Card 渲染为 div，不能顶替 <li>（会破坏父级 <ul> 的列表语义与 listitem 角色），
                      故作为列表项内部的卡片容器；圆角/描边/底色/阴影统一由 Card 提供。 */}
                  <Card className={cn('gap-0 p-3', errorMap.has(`${base}.command`) && 'border-destructive')}>
                  <div className="mb-2 flex items-center justify-between gap-2">
                    <span className="text-xs font-medium text-muted-foreground">
                      {t('botsLoad.stepN', { n: index + 1 })}
                    </span>
                    <div className="flex gap-1">
                      <Button type="button" size="xs" variant="ghost" onClick={() => move(index, -1)} disabled={index === 0} aria-label={t('botsLoad.moveUp')}>
                        <ArrowUp className="size-3.5" />
                      </Button>
                      <Button type="button" size="xs" variant="ghost" onClick={() => move(index, 1)} disabled={index === value.commands.length - 1} aria-label={t('botsLoad.moveDown')}>
                        <ArrowDown className="size-3.5" />
                      </Button>
                      <Button type="button" size="xs" variant="ghost" onClick={() => duplicate(index)} aria-label={t('botsLoad.duplicate')}>
                        <Copy className="size-3.5" />
                      </Button>
                      <Button type="button" size="xs" variant="ghost" onClick={() => remove(index)} disabled={value.commands.length <= 1} aria-label={t('common.delete')}>
                        <Trash2 className="size-3.5" />
                      </Button>
                    </div>
                  </div>
                  <div className="grid grid-cols-1 gap-2 md:grid-cols-3">
                    <div className="space-y-1">
                      <FieldLabel htmlFor={`${base}-id`}>{t('botsLoad.commandId')}</FieldLabel>
                      <Input
                        id={`${base}-id`}
                        value={cmd.id}
                        onChange={(e) => updateCmd(index, { id: e.target.value })}
                        aria-invalid={!!errorMap.get(`${base}.id`)}
                      />
                      <FieldError error={errorMap.get(`${base}.id`)} />
                    </div>
                    <div className="space-y-1">
                      <FieldLabel htmlFor={`${base}-at`}>{t('botsLoad.atMs')}</FieldLabel>
                      <Input
                        id={`${base}-at`}
                        type="number"
                        value={cmd.atMs}
                        onChange={(e) => updateCmd(index, { atMs: Number(e.target.value) })}
                        aria-invalid={!!errorMap.get(`${base}.atMs`)}
                      />
                      <FieldError error={errorMap.get(`${base}.atMs`)} />
                    </div>
                    <div className="space-y-1 md:col-span-1">
                      <FieldLabel htmlFor={`${base}-cmd`}>{t('botsLoad.commandText')}</FieldLabel>
                      <Input
                        id={`${base}-cmd`}
                        value={cmd.command}
                        onChange={(e) => updateCmd(index, { command: e.target.value })}
                        aria-invalid={!!errorMap.get(`${base}.command`)}
                      />
                      <FieldError error={errorMap.get(`${base}.command`)} />
                    </div>
                  </div>
                  <div className="mt-2 grid grid-cols-2 gap-2">
                    <div className="space-y-1">
                      <FieldLabel htmlFor={`${base}-iv`}>{t('botsLoad.repeatInterval')}</FieldLabel>
                      <Input
                        id={`${base}-iv`}
                        type="number"
                        placeholder="—"
                        value={cmd.repeat?.intervalMs ?? ''}
                        onChange={(e) => {
                          const intervalMs = e.target.value === '' ? undefined : Number(e.target.value)
                          if (intervalMs === undefined) {
                            updateCmd(index, { repeat: undefined })
                          } else {
                            updateCmd(index, {
                              repeat: { intervalMs, count: cmd.repeat?.count ?? 1 },
                            })
                          }
                        }}
                      />
                    </div>
                    <div className="space-y-1">
                      <FieldLabel htmlFor={`${base}-ct`}>{t('botsLoad.repeatCount')}</FieldLabel>
                      <Input
                        id={`${base}-ct`}
                        type="number"
                        placeholder="—"
                        value={cmd.repeat?.count ?? ''}
                        onChange={(e) => {
                          const count = e.target.value === '' ? undefined : Number(e.target.value)
                          if (count === undefined) {
                            updateCmd(index, { repeat: undefined })
                          } else {
                            updateCmd(index, {
                              repeat: { intervalMs: cmd.repeat?.intervalMs ?? 1000, count },
                            })
                          }
                        }}
                      />
                    </div>
                  </div>
                  </Card>
                </li>
              )
            })}
          </ul>
          <Button type="button" size="sm" variant="outline" onClick={add}>
            <Plus className="size-4" /> {t('botsLoad.addCommand')}
          </Button>
        </>
      )}
    </div>
  )
}
