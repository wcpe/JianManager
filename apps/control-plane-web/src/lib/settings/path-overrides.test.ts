import { describe, expect, it } from 'vitest'
import {
  allRowsValid,
  duplicateInstanceIds,
  emptyRow,
  parsePathOverrides,
  serializePathOverrides,
  validateRow,
  type PathOverrideRow,
} from './path-overrides'
import { validateSettingDraft } from './settings-form'

const row = (instanceId: string, path: string): PathOverrideRow => ({
  key: `k-${instanceId}-${path}`,
  instanceId,
  path,
})

describe('parsePathOverrides', () => {
  it('正常解析为行，并按实例 ID 数值序排列', () => {
    const rows = parsePathOverrides('{"inst:7":"b.log","inst:1":"a.log","inst:20":"c.log"}')
    expect(rows.map((r) => r.instanceId)).toEqual(['1', '7', '20'])
    expect(rows.map((r) => r.path)).toEqual(['a.log', 'b.log', 'c.log'])
  })

  it('空串 → 无行（后端把空串当"无覆盖"）', () => {
    expect(parsePathOverrides('')).toEqual([])
    expect(parsePathOverrides(undefined)).toEqual([])
    expect(parsePathOverrides('   ')).toEqual([])
  })

  it('非法 JSON 不抛错，回落空数组（坏配置不该崩页）', () => {
    expect(parsePathOverrides('{not json')).toEqual([])
    expect(parsePathOverrides('{"a":')).toEqual([])
  })

  it('非对象（数组/字符串/数字/null）一律不认', () => {
    expect(parsePathOverrides('[]')).toEqual([])
    expect(parsePathOverrides('"str"')).toEqual([])
    expect(parsePathOverrides('123')).toEqual([])
    expect(parsePathOverrides('null')).toEqual([])
  })

  it('键不是 inst:<数字> 的条目丢弃（不是本字段的合法键）', () => {
    const rows = parsePathOverrides('{"inst:1":"a.log","1":"x.log","inst:007":"y.log","inst:x":"z.log"}')
    expect(rows).toHaveLength(1)
    expect(rows[0]?.instanceId).toBe('1')
  })

  it('值不是字符串的条目丢弃', () => {
    expect(parsePathOverrides('{"inst:1":123}')).toEqual([])
    expect(parsePathOverrides('{"inst:1":null}')).toEqual([])
  })

  it('每次解析都给出全新的行 key（避免两次解析共用同一 React key）', () => {
    const a = parsePathOverrides('{"inst:1":"a.log"}')
    const b = parsePathOverrides('{"inst:1":"a.log"}')
    expect(a[0]?.key).not.toBe(b[0]?.key)
  })
})

describe('serializePathOverrides', () => {
  it('序列化为后端要的 JSON 文本', () => {
    expect(serializePathOverrides([row('1', 'a.log'), row('7', 'b.log')]))
      .toBe('{"inst:1":"a.log","inst:7":"b.log"}')
  })

  it('全部为空时返回空串，而不是 "{}"', () => {
    // 空串 = 无覆盖（回落更大范围的值）；"{}" 是"显式空对象"，语义不同。
    expect(serializePathOverrides([])).toBe('')
    expect(serializePathOverrides([emptyRow()])).toBe('')
  })

  it('半填的行被跳过（缺实例或缺路径）', () => {
    expect(serializePathOverrides([row('', 'a.log')])).toBe('')
    expect(serializePathOverrides([row('1', '')])).toBe('')
    hasNoHalfRow()
  })

  it('同一实例重复时后者覆盖前者', () => {
    expect(serializePathOverrides([row('1', 'first.log'), row('1', 'second.log')]))
      .toBe('{"inst:1":"second.log"}')
  })

  it('前后空白被去除', () => {
    expect(serializePathOverrides([row(' 1 ', ' a.log ')])).toBe('{"inst:1":"a.log"}')
  })

  it('与 parse 往返一致（同一个映射不因来回转换而变）', () => {
    const raw = '{"inst:1":"a.log","inst:2":"b/c.log"}'
    expect(serializePathOverrides(parsePathOverrides(raw))).toBe(raw)
  })
})

function hasNoHalfRow() {
  // 占位：上面两条断言已覆盖"缺一半"的两种情形。
}

describe('duplicateInstanceIds', () => {
  it('找出被重复填写的实例', () => {
    expect(duplicateInstanceIds([row('1', 'a'), row('2', 'b'), row('1', 'c')])).toEqual(['1'])
  })

  it('空实例不参与判重（多行都还没选实例是正常状态）', () => {
    expect(duplicateInstanceIds([row('', 'a'), row('', 'b')])).toEqual([])
  })

  it('无重复返回空数组', () => {
    expect(duplicateInstanceIds([row('1', 'a'), row('2', 'b')])).toEqual([])
  })
})

