#!/usr/bin/env python3
"""
PR#71 FR 真机验收执行器（统一口径）

设计目标：后续 8 个批全部复用本文件，保证裁定口径一致。

裁定四维（最强语义）：
  capability  最强能力 —— 边界/异常/并发/失败回退是否覆盖
  perf        最强性能 —— 有无量化读数、经不经得起压力
  modern      最现代化 —— 有无已被淘汰的范式
  semantics   最强语义 —— 错误是否诚实可归因（不把失败报成功）

四档结论：
  PASS      达标
  COND      有条件达标（附限制说明）
  FAIL      不达标
  BLOCKED   无法验收（环境所限，非功能缺陷）

用法：
  python3 fr_acceptance.py probe          # 环境探活
  python3 fr_acceptance.py baseline       # 采集基线快照
  python3 fr_acceptance.py route-check    # 校验所有路由真实存在性
"""
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timezone

BASE = os.environ.get('JM_BASE', 'http://127.0.0.1:8080')
TOKEN_FILE = '/tmp/jm_token'
EVIDENCE_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), 'evidence')


# ---------- 认证 ----------
def login(user='admin', password='adminadmin'):
    payload = json.dumps({'username': user, 'password': password}).encode()
    req = urllib.request.Request(
        BASE + '/api/v1/auth/login', data=payload,
        headers={'Content-Type': 'application/json'}, method='POST')
    with urllib.request.urlopen(req, timeout=10) as r:
        d = json.load(r)
    tok = d['accessToken']
    with open(TOKEN_FILE, 'w') as f:
        f.write(tok)
    return tok


def token():
    if not os.path.exists(TOKEN_FILE):
        return login()
    t = open(TOKEN_FILE).read().strip()
    return t or login()


# ---------- HTTP ----------

def api(path, method='GET', body=None, timeout=25, raw=False):
    """返回 (status, parsed_or_text)。status=None 表示连接层失败（与 4xx/5xx 区别开）。

    token 过期（401）时自动重新登录并重试一次：验收会跑很久，
    token 必然在中途过期，不能因此把功能误判成「鉴权坏了」。
    """
    st, val = _api_once(path, method, body, timeout, raw)
    if st == 401:
        login()
        st, val = _api_once(path, method, body, timeout, raw)
    return st, val


def _api_once(path, method, body, timeout, raw):
    url = BASE + path
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(
        url, data=data, method=method,
        headers={'Authorization': 'Bearer ' + token(),
                 'Content-Type': 'application/json'})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            b = r.read()
            if raw:
                return r.status, b
            try:
                return r.status, json.loads(b)
            except Exception:
                return r.status, b.decode('utf-8', 'replace')
    except urllib.error.HTTPError as e:
        b = e.read()
        try:
            return e.code, json.loads(b)
        except Exception:
            return e.code, b.decode('utf-8', 'replace')[:400]
    except Exception as e:
        # 连接失败与 HTTP 错误码必须区分：前者是环境问题，后者是接口问题
        return None, 'CONN_ERROR: %s' % str(e)[:160]


# ---------- 裁定记录 ----------
class Verdict:
    def __init__(self, fr, title):
        self.fr = fr
        self.title = title
        self.result = None      # PASS / COND / FAIL / BLOCKED
        self.dims = {}          # capability/perf/modern/semantics -> (ok, note)
        self.evidence = []      # 支撑证据（命令输出、读数）
        self.note = ''

    def dim(self, name, ok, note=''):
        self.dims[name] = (ok, note)
        return self

    def ev(self, text):
        self.evidence.append(text)
        return self

    def judge(self, result, note=''):
        self.result = result
        self.note = note
        return self

    def as_dict(self):
        return {
            'fr': self.fr, 'title': self.title, 'result': self.result,
            'dims': {k: {'ok': v[0], 'note': v[1]} for k, v in self.dims.items()},
            'evidence': self.evidence, 'note': self.note,
        }

    def show(self):
        mark = {'PASS': '✅', 'COND': '⚠️', 'FAIL': '❌', 'BLOCKED': '🚫'}.get(self.result, '?')
        print('%s %-9s %s' % (mark, self.fr, self.title))
        for k, (ok, note) in self.dims.items():
            if note or not ok:
                print('     %-11s %s %s' % (k, 'ok' if ok else 'NO', note))
        if self.note:
            print('     -> %s' % self.note)


def save_report(rows, name):
    os.makedirs(EVIDENCE_DIR, exist_ok=True)
    p = os.path.join(EVIDENCE_DIR, name)
    with open(p, 'w', encoding='utf-8') as f:
        json.dump(rows, f, ensure_ascii=False, indent=2)
    return p


