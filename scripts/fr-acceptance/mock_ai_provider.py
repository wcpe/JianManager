#!/usr/bin/env python3
"""AI 供应商模拟服务：同一进程仿真三种协议

支持的协议与端点：
  openai-chat      POST /v1/chat/completions
  openai-responses POST /v1/responses
  anthropic        POST /v1/messages
  所有协议           GET  /v1/models          （模型发现）

每个子路径都同时挂根路径与 /v1 前缀，因为管理员可能把 BaseURL 填成
http://host:port 或 http://host:port/v1 两种形态。

设计点（按最强语义）：
- SSE 严格遵守规范：event: / data: 字段，空行分帧，[DONE] 哨兵
- 错误路径也实现：401/429/500，用于验证「错误可归因」
- 用一个可寻址的模型名决定行为：名称含 err401/err429/err500 时返回对应错误
  这样不改服务就能测各错误分支
"""
import json
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 18080

# 对外声明的模型列表（模型发现端点返回）
MODELS = [
    'mock-gpt-primary',
    'mock-gpt-fast',
    'mock-claude-sonnet',
]


def model_err(model: str):
    """模型名里带错误标记时返回对应 HTTP 码，用于验证错误分类。"""
    for code in ('401', '429', '500', '503'):
        if 'err' + code in (model or ''):
            return int(code)
    return None


