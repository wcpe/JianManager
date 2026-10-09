import { describe, it, expect } from 'vitest'
import { reportFilename, reportDisclaimer } from '@/lib/bot-load/bot-load-report'
import { BOT_CHAT_SUCCESS_DISCLAIMER_ZH } from '@/lib/bot-load/bot-load-types'

describe('report helpers', () => {
  it('文件名格式', () => {
    expect(reportFilename('abc', 'json')).toBe('bot-load-abc.json')
    expect(reportFilename('abc', 'csv')).toBe('bot-load-abc.csv')
  })

  it('免责声明兜底', () => {
    expect(reportDisclaimer(null)).toBe(BOT_CHAT_SUCCESS_DISCLAIMER_ZH)
    expect(reportDisclaimer({ disclaimer: '  x  ' } as never)).toBe('x')
  })
})
