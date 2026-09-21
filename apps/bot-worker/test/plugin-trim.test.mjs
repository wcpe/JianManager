import assert from 'node:assert/strict'
import test from 'node:test'

import {
  DISABLED_MELEE_PLUGINS,
  REQUIRED_PLUGINS,
  resolvePluginOptions,
} from '../dist/ipc/fleet.js'

test('禁用清单与必要清单不得有交集', () => {
  const disabled = new Set(DISABLED_MELEE_PLUGINS)
  for (const required of REQUIRED_PLUGINS) {
    assert.equal(
      disabled.has(required),
      false,
      `${required} 被 pathfinder 或本项目代码依赖，不允许禁用`,
    )
  }
})

test('resolvePluginOptions 默认把清单内插件全部置为 false', () => {
  const opts = resolvePluginOptions({})

  for (const name of DISABLED_MELEE_PLUGINS) {
    assert.equal(opts[name], false, `${name} 应被禁用`)
  }
  // 必要插件不得出现禁用键，否则 mineflayer 不会加载它们
  for (const required of REQUIRED_PLUGINS) {
    assert.equal(required in opts, false, `${required} 不应出现在开关表里`)
  }
})

test('JM_BOT_WORKER_KEEP_PLUGINS=all 时关闭全部裁剪（逃生开关）', () => {
  assert.deepEqual(resolvePluginOptions({ JM_BOT_WORKER_KEEP_PLUGINS: 'all' }), {})
  // 大小写不敏感
  assert.deepEqual(resolvePluginOptions({ JM_BOT_WORKER_KEEP_PLUGINS: 'ALL' }), {})
  // 其他取值不影响裁剪
  assert.notDeepEqual(resolvePluginOptions({ JM_BOT_WORKER_KEEP_PLUGINS: 'none' }), {})
})

test('禁用清单不含 settings（禁掉会导致客户端设置上报直接抛错）', () => {
  assert.equal(DISABLED_MELEE_PLUGINS.includes('settings'), false)
})
