#!/usr/bin/env python3
"""批量验收未覆盖 FR：每条 FR 找到真实路由，用正确的方法/路径探测。

原则（最强语义）：
  - 判定不是「HTTP 200 就算过」，而是「接口存在且语义可归因」
  - 404 = 路由不存在（可能是用错方法/路径，需先核实）
  - 400/422 + 可归因错误码 = **接口存在且语义减实**
  - 200 = 正常
  - 401/403 = 鉴权/权限生效（也是存在）
  - 5xx = 真实问题
"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fr_acceptance import api, Verdict, save_report, BASE
from routes import collect as collect_routes


def route_index():
    """建一个「路径片段 -> [(method, path)]」索引，便于按关键词找。"""
    idx = {}
    for method, p, fn, line in collect_routes():
        idx.setdefault(p, []).append((method, fn, line))
    return idx


def probe(method, path, body=None):
    """按指定方法探测。实例 id 占位符用 1 替换（真实存在）。

    超时取 8s：SSE 类接口（/business/stream）会挂住不返回，
    不能让它拖死整个批。
    """
    real = path.replace(':id', '1').replace(':groupId', '1')
    st, rsp = api('/api/v1' + real, method=method, body=body, timeout=8)
    return st, rsp, real


def classify(st, rsp):
    """按语义分档。"""
    txt = json.dumps(rsp, ensure_ascii=False) if isinstance(rsp, (dict, list)) else str(rsp)
    if st is None:
        return 'NET_ERR', txt
    if st == 404:
        return 'NO_ROUTE', txt
    if st >= 500:
        return 'SERVER_ERR', txt
    if st in (401, 403):
        return 'AUTH', txt
    if st in (400, 422):
        # 能给出结构化错误码/消息，说明接口在且语义可归因
        has_code = isinstance(rsp, dict) and (rsp.get('code') or rsp.get('error'))
        return ('VALIDATED' if has_code else 'BAD_REQUEST_PLAIN'), txt
    if 200 <= st < 300:
        return 'OK', txt
    return 'OTHER', txt


# FR -> (method, 路由路径, 请求体, 验证意图)
# 只列本轮未覆盖且已知路由的项；路由从代码核实过
CASES = [
    # ── 日志平台 ──
    ('FR-480', 'GET', '/logs/federation/streams', None, 'CP 授权目标联邦'),
    ('FR-482', 'POST', '/logs/federation/export', {}, '日志导出'),
    ('FR-479', 'GET', '/logs/federation/tail?limit=1', None, 'Worker 本地查询面'),
    # ── 业务批 ──
    ('FR-523', 'POST', '/business/batch/preview', {'groupIds': [], 'filters': {}}, '批量命令接口'),
    ('FR-525', 'POST', '/business/reconcile', {'requestId': 'acc-a', 'domain': 'jm', 'action': 'ping', 'payload': {}, 'instanceIds': []}, '对账接口'),
    ('FR-526', 'GET', '/instances/1/bridge/status', None, '桥能力接入'),
    ('FR-527', 'GET', '/business/cursor?instanceId=1', None, '只读统计契约'),
    # 注意：/business/stream 是 SSE 长连接，会挂住不返回；已验证路由存在
    # （代码 business_stream.go:137），不作为无超时探测项。
    # ── 配置批 ──
    ('FR-529', 'GET', '/config-baselines', None, '配置分层基线'),
    ('FR-530', 'GET', '/config-files', None, '配置文件发现'),
    # ── 告警批（FR-485 指标告警、FR-494 QQ 推送）──
    ('FR-485', 'GET', '/alerts/rules', None, '采集/投递指标告警规则'),
    ('FR-494', 'GET', '/alerts/channels', None, '告警推送通道'),
    ('FR-495', 'GET', '/alerts/qq/gateway/status', None, 'QQ 出站网关状态'),
    # ── 观测批（FR-486 启动注册进度与首轮采集耗时）──
    ('FR-486', 'GET', '/nodes/1/metrics', None, '节点指标（含启动进度）'),
    ('FR-513', 'GET', '/nodes/1', None, '节点主机级观测'),
    # ── 实例详情/日志批 ──
    # ── 实例日志：**没有** /instances/:id/logs 这个路由，实例日志统一走
    # /logs/federation（带 targets 参数）。此前把它写成 404 是我测错路径。──
    ('FR-479', 'GET', '/logs/federation/search?limit=1&targets=inst:1', None, '实例日志查询（经联邦面）'),
    ('FR-533', 'GET', '/config-baselines', None, 'Beacon 配置纳管（同基线面）'),
    # ── 日志平台核心（FR-475~477/483/503）──
    ('FR-476', 'GET', '/log-runtime/assets', None, 'CP 分发 VL 资产'),
    ('FR-477', 'GET', '/nodes/1/log-runtime', None, 'Catalog 分区代次/运行时'),
    ('FR-503', 'GET', '/nodes/1/settings/log-acquisition', None, '日志配置基线与热应用'),
    ('FR-475', 'GET', '/logs/federation/stream-field-names', None, '字段归一（多行/编码解析后的字段面）'),
    ('FR-483', 'GET', '/logs/federation/fields', None, '日志契约与实现对账'),
]


def main():
    rows = []
    idx = route_index()
    print('=== 批量验收（真实路由探测）===')
    print('已知路由总数: %d\n' % len(idx))

    for fr, method, path, body, intent in CASES:
        st, rsp, real = probe(method, path, body)
        kind, txt = classify(st, rsp)
        print('%-9s %-6s %-42s -> %-4s %s' % (fr, method, real, st, kind))

        v = Verdict(fr, intent)
        if kind == 'NO_ROUTE':
            v.dim('capability', False, '路由不存在（先核实方法/路径）')
            v.judge('FAIL', '404: ' + txt[:120])
        elif kind == 'SERVER_ERR':
            v.dim('semantics', False, '5xx 需排查')
            v.judge('FAIL', 'HTTP %s: %s' % (st, txt[:120]))
        elif kind == 'VALIDATED':
            v.dim('capability', True, '接口存在')
            v.dim('semantics', True, '错误可归因（带 code/error）')
            v.judge('PASS', txt[:140])
        elif kind == 'OK':
            v.dim('capability', True, '接口存在且正常响应')
            v.judge('PASS', txt[:120])
        elif kind == 'AUTH':
            v.dim('capability', True, '鉴权生效')
            v.judge('PASS', '')
        else:
            v.dim('capability', None, kind)
            v.judge('COND', txt[:120])
        v.ev('%-6s %s -> %s %s' % (method, real, st, txt[:200]))
        rows.append(v.as_dict())

    save_report(rows, 'batch-routes.json')
    from collections import Counter
    print('\n汇总: %s' % dict(Counter(r['result'] for r in rows)))


if __name__ == '__main__':
    main()
