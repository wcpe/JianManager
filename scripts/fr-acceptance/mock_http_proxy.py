#!/usr/bin/env python3
"""HTTP 正向代理模拟服务（FR-539 出站代理验收用）

它不转发到外网：只做三件事：
  1. 收到绝对 URI 形式的请求（HTTP 代理的标准形态），记录下来
  2. 证明「请求确实经过了代理」——通过注入响应头 X-Mock-Proxy: hit
  3. 直接返回预设结果（不真的去请求目标）

为何不去真转发：验收要回答的问题是「出站请求是否真的走代理」、
「代理模式 direct/env/custom 是否真的生效」，而不是「代理能不能上网」。
不真转发还能避免对外网产生依赖与泄漏。

用法：python3 mock_http_proxy.py 18182
"""
import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 18182
HITS = []


class Handler(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'

    def log_message(self, fmt, *args):
        sys.stderr.write('[proxy] %s\n' % (fmt % args))

    def _record(self, method):
        hit = {
            'method': method,
            'path': self.path,
            'host': self.headers.get('Host', ''),
            # 代理场景下 path 应是绝对 URI（http://host/...）
            'absolute_form': self.path.startswith('http://') or self.path.startswith('https://'),
            'has_auth': bool(self.headers.get('Proxy-Authorization')),
            'auth_scheme': (self.headers.get('Proxy-Authorization') or '').split(' ')[0],
        }
        HITS.append(hit)
        sys.stderr.write('[proxy] HIT %s\n' % json.dumps(hit, ensure_ascii=False))
        return hit

    def _respond_json(self, code, obj, extra_headers=None):
        body = json.dumps(obj, ensure_ascii=False).encode()
        self.send_response(code)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body)))
        # 关键：证明流量经过了代理
        self.send_header('X-Mock-Proxy', 'hit')
        for k, v in (extra_headers or {}).items():
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        self._record('GET')
        if '/models' in self.path or self.path.endswith('/v1/models'):
            return self._respond_json(200, {
                'object': 'list',
                'data': [{'id': 'proxy-seen-model', 'object': 'model'}],
            })
        # /__proxy_stats 用于测试断言「到底来了几次、长什么样」
        if '__proxy_stats' in self.path:
            return self._respond_json(200, {'hits': len(HITS), 'detail': HITS})
        return self._respond_json(200, {'ok': True, 'path': self.path})

    def do_POST(self):
        self._record('POST')
        n = int(self.headers.get('Content-Length') or 0)
        raw = self.rfile.read(n) if n else b'{}'
        # 回一个最小可用的 SSE，便于上游适配器解析
        self.send_response(200)
        self.send_header('Content-Type', 'text/event-stream')
        self.send_header('Cache-Control', 'no-cache')
        self.send_header('Connection', 'close')
        self.send_header('X-Mock-Proxy', 'hit')
        self.end_headers()
        chunk = ('data: ' + json.dumps({
            'id': 'chatcmpl-proxy', 'object': 'chat.completion.chunk',
            'model': 'proxy-seen-model',
            'choices': [{'index': 0, 'delta': {'content': 'via-proxy'}, 'finish_reason': None}],
        }) + '\n\n').encode()
        end = ('data: ' + json.dumps({
            'id': 'chatcmpl-proxy', 'object': 'chat.completion.chunk',
            'model': 'proxy-seen-model',
            'choices': [{'index': 0, 'delta': {}, 'finish_reason': 'stop'}],
            'usage': {'prompt_tokens': 1, 'completion_tokens': 1},
        }) + '\n\n').encode()
        self.wfile.write(chunk + end + b'data: [DONE]\n\n')
        self.wfile.flush()
        self.close_connection = True


if __name__ == '__main__':
    srv = ThreadingHTTPServer(('127.0.0.1', PORT), Handler)
    sys.stderr.write('mock http proxy listening on 127.0.0.1:%d\n' % PORT)
    srv.serve_forever()