# ---------- 子命令 ----------
def cmd_probe():
    print('=== 环境探活 ===')
    checks = [
        ('CP /health',      '/health'),
        ('登录',            '/api/v1/auth/me'),
        ('节点',            '/api/v1/nodes'),
        ('实例',            '/api/v1/instances?pageSize=200'),
        ('日志联邦门面',    '/api/v1/logs/federation?limit=1'),
        ('日志 search',     '/api/v1/logs/federation/search?limit=1'),
        ('告警规则',        '/api/v1/alerts/rules'),
        ('任务',            '/api/v1/tasks?pageSize=1'),
        ('审批',            '/api/v1/approvals?pageSize=1'),
    ]
    ok = 0
    for name, path in checks:
        st, _ = api(path)
        flag = 'ok' if st and 200 <= st < 300 else 'NO'
        if flag == 'ok':
            ok += 1
        print('  %-14s %-6s %s' % (name, st, flag))
    print('\n%d/%d 可用' % (ok, len(checks)))
    return 0 if ok == len(checks) else 1


def cmd_baseline():
    print('=== 基线快照 ===')
    snap = {'ts': datetime.now(timezone.utc).isoformat(), 'items': {}}

    st, insts = api('/api/v1/instances?pageSize=200')
    items = insts if isinstance(insts, list) else []
    from collections import Counter
    snap['items']['instances'] = {
        'total': len(items),
        'by_status': dict(Counter(i.get('status', '?') for i in items)),
        'detail': [{'name': i.get('name'), 'status': i.get('status'),
                    'uuid': i.get('uuid'), 'serverPort': i.get('serverPort'),
                    'probePort': i.get('probePort')} for i in items],
    }
    print('  实例 %d 台，状态 %s' % (len(items), snap['items']['instances']['by_status']))

    st, nodes = api('/api/v1/nodes')
    nodes = nodes if isinstance(nodes, list) else []
    snap['items']['nodes'] = {'total': len(nodes),
                              'detail': [{'name': n.get('name'), 'status': n.get('status'),
                                          'host': n.get('host')} for n in nodes]}
    print('  节点 %d 个' % len(nodes))

    st, fed = api('/api/v1/logs/federation?limit=1')
    cov = fed.get('coverage', {}) if isinstance(fed, dict) else {}
    snap['items']['log_coverage'] = cov
    print('  日志覆盖 complete=%s' % cov.get('complete'))

    p = save_report(snap, 'baseline.json')
    print('\n已保存: %s' % p)
    return 0


def cmd_route_check():
    """逐个验证路由真实存在（避免把 404 误判为功能缺失）。"""
    routes = [
        '/api/v1/logs/federation',
        '/api/v1/logs/federation/search?limit=1',
        '/api/v1/logs/federation/stats',
        '/api/v1/logs/federation/fields',
        '/api/v1/logs/federation/facets?dimensions=level',
        '/api/v1/logs/federation/streams',
        '/api/v1/logs/federation/tail',
        '/api/v1/alerts/events?pageSize=1',
        '/api/v1/alerts/rules',
        '/api/v1/alerts/channels',
        '/api/v1/assistant/admin/providers',
        '/api/v1/assistant/conversations',
        '/api/v1/config-files',
        '/api/v1/config-baselines',
        '/api/v1/audit?pageSize=1',
        '/api/v1/tasks?pageSize=1',
        '/api/v1/approvals?pageSize=1',
        '/api/v1/agent/tokens',
    ]
    print('=== 路由真实性校验 ===')
    bad = []
    for r in routes:
        st, body = api(r)
        good = st is not None and 200 <= st < 300
        print('  %-42s %-6s %s' % (r, st, 'ok' if good else 'NO'))
        if not good:
            bad.append((r, st, str(body)[:100]))
    if bad:
        print('\n不存在的路由 %d 个（注意：可能是 404=路径错，不是功能缺失）：' % len(bad))
        for r, st, b in bad:
            print('  %s -> %s %s' % (r, st, b))
    return 0 if not bad else 1


if __name__ == '__main__':
    cmd = sys.argv[1] if len(sys.argv) > 1 else 'probe'
    fn = {'probe': cmd_probe, 'baseline': cmd_baseline, 'route-check': cmd_route_check}.get(cmd)
    if not fn:
        print('未知子命令: %s' % cmd)
        sys.exit(2)
    sys.exit(fn())