describe('validateRow', () => {
  it('整行空白不报错（刚点"添加"还没填）', () => {
    expect(validateRow(emptyRow())).toBeNull()
  })

  it('缺实例 → 报错', () => {
    expect(validateRow(row('', 'a.log'))).toBe('logAcqOverride.instanceRequired')
  })

  it('缺路径 → 报错', () => {
    expect(validateRow(row('1', ''))).toBe('logAcqOverride.pathRequired')
  })

  it('绝对路径（Unix / Windows 盘符）被拒——必须是相对实例工作目录', () => {
    expect(validateRow(row('1', '/var/log/server.log'))).toBe('logAcqOverride.pathMustBeRelative')
    expect(validateRow(row('1', 'D:/logs/server.log'))).toBe('logAcqOverride.pathMustBeRelative')
    expect(validateRow(row('1', 'C:\\logs\\server.log'))).toBe('logAcqOverride.pathMustBeRelative')
  })

  it('`..` 逃逸被拒', () => {
    expect(validateRow(row('1', '../outside.log'))).toBe('logAcqOverride.pathEscapes')
    expect(validateRow(row('1', 'a/../../x.log'))).toBe('logAcqOverride.pathEscapes')
    // 反斜杠形式同样要拦住（Windows 路径分隔符）
    expect(validateRow(row('1', 'a\\..\\..\\x.log'))).toBe('logAcqOverride.pathEscapes')
  })

  it('普通相对路径合法', () => {
    expect(validateRow(row('1', 'logs/server.log'))).toBeNull()
    expect(validateRow(row('1', 'proxy.log.0'))).toBeNull()
  })
})

describe('allRowsValid', () => {
  it('有空行但其它行合法时仍算合法（空行提交时跳过）', () => {
    expect(allRowsValid([row('1', 'a.log'), emptyRow()])).toBe(true)
  })

  it('有非法行 → 不合法', () => {
    expect(allRowsValid([row('1', '/abs.log')])).toBe(false)
  })

  it('有重复实例 → 不合法（提交会丢一条，必须先解决）', () => {
    expect(allRowsValid([row('1', 'a.log'), row('1', 'b.log')])).toBe(false)
  })
})

describe('与设置页的校验口径一致（防漂移）', () => {
  /*
    【为什么要有这一组】同一个配置项有两个入口：系统设置页（整段 JSON 文本）
    与节点采集面板（成对行编辑）。若两处的校验规则不同，同一个值会在一处
    当场报错、在另一处提交后才失败——用户会认为"面板允许但后端拒绝"，无从判断。

    这里把两边对同一批路径的判定**逐条对照**：行编辑器的 validateRow
    与设置页的 validateSettingDraft 必须给出相同结论。
  */
  const paths = [
    'logs/server.log',
    'proxy.log.0',
    'a/b/c.log',
    'logs/../a.log', // 折叠后仍在工作目录内 → 合法
    './x.log',
    '/abs.log', // 绝对路径 → 非法
    '\\logs\\a.log', // Windows 根相对反斜杠 → 非法
    '\\\\server\\share\\a.log', // Windows UNC 路径 → 非法
    'D:/x.log', // Windows 盘符 → 非法
    '../escape.log', // 逃出工作目录 → 非法
    'a/../../x.log', // 折叠后逃逸 → 非法
  ]

  it.each(paths)('路径 %s：两个入口结论相同', (p) => {
    const rowOk = validateRow({ key: 'k', instanceId: '1', path: p }) === null
    // 设置页校验的是整段 JSON，故包成合法键的单条映射来比对路径规则。
    const settingsErr = validateSettingDraft('log.acq.path_overrides', JSON.stringify({ 'inst:1': p }))
    const settingsOk = settingsErr === undefined
    expect(rowOk, `路径 ${p}：行编辑器=${rowOk}，设置页=${settingsOk}`).toBe(settingsOk)
  })

  it('手输非法实例 ID（非规范整数）在行内即报错', () => {
    expect(validateRow(row('inst:1', 'a.log'))).toBe('logAcqOverride.instanceInvalid')
    expect(validateRow(row('007', 'a.log'))).toBe('logAcqOverride.instanceInvalid')
    expect(validateRow(row('abc', 'a.log'))).toBe('logAcqOverride.instanceInvalid')
    expect(validateRow(row('7', 'a.log'))).toBeNull()
  })
})