class Handler(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'

    def log_message(self, fmt, *args):
        sys.stderr.write('[mock] %s\n' % (fmt % args))

    # ---------- 工具 ----------
    def _json(self, code, obj):
        body = json.dumps(obj, ensure_ascii=False).encode()
        self.send_response(code)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _sse_start(self):
        self.send_response(200)
        self.send_header('Content-Type', 'text/event-stream')
        self.send_header('Cache-Control', 'no-cache')
        self.send_header('Connection', 'close')
        self.end_headers()

    def _sse_finish(self):
        """显式结束响应。

        HTTP/1.1 下不声明长度也不 close，客户端会一直等连接关闭——
        curl 实测会卡到 10s 超时。真供应商会主动关闭或发 [DONE] 后断开，
        这里必须跟。
        """
        try:
            self.wfile.flush()
        except Exception:
            pass
        self.close_connection = True

    def _sse(self, data, event=None):
        buf = b''
        if event:
            buf += b'event: ' + event.encode() + b'\n'
        # data 按规范可多行；这里单行即可
        buf += b'data: ' + (data if isinstance(data, bytes) else data.encode()) + b'\n\n'
        self.wfile.write(buf)
        self.wfile.flush()

    def _body(self):
        n = int(self.headers.get('Content-Length') or 0)
        raw = self.rfile.read(n) if n else b'{}'
        try:
            return json.loads(raw)
        except Exception:
            return {}

    def _path(self):
        p = self.path.split('?')[0]
        # 去掉可能的 /v1 前缀，统一路由
        if p.startswith('/v1/'):
            p = p[3:]
        return p

    # ---------- GET ----------
    def do_GET(self):
        p = self._path()
        if p == '/models':
            key = self.headers.get('x-api-key') or \
                  (self.headers.get('Authorization') or '').replace('Bearer ', '')
            if not key:
                # 无凭据：按 OpenAI / Anthropic 的常见形态返 401
                return self._json(401, {'error': {'message': 'missing api key', 'type': 'authentication_error'}})
            return self._json(200, {'object': 'list',
                                    'data': [{'id': m, 'object': 'model'} for m in MODELS]})
        return self._json(404, {'error': {'message': 'not found: ' + p}})

    # ---------- POST ----------
    def do_POST(self):
        p = self._path()
        body = self._body()
        model = body.get('model', '')

        code = model_err(model)
        if code == 401:
            return self._json(401, {'error': {'message': 'invalid api key', 'type': 'authentication_error'}})
        if code == 429:
            return self._json(429, {'error': {'message': 'rate limit exceeded', 'type': 'rate_limit_error'}})
        if code in (500, 503):
            return self._json(code, {'error': {'message': 'upstream unavailable', 'type': 'server_error'}})

        if p == '/chat/completions':
            return self._openai_chat(body)
        if p == '/responses':
            return self._openai_responses(body)
        if p == '/messages':
            return self._anthropic(body)
        return self._json(404, {'error': {'message': 'not found: ' + p}})

    # ---------- openai-chat ----------
    def _openai_chat(self, body):
        self._sse_start()
        model = body.get('model', 'mock')
        # 回显一段可断言的内容
        text = 'mock-openai-chat ok; model=%s' % model
        for piece in (text[:12], text[12:]):
            chunk = {'id': 'chatcmpl-mock', 'object': 'chat.completion.chunk',
                     'model': model,
                     'choices': [{'index': 0, 'delta': {'content': piece}, 'finish_reason': None}]}
            self._sse(json.dumps(chunk))
        # 结尾分片：finish_reason + usage
        end = {'id': 'chatcmpl-mock', 'object': 'chat.completion.chunk', 'model': model,
               'choices': [{'index': 0, 'delta': {}, 'finish_reason': 'stop'}],
               'usage': {'prompt_tokens': 11, 'completion_tokens': 7}}
        self._sse(json.dumps(end))
        self._sse('[DONE]')
        self._sse_finish()

    # ---------- openai-responses ----------
    def _openai_responses(self, body):
        self._sse_start()
        model = body.get('model', 'mock')
        text = 'mock-openai-responses ok; model=%s' % model
        seq = 0

        def ev(t, obj):
            nonlocal seq
            seq += 1
            obj['type'] = t
            obj['sequence_number'] = seq
            self._sse(json.dumps(obj), event=t)

        ev('response.created', {'response': {'id': 'resp_mock', 'status': 'in_progress'}})
        for piece in (text[:15], text[15:]):
            ev('response.output_text.delta',
               {'item_id': 'msg_mock', 'output_index': 0, 'content_index': 0, 'delta': piece})
        ev('response.completed',
           {'response': {'id': 'resp_mock', 'status': 'completed',
                         'usage': {'input_tokens': 11, 'output_tokens': 7}}})
        self._sse_finish()

    # ---------- anthropic ----------
    def _anthropic(self, body):
        self._sse_start()
        model = body.get('model', 'mock')
        text = 'mock-anthropic ok; model=%s' % model

        self._sse(json.dumps({'type': 'message_start',
                              'message': {'id': 'msg_mock', 'model': model,
                                          'usage': {'input_tokens': 11, 'output_tokens': 0}}}),
                  event='message_start')
        self._sse(json.dumps({'type': 'content_block_start', 'index': 0,
                              'content_block': {'type': 'text', 'text': ''}}),
                  event='content_block_start')
        for piece in (text[:15], text[15:]):
            self._sse(json.dumps({'type': 'content_block_delta', 'index': 0,
                                  'delta': {'type': 'text_delta', 'text': piece}}),
                      event='content_block_delta')
        self._sse(json.dumps({'type': 'content_block_stop', 'index': 0}),
                  event='content_block_stop')
        self._sse(json.dumps({'type': 'message_delta',
                              'delta': {'stop_reason': 'end_turn'},
                              'usage': {'output_tokens': 7}}),
                  event='message_delta')
        self._sse(json.dumps({'type': 'message_stop'}), event='message_stop')
        self._sse_finish()


if __name__ == '__main__':
    port = PORT
    use_tls = False
    cert = key = None
    # 用法: mock_ai_provider.py [port] [cert.pem key.pem]
    #
    # 为何需要 TLS：平台的受保护出站**刻意不提供 InsecureSkipVerify**
    # （internal/platform/httpclient/protected.go），明文 http 也被拒。
    # 要真实走一遍出站链路，模拟服务必须能吐 TLS。
    if len(sys.argv) >= 4:
        cert, key = sys.argv[2], sys.argv[3]
        use_tls = True

    srv = ThreadingHTTPServer(('127.0.0.1', port), Handler)
    if use_tls:
        import ssl
        ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        ctx.load_cert_chain(certfile=cert, keyfile=key)
        srv.socket = ctx.wrap_socket(srv.socket, server_side=True)
        sys.stderr.write('mock ai provider listening on https://127.0.0.1:%d\n' % port)
    else:
        sys.stderr.write('mock ai provider listening on http://127.0.0.1:%d\n' % port)
    sys.stderr.write('models: %s\n' % ', '.join(MODELS))
    srv.serve_forever()
