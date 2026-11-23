import { describe, it, expect } from 'vitest'
import {
  validateRequired,
  minLength,
  validatePort,
  validatePositiveInt,
  validateAbsPath,
  validateUrl,
  validateEnvRef,
  validateHost,
  validateFields,
  hasErrors,
} from './form-validation'

describe('validateRequired', () => {
  it('空与纯空白判错', () => {
    expect(validateRequired('')).toBe('validation.required')
    expect(validateRequired('   ')).toBe('validation.required')
  })
  it('非空合法', () => {
    expect(validateRequired('a')).toBeUndefined()
  })
})

describe('minLength', () => {
  const min3 = minLength(3)
  it('空串放行（交由 required）', () => {
    expect(min3('')).toBeUndefined()
  })
  it('短于阈值判错，达标放行', () => {
    expect(min3('ab')).toBe('validation.minLength')
    expect(min3('abc')).toBeUndefined()
    expect(min3('abcd')).toBeUndefined()
  })
})

describe('validatePort', () => {
  it('空串放行（交由 required）', () => {
    expect(validatePort('')).toBeUndefined()
  })
  it('合法端口 1-65535', () => {
    expect(validatePort('1')).toBeUndefined()
    expect(validatePort('25565')).toBeUndefined()
    expect(validatePort('65535')).toBeUndefined()
  })
  it('越界与非整数判错', () => {
    expect(validatePort('0')).toBe('validation.portRange')
    expect(validatePort('65536')).toBe('validation.portRange')
    expect(validatePort('80.5')).toBe('validation.portInteger')
    expect(validatePort('abc')).toBe('validation.portInteger')
  })
})

describe('validatePositiveInt', () => {
  it('空串放行', () => {
    expect(validatePositiveInt('')).toBeUndefined()
  })
  it('正整数合法，0/负/非整数判错', () => {
    expect(validatePositiveInt('2048')).toBeUndefined()
    expect(validatePositiveInt('0')).toBe('validation.positive')
    expect(validatePositiveInt('-1')).toBe('validation.integer')
    expect(validatePositiveInt('1.5')).toBe('validation.integer')
  })
})

describe('validateAbsPath', () => {
  it('空串放行', () => {
    expect(validateAbsPath('')).toBeUndefined()
  })
  it('接受 Unix 与 Windows 绝对路径', () => {
    expect(validateAbsPath('/servers/survival')).toBeUndefined()
    expect(validateAbsPath('C:/Java/jdk-21')).toBeUndefined()
    expect(validateAbsPath('C:\\Java\\jdk-21')).toBeUndefined()
  })
  it('相对路径判错', () => {
    expect(validateAbsPath('servers/survival')).toBe('validation.absPath')
    expect(validateAbsPath('./x')).toBe('validation.absPath')
  })
})

describe('validateUrl', () => {
  it('空串放行', () => {
    expect(validateUrl('')).toBeUndefined()
  })
  it('http/https 合法', () => {
    expect(validateUrl('https://example.com/a.jar')).toBeUndefined()
    expect(validateUrl('http://localhost:8080/x')).toBeUndefined()
  })
  it('非法 scheme 与非 URL 判错', () => {
    expect(validateUrl('ftp://example.com')).toBe('validation.urlScheme')
    expect(validateUrl('not a url')).toBe('validation.url')
  })
})

describe('validateEnvRef', () => {
  it('空串放行', () => {
    expect(validateEnvRef('')).toBeUndefined()
  })
  it('${NAME} 形式合法', () => {
    expect(validateEnvRef('${S3_KEY}')).toBeUndefined()
    expect(validateEnvRef('${_X1}')).toBeUndefined()
  })
  it('明文/非法引用判错', () => {
    expect(validateEnvRef('AKIAXXXX')).toBe('validation.envRef')
    expect(validateEnvRef('${1BAD}')).toBe('validation.envRef')
    expect(validateEnvRef('$NAME')).toBe('validation.envRef')
  })
})

describe('validateHost', () => {
  it('空串放行', () => {
    expect(validateHost('')).toBeUndefined()
  })
  it('域名/IP/localhost 合法', () => {
    expect(validateHost('mc.example.com')).toBeUndefined()
    expect(validateHost('127.0.0.1')).toBeUndefined()
    expect(validateHost('localhost')).toBeUndefined()
  })
  it('非法主机判错', () => {
    expect(validateHost('bad host')).toBe('validation.host')
    expect(validateHost('-leading.com')).toBe('validation.host')
  })
})

describe('validateFields / hasErrors', () => {
  it('按规则链短路返回首个错误，仅含有错字段', () => {
    const errors = validateFields(
      { name: '', port: '70000' },
      { name: [validateRequired], port: [validatePort] },
    )
    expect(errors).toEqual({ name: 'validation.required', port: 'validation.portRange' })
  })
  it('全合法返回空对象', () => {
    const errors = validateFields(
      { name: 'x', port: '25565' },
      { name: [validateRequired], port: [validatePort] },
    )
    expect(errors).toEqual({})
    expect(hasErrors(errors)).toBe(false)
  })
  it('hasErrors 检出任一错误', () => {
    expect(hasErrors({ a: undefined, b: 'validation.required' })).toBe(true)
    expect(hasErrors({ a: undefined })).toBe(false)
  })
})
