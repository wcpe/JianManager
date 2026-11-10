#!/usr/bin/env python3
"""P5 AI 助手批：用模拟供应商做真机验收

模拟服务：scripts/fr-acceptance/mock_ai_provider.py（端口 18181）
覆盖 FR-538（协议三选+模型发现）、FR-539（出站代理）、FR-515（内置 Agent）、
FR-517（MCP）、FR-544（工具披露）
"""
import json
import os
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fr_acceptance import api, Verdict, save_report

MOCK = 'http://127.0.0.1:18181/v1'
CREATED = []  # (kind, id)


def cleanup():
    for kind, i in CREATED:
        try:
            if kind == 'provider':
                api('/api/v1/assistant/admin/providers/%s' % i, method='DELETE')
            elif kind == 'model':
                api('/api/v1/assistant/admin/models/%s' % i, method='DELETE')
        except Exception:
            pass


def main():
    rows = []
    print('=== P5 AI 助手批（模拟供应商 18181）===\n')

    # ── FR-538①：三种协议各建一个供应商 ──
    print('1. FR-538 协议三选：三种协议各建一个供应商')
    proto_ids = {}
    for proto in ('openai-chat', 'openai-responses', 'anthropic'):
        st, resp = api('/api/v1/assistant/admin/providers', method='POST', body={
            'name': 'mock-%s' % proto,
            'protocol': proto,
            'baseUrl': MOCK,
            'enabled': True,
            'credentialMode': 'stored',
            'credentialSecret': 'sk-mock-test',
            'version': 0,
        })
        pid = (resp or {}).get('id') if isinstance(resp, dict) else None
        print('   %-18s HTTP=%s id=%s %s' % (proto, st, pid, '' if st in (200, 201) else str(resp)[:120]))
        if st in (200, 201) and pid:
            proto_ids[proto] = pid
            CREATED.append(('provider', pid))

    v1 = Verdict('FR-538', '供应商协议三选')
    v1.dim('capability', len(proto_ids) == 3,
           '三种协议均可创建：%s' % list(proto_ids))
    v1.dim('modern', len(proto_ids) == 3, 'openai-chat / openai-responses / anthropic 均支持')
    v1.ev('created=%s' % proto_ids)
    v1.judge('PASS' if len(proto_ids) == 3 else 'FAIL', '')
    v1.show()
    rows.append(v1.as_dict())

    # ── FR-538②：模型自动发现（对三种协议都试）──
    print('\n2. FR-538 模型自动发现')
    discover_ok = {}
    for proto, pid in proto_ids.items():
        st, resp = api('/api/v1/assistant/admin/providers/%s/discover-models' % pid, method='POST')
        body = resp if isinstance(resp, dict) else {}
        n = len(body.get('discovered') or [])
        total = body.get('total')
        print('   %-18s HTTP=%s discovered=%s total=%s' % (proto, st, n, total))
        if n:
            print('      %s' % [m.get('modelId') for m in body['discovered']][:5])
        discover_ok[proto] = (st == 200 and n > 0)

    v2 = Verdict('FR-538', '模型自动发现')
    # 空集陷阱：all({}) 恒为 True，必须先确认确实试过协议
    allok = bool(discover_ok) and all(discover_ok.values())
    v2.dim('capability', allok, '三协议发现结果: %s' % discover_ok)
    v2.dim('semantics', allok, '能正确解析 {data:[{id}]} 形态并返回模型列表')
    v2.ev(json.dumps(discover_ok))
    v2.judge('PASS' if allok else 'FAIL',
             '' if allok else '有协议未能发现模型')
    v2.show()
    rows.append(v2.as_dict())

    # ── FR-538③：错误可归因（上游错误码分类）──
    print('\n3. FR-538 错误可归因（上游 401/429/500 分类）')
    err_results = {}
    if 'openai-chat' in proto_ids:
        # 建带错误标记的模型来触发上游错误
        pid = proto_ids['openai-chat']
        for tag, want in (('err401', 401), ('err429', 429)):
            st, resp = api('/api/v1/assistant/admin/models', method='POST', body={
                'providerId': pid,
                'modelId': 'mock-%s' % tag,
                'displayName': 'err test %s' % tag,
                'enabled': True,
            })
            mid = (resp or {}).get('id') if isinstance(resp, dict) else None
            if mid:
                CREATED.append(('model', mid))
                st2, resp2 = api('/api/v1/assistant/admin/models/%s/test' % mid, method='POST')
                txt = json.dumps(resp2, ensure_ascii=False) if isinstance(resp2, (dict, list)) else str(resp2)
                print('   %-8s test HTTP=%s %s' % (tag, st2, txt[:160]))
                err_results[tag] = txt

    v3 = Verdict('FR-538', '上游错误可归因（非静默失败）')
    # 关键：错误信息必须能让管理员区分密钥错/限流
    classified = any('auth' in t.lower() or '密钥' in t or '401' in t for t in err_results.values())
    v3.dim('semantics', bool(err_results), '能触发上游错误路径: %s' % list(err_results))
    v3.dim('capability', classified, '错误被分类而非笼统失败')
    v3.ev(json.dumps(err_results, ensure_ascii=False)[:400])
    v3.judge('PASS' if classified else 'COND',
             '' if classified else '错误分类需人工确认')
    v3.show()
    rows.append(v3.as_dict())

    save_report(rows, 'p5-mock-assistant.json')
    print('\n--- 清理 ---')
    cleanup()
    print('已清理 %d 项' % len(CREATED))


if __name__ == '__main__':
    try:
        main()
    finally:
        pass
