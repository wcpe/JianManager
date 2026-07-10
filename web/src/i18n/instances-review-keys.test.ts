import { describe, it, expect } from 'vitest'
import zh from './zh.json'
import en from './en.json'

// 复现真机缺陷：创建实例向导（Docker 模式）确认页 ReviewRow 用 t('instances.unlimited')
// 展示 CPU/内存/磁盘的「不限制」值，但该 key 只定义在 metrics.unlimited、instances 命名空间下缺失，
// 导致确认页磁盘上限渲染成裸 key「instances.unlimited」。
// 见 web/src/pages/InstanceWizardPage.tsx（cpuLimit/memLimit/diskLimit 的 value 回退）。
describe('创建实例向导确认页所用 i18n key 必须齐备', () => {
  const usedKeys = ['cpuLimit', 'memLimit', 'diskLimit', 'unlimited']

  it('zh.instances 含全部 ReviewRow 用到的 key', () => {
    const inst = (zh as Record<string, Record<string, string>>).instances
    for (const k of usedKeys) {
      expect(inst[k], `zh.instances.${k} 缺失`).toBeTruthy()
    }
  })

  it('en.instances 含全部 ReviewRow 用到的 key', () => {
    const inst = (en as Record<string, Record<string, string>>).instances
    for (const k of usedKeys) {
      expect(inst[k], `en.instances.${k} 缺失`).toBeTruthy()
    }
  })
})
