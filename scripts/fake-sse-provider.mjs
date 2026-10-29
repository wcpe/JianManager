#!/usr/bin/env node
/**
 * 本地 OpenAI 兼容假供应商（SSE 流式），用于验收 AI 助手真实链路。
 *
 * 为什么需要它：FR-515 的运行时已实现，但一直缺「真实供应商」验证（缺凭据）。
 * 这个脚本提供一个**真的走 HTTP + 真 SSE 分帧**的上游，让 CP 的受保护出站、SSE 解析、
 * 事件落库、前端增量渲染全部按生产路径跑一遍——而不是只靠 devmock 里那段回放。
 *
 * 覆盖的协议面（2026-10-29 起三协议齐备）：
 *  - POST /v1/chat/completions （openai-chat）逐帧 `data: {...}` + `data: [DONE]`
 *  - POST /v1/responses        （openai-responses）`response.output_text.delta` 等带类型事件
 *  - POST /v1/messages         （anthropic）`message_start` → `content_block_delta` → `message_stop`
 *  - 文本增量按**多帧**下发（验证逐字渲染，而非一次性整段）
 *  - 末尾带 usage：chat 用「choices 为空的一帧」，responses 用 `response.completed`，
 *    anthropic 用 `message_delta` 里的 `message.usage`（三处帧位置各不相同，弄错就变成「上游未报用量」）
 *  - 工具调用：设 `FAKE_PROBE_TOOL=<工具名>` 且请求里下发了该工具，就如实回一次调用
 *  - 随机回复内容与随机分片数：避免「只有一种顺序」掩盖时序缺陷
 *
 * 刻意**不**做的事：不假装是一个真实模型。回复里带「本地假供应商」字样，
 * 免得有人看到截图以为接了真模型。
 *
 * 用法：
 *   FAKE_PROBE_TOOL=assistant_probe_ping node scripts/fake-sse-provider.mjs --port 5458
 *
 *   # 另一终端，对三种协议各跑一次（PROTOCOL 换 openai-chat / openai-responses / anthropic）
 *   JIANMANAGER_ASSISTANT_LIVE_PROTOCOL=openai-chat \
 *   JIANMANAGER_ASSISTANT_LIVE_BASE_URL=http://127.0.0.1:5458/v1 \
 *   JIANMANAGER_ASSISTANT_LIVE_MODEL=fake-sse-1 \
 *   JIANMANAGER_ASSISTANT_LIVE_KEY=fake-key \
 *   JIANMANAGER_ASSISTANT_LIVE_TOOLS=1 \
 *   JIANMANAGER_ASSISTANT_LIVE_PRIVATE=1 \
 *   go test ./internal/controlplane/service/ -run 'TestAssistantLive' -v -count=1
 *
 * 注意：本地跑绿只能证明「我们说对了协议」，**不等于**任何商业厂商已验证。
 */

import { createServer } from 'node:http'

const argOf = (name, dflt) => {
  const i = process.argv.indexOf(name)
  return i >= 0 ? Number(process.argv[i + 1]) : dflt
}
const PORT = (() => {
  const p = argOf('--port', 5399)
  return Number.isFinite(p) ? p : 5399
})()
/**
 * 帧间间隔（毫秒）。验收时调大写（如 400ms），才能用探针看出「帧是陆续到达的」——
 * 默认 30ms 时整个回复 200ms 内跑完，观察到的都是补读回放，证明不了流式。
 */
const FRAME_DELAY = (() => {
  const d = argOf('--delay', 30)
  return Number.isFinite(d) && d >= 0 ? d : 30
})()

/** 随机回复池：都围绕运维语境，方便在界面上看出「这是一轮真实问答」。 */
const REPLIES = [
  '（本地假供应商）当前没有发现异常退出的实例；最近 6 小时内所有实例的退出码都是 0。',
  '（本地假供应商）我看到 3 个未解决的告警，最高等级是 warning，集中在 node-2 的内存使用上。',
  '（本地假供应商）最近 5 个任务里有 1 个失败：安装便携运行时下载校验失败（sha256 不匹配）。',
  '（本地假供应商）集群水位看起来平稳：节点在线 2/2，运行实例 242，CPU 均值 47%。',
  '（本地假供应商）建议先看 server-0009 的最近日志，它在 24 小时内有 4 次重启记录。',
]

function pickReply() {
  return REPLIES[Math.floor(Math.random() * REPLIES.length)]
}

/**
 * 把一段文本切成随机片数。
 *
 * 故意随机：固定切法会让「分片重组的边界错误」在测试里恰好不出现。
 * 片数取 3~6，且保证每片非空（空增量帧没有信息量，还会被前端当成一次无变化的 setState）。
 */
function chunkText(text) {
  const parts = 3 + Math.floor(Math.random() * 4)
  const size = Math.max(1, Math.ceil(text.length / parts))
  const out = []
  for (let i = 0; i < text.length; i += size) out.push(text.slice(i, i + size))
  return out.filter((s) => s !== '')
}

function sseHead(res) {
  res.writeHead(200, {
    'Content-Type': 'text/event-stream; charset=utf-8',
    'Cache-Control': 'no-cache, no-transform',
    Connection: 'keep-alive',
    // 关掉代理缓冲：否则 nginx 类中间层会把增量攒成一大坨，测不出流式。
    'X-Accel-Buffering': 'no',
  })
}

function frame(obj) {
  return `data: ${JSON.stringify(obj)}\n\n`
}

/** 一帧增量（只带 role 的第一帧与只带 content 的后续帧，与真实供应商一致）。 */
function deltaFrame(content, extra = {}) {
  return frame({
    id: 'chatcmpl-fake',
    object: 'chat.completion.chunk',
    created: Math.floor(Date.now() / 1000),
    model: 'fake-sse-1',
    choices: [{ index: 0, delta: { content }, finish_reason: null }],
    ...extra,
  })
}

/** 读完并丢弃请求体：不读会让 keep-alive 连接状态错乱。 */
function drainBody(req) {
  return new Promise((resolve) => {
    const chunks = []
    req.on('data', (c) => chunks.push(c))
    req.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')))
    req.on('error', () => resolve(''))
  })
}

const server = createServer(async (req, res) => {
  const url = new URL(req.url ?? '/', `http://127.0.0.1:${PORT}`)

  // 探活：配置供应商时面板/后端会试连通性。
  if (req.method === 'GET' && (url.pathname === '/healthz' || url.pathname === '/v1/models')) {
    // /v1/models 是 OpenAI 兼容面，真实供应商也有；返回一个模型以支持「列模型」探测。
    res.writeHead(200, { 'Content-Type': 'application/json' })
    res.end(JSON.stringify({ data: [{ id: 'fake-sse-1', object: 'model' }] }))
    return
  }

  // ── OpenAI Responses 协议：POST {base}/responses ────────────────────
  // 与 Chat 的关键差异：响应是一串**带类型的事件**，文本增量走
  // response.output_text.delta，结束走 response.completed。
  if (req.method === 'POST' && url.pathname.endsWith('/responses')) {
    const raw = await drainBody(req)
    let body = {}
    try { body = JSON.parse(raw) } catch { body = {} }
    sseHead(res)
    const chunks = chunkText(pickReply())
    const itemId = `msg_fake_${Math.random().toString(36).slice(2, 10)}`
    const tick = () => new Promise((r) => setTimeout(r, FRAME_DELAY))

    // 工具模式：请求里声明了 FAKE_PROBE_TOOL 指定的工具，就如实回一次 function_call。
    // Responses 的工具声明是**扁平**结构（type:function + name 顶层），不是嵌套 function。
    const PROBE_TOOL = process.env.FAKE_PROBE_TOOL || ''
    const offered = PROBE_TOOL && (body?.tools ?? []).some((t) => t?.name === PROBE_TOOL)

    if (offered) {
      const callId = `call_fake_${Math.random().toString(36).slice(2, 10)}`
      const fnItemId = `fc_fake_${Math.random().toString(36).slice(2, 10)}`
      const args = process.env.FAKE_PROBE_ARGS || '{"nonce":"probe"}'
      res.write(frame({ type: 'response.created' }))
      await tick()
      // 输出项起始：带 name 与 call_id，客户端据此建 ToolCall。
      res.write(frame({
        type: 'response.output_item.added',
        item: { type: 'function_call', id: fnItemId, name: PROBE_TOOL, call_id: callId },
      }))
      await tick()
      res.write(frame({
        type: 'response.function_call_arguments.delta', item_id: fnItemId, delta: args,
      }))
      await tick()
      res.write(frame({
        type: 'response.function_call_arguments.done', item_id: fnItemId, arguments: args,
      }))
      await tick()
      res.write(frame({
        type: 'response.completed',
        response: { status: 'completed', usage: { input_tokens: 42, output_tokens: 18 } },
      }))
      res.write('data: [DONE]\n\n')
      res.end()
      return
    }

    res.write(frame({ type: 'response.created' }))
    await tick()
    res.write(frame({ type: 'response.output_item.added', item: { type: 'message', id: itemId } }))
    await tick()

    for (const c of chunks) {
      if (res.writableEnded || res.destroyed) return
      res.write(frame({ type: 'response.output_text.delta', item_id: itemId, delta: c }))
      await tick()
    }

    res.write(frame({ type: 'response.output_text.done', item_id: itemId }))
    await tick()
    res.write(frame({
      type: 'response.completed',
      response: {
        status: 'completed',
        usage: {
          input_tokens: 42 + Math.floor(Math.random() * 10),
          output_tokens: 64 + Math.floor(Math.random() * 20),
        },
      },
    }))
    res.write('data: [DONE]\n\n')
    res.end()
    return
  }

  // ── Anthropic Messages 协议：POST {base}/messages ───────────────────
  // 事件序列：message_start → content_block_start → content_block_delta×N
  //          → content_block_stop → message_delta → message_stop
  if (req.method === 'POST' && url.pathname.endsWith('/messages')) {
    const raw = await drainBody(req)
    let body = {}
    try { body = JSON.parse(raw) } catch { body = {} }
    sseHead(res)
    const chunks = chunkText(pickReply())
    const tick = () => new Promise((r) => setTimeout(r, FRAME_DELAY))

    // 工具模式：Anthropic 的 tool 是 {name, description, input_schema}（与 Responses 不同）。
    const PROBE_TOOL = process.env.FAKE_PROBE_TOOL || ''
    const offered = PROBE_TOOL && (body?.tools ?? []).some((t) => t?.name === PROBE_TOOL)

    res.write(frame({
      type: 'message_start',
      message: { id: 'msg_fake', role: 'assistant', content: [], usage: { input_tokens: 42, output_tokens: 0 } },
    }))
    await tick()

    if (offered) {
      const toolUseId = `toolu_fake_${Math.random().toString(36).slice(2, 10)}`
      const args = JSON.parse(process.env.FAKE_PROBE_ARGS || '{"nonce":"probe"}')
      // tool_use 块：客户端从 content_block_start 建 ToolCall（CallID=block.id, Name=block.name），
      // 参数从 input_json_delta 累加。
      res.write(frame({
        type: 'content_block_start', index: 0,
        content_block: { type: 'tool_use', id: toolUseId, name: PROBE_TOOL, input: {} },
      }))
      await tick()
      res.write(frame({
        type: 'content_block_delta', index: 0,
        delta: { type: 'input_json_delta', partial_json: JSON.stringify(args) },
      }))
      await tick()
      res.write(frame({ type: 'content_block_stop', index: 0 }))
      res.write(frame({
        type: 'message_delta',
        // 客户端从 `message` 里读 stop_reason 与 usage（不是事件顶层），
        // 且 usage 两个字段都要给才算「有用量」。
        message: { stop_reason: 'tool_use', usage: { input_tokens: 42, output_tokens: 18 } },
      }))
      res.write(frame({ type: 'message_stop' }))
      res.end()
      return
    }

    res.write(frame({ type: 'content_block_start', index: 0, content_block: { type: 'text', text: '' } }))
    await tick()

    for (const c of chunks) {
      if (res.writableEnded || res.destroyed) return
      res.write(frame({ type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: c } }))
      await tick()
    }

    res.write(frame({ type: 'content_block_stop', index: 0 }))
    res.write(frame({
      type: 'message_delta',
      message: { stop_reason: 'end_turn', usage: { input_tokens: 42, output_tokens: 64 + Math.floor(Math.random() * 20) } },
    }))
    res.write(frame({ type: 'message_stop' }))
    res.end()
    return
  }

  if (req.method !== 'POST' || !url.pathname.endsWith('/chat/completions')) {
    res.writeHead(404, { 'Content-Type': 'application/json' })
    res.end(JSON.stringify({ error: { message: 'not found', type: 'invalid_request_error' } }))
    return
  }

  // 读完请求体（流式请求不需要它，但不读会让 keep-alive 连接状态错乱）。
  const body = await new Promise((resolve) => {
    const chunks = []
    req.on('data', (c) => chunks.push(c))
    req.on('end', () => {
      try {
        resolve(JSON.parse(Buffer.concat(chunks).toString('utf8') || '{}'))
      } catch {
        resolve({})
      }
    })
  })

  const wantStream = body?.stream === true
  const reply = pickReply()

  // 非流式分支：真实供应商也支持，顺手覆盖（便于用 curl 快速自检）。
  if (!wantStream) {
    res.writeHead(200, { 'Content-Type': 'application/json' })
    res.end(
      JSON.stringify({
        id: 'chatcmpl-fake',
        object: 'chat.completion',
        created: Math.floor(Date.now() / 1000),
        model: 'fake-sse-1',
        choices: [{ index: 0, message: { role: 'assistant', content: reply }, finish_reason: 'stop' }],
        usage: { prompt_tokens: 42, completion_tokens: 64, total_tokens: 106 },
      }),
    )
    return
  }

  sseHead(res)

  /** 让出事件循环，保证每个 write 都真的成为一个独立 TCP 段（这才叫流式）。 */
  const tick = () => new Promise((r) => setTimeout(r, FRAME_DELAY))

  // 工具调用分支（FR-516 验收）。
  //
  // 触发方式：最后一条 user 消息里含 `PLEASE_CALL <tool> <json-args>`。
  // 走**消息内容**而不是 querystring，是因为这个供应商要由 CP 按配置里的 baseUrl 调用，
  // 无法逐次改 URL；把意图写进消息才能让「模型决定调用哪个工具」这件事真的发生
  // ——否则就成了绕过模型直接调执行器，测不到「工具目录下发给模型」这一步。
  // 只认**最后一条** user 消息：工具结果回灌后，历史里仍留着最初那条 PLEASE_CALL，
  // 若在全部历史里匹配，模型每一轮都会重复调用同一个工具，直到耗尽预算。
  const lastUser = [...(body?.messages ?? [])].reverse().find((m) => m?.role === 'user')
  const text = typeof lastUser?.content === 'string' ? lastUser.content : ''
  const marker = text.match(/PLEASE_CALL\s+(\S+)\s*(\{[\s\S]*\})?/)
  let toolName = url.searchParams.get('tool')
  let callArgs = url.searchParams.get('args') ?? '{}'
  if (marker) {
    toolName = marker[1]
    callArgs = marker[2] ?? '{}'
  }

  /**
   * 探测工具开关（默认关闭）。
   *
   * CP 的「模型测试」用固定提示词，且 baseUrl 在配置阶段就被静态校验过（不许带 query），
   * 因此没法给单次请求塞 ?tool=，提示词里也不能写 PLEASE_CALL ——那是本地假上游的
   * 私有约定，不该出现在生产代码里。所以这里给一个进程级开关：
   * 只要本次请求下发了这个工具，就如实回一次调用。
   *
   * 语义上仍然诚实：这个假模型「被要求调用哪个就调用哪个」，与真实模型遵循 schema 等价。
   * 不设该变量时行为完全不变（下面两条分支都不进入）。
   */
  const PROBE_TOOL = process.env.FAKE_PROBE_TOOL || ''
  if (PROBE_TOOL) {
    const offered = (body?.tools ?? []).some((t) => t?.function?.name === PROBE_TOOL)
    if (offered) {
      toolName = PROBE_TOOL
      callArgs = process.env.FAKE_PROBE_ARGS || '{"nonce":"probe"}'
    }
  }

  // **只调用一次**：若历史里已经出现过发给该工具的 tool 消息，说明这一轮的工具往返
  // 已经发生过，本次请求是「结果回灌后」的收尾请求 —— 必须回文本结束，
  // 否则会无限重复调用同一工具直到耗尽预算，掩盖掉后面真正要验的行为。
  if (process.env.FAKE_DEBUG) {
    console.error('[req] tools=%d msgs=%s', (body?.tools ?? []).length,
      JSON.stringify((body?.messages ?? []).map((m) => m?.role)))
  }
  const alreadyCalled = (body?.messages ?? []).some(
    (m) => m?.role === 'tool' && (m?.name === toolName || m?.tool_call_id),
  )

  const multiMarker = text.match(/PLEASE_CALL_MULTI\s+(.+)$/)
  if (!alreadyCalled && multiMarker && Array.isArray(body?.tools) && body.tools.length > 0) {
    const parts = [...multiMarker[1].matchAll(/(\S+)\s*(\{[^}]*\})/g)]
    res.write(frame({ id: 'chatcmpl-fake', object: 'chat.completion.chunk',
      created: Math.floor(Date.now() / 1000), model: 'fake-sse-1',
      choices: [{ index: 0, delta: { role: 'assistant' }, finish_reason: null }] }))
    await tick()
    const calls = parts.map((m, i) => ({
      index: i, id: 'call_multi_' + i, type: 'function',
      function: { name: m[1], arguments: m[2] },
    }))
    res.write(frame({ id: 'chatcmpl-fake', object: 'chat.completion.chunk',
      created: Math.floor(Date.now() / 1000), model: 'fake-sse-1',
      choices: [{ index: 0, delta: { tool_calls: calls }, finish_reason: null }] }))
    await tick()
    res.write(frame({ id: 'chatcmpl-fake', object: 'chat.completion.chunk',
      created: Math.floor(Date.now() / 1000), model: 'fake-sse-1',
      choices: [{ index: 0, delta: {}, finish_reason: 'tool_calls' }] }))
    res.write('data: [DONE]\n\n')
    res.end()
    return
  }

  if (!alreadyCalled && toolName && Array.isArray(body?.tools) && body.tools.length > 0) {
    const known = body.tools.some((t) => t?.function?.name === toolName)
    const callId = 'call_fake_' + Math.random().toString(36).slice(2, 10)
    // 首帧 role
    res.write(
      frame({
        id: 'chatcmpl-fake', object: 'chat.completion.chunk',
        created: Math.floor(Date.now() / 1000), model: 'fake-sse-1',
        choices: [{ index: 0, delta: { role: 'assistant' }, finish_reason: null }],
      }),
    )
    await tick()
    if (!known) {
      // 请求的工具不在目录里 → 如实说明，让验收能看见「目录没下发这个工具」，
      // 而不是伪造一次调用后假装成功。
      const names = body.tools.map((t) => t?.function?.name).filter(Boolean)
      res.write(
        frame({
          id: 'chatcmpl-fake', object: 'chat.completion.chunk',
          created: Math.floor(Date.now() / 1000), model: 'fake-sse-1',
          choices: [{ index: 0, delta: { content: `工具 ${toolName} 不在下发的目录里；目录=${names.join(',')}` }, finish_reason: null }],
        }),
      )
      res.write(
        frame({
          id: 'chatcmpl-fake', object: 'chat.completion.chunk',
          created: Math.floor(Date.now() / 1000), model: 'fake-sse-1',
          choices: [{ index: 0, delta: {}, finish_reason: 'stop' }],
        }),
      )
      res.write('data: [DONE]\n\n')
      res.end()
      return
    }
    res.write(
      frame({
        id: 'chatcmpl-fake', object: 'chat.completion.chunk',
        created: Math.floor(Date.now() / 1000), model: 'fake-sse-1',
        choices: [{
          index: 0,
          delta: {
            tool_calls: [{
              index: 0, id: callId, type: 'function',
              function: { name: toolName, arguments: callArgs },
            }],
          },
          finish_reason: null,
        }],
      }),
    )
    await tick()
    res.write(
      frame({
        id: 'chatcmpl-fake', object: 'chat.completion.chunk',
        created: Math.floor(Date.now() / 1000), model: 'fake-sse-1',
        choices: [{ index: 0, delta: {}, finish_reason: 'tool_calls' }],
      }),
    )
    // 工具分支也要带 usage（与真实上游一致：最后一帧 choices 为空时给用量），
    // 否则客户端会以为上游没报用量，用量解析路径得不到验证。
    res.write(frame({
      id: 'chatcmpl-fake', object: 'chat.completion.chunk',
      created: Math.floor(Date.now() / 1000), model: 'fake-sse-1',
      choices: [],
      usage: { prompt_tokens: 42, completion_tokens: 18, total_tokens: 60 },
    }))
    res.write('data: [DONE]\n\n')
    res.end()
    return
  }

  const chunks = chunkText(reply)
  // 首帧只给 role：与真实供应商一致，避免前端把 role 帧当成空内容渲染。
  res.write(frame({
    id: 'chatcmpl-fake',
    object: 'chat.completion.chunk',
    created: Math.floor(Date.now() / 1000),
    model: 'fake-sse-1',
    choices: [{ index: 0, delta: { role: 'assistant' }, finish_reason: null }],
  }))
  await tick()

  for (const c of chunks) {
    // 客户端断开就停：否则一个断开的 run 会让脚本继续往已关闭的 socket 写。
    if (res.writableEnded || res.destroyed) return
    res.write(deltaFrame(c))
    await tick()
  }

  // usage 帧：与 `stream_options.include_usage` 的语义一致（choices 为空数组）。
  res.write(frame({
    id: 'chatcmpl-fake',
    object: 'chat.completion.chunk',
    created: Math.floor(Date.now() / 1000),
    model: 'fake-sse-1',
    choices: [],
    usage: {
      prompt_tokens: 42 + Math.floor(Math.random() * 10),
      completion_tokens: 64 + Math.floor(Math.random() * 20),
      total_tokens: 106,
    },
  }))

  // 终帧：带 finish_reason 的最后一帧 choice。
  res.write(frame({
    id: 'chatcmpl-fake',
    object: 'chat.completion.chunk',
    created: Math.floor(Date.now() / 1000),
    model: 'fake-sse-1',
    choices: [{ index: 0, delta: {}, finish_reason: 'stop' }],
  }))
  res.write('data: [DONE]\n\n')
  res.end()
})

server.listen(PORT, '127.0.0.1', () => {
  console.log(`[fake-provider] listening on http://127.0.0.1:${PORT}  (POST /v1/chat/completions, frame delay ${FRAME_DELAY}ms)`)
})
