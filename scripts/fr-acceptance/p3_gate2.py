#!/usr/bin/env python3
"""P3 审批闸补充验证：给足权限后看是否走审批

前次失败原因（已查明，不是产品缺陷）：
  agent.go:511-522 的顺序是 ①AuthorizeInstanceAction ②gateApproval。
  注释写明「越权请求不应变成一张等人批准的票——审批是有权限的人决定要不要做，
  不是给没有权限的人开后门」。
  我的测试 Token 权限不足，在 ① 就被 403，根本没走到 ② 审批闸。

本次：用 writeAllowlist 授予 instance_stop，再看是否返回 awaiting_approval。
"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fr_acceptance import api, Verdict, save_report


def agent_api(path, tok, method='GET'):
    import urllib.error
    import urllib.request
    req = urllib.request.Request('http://127.0.0.1:8080' + path, method=method,
                                 headers={'Authorization': 'Bearer ' + tok})
    try:
        with urllib.request.urlopen(req, timeout=20) as r:
            return r.status, json.loads(r.read())
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read())
        except Exception:
            return e.code, {}
    except Exception as e:
        return None, {'conn_error': str(e)[:150]}


def main():
    rows = []
    print('=== P3 审批闸补充验证（给足权限）===\n')

    st, insts = api('/api/v1/instances?pageSize=200')
    lst = insts if isinstance(insts, list) else []
    target = next((i for i in lst if i.get('status') == 'STOPPED'), None) or (lst[0] if lst else None)
    if not target:
        print('无靶点')
        return
    iid = target['id']
    print('靶点: %s (id=%s, %s)\n' % (target.get('name'), iid, target.get('status')))

    # 用 writeAllowlist 授予 instance_stop（V1 写法）
    for attempt in (
        {'name': 'fr-gate-1', 'scopedNodeIds': [1], 'writeAllowlist': ['instance_stop'], 'ttlDays': 1},
        {'name': 'fr-gate-2', 'scopedNodeIds': [1], 'writeAllowlist': ['instance.stop', 'instance_stop'], 'ttlDays': 1},
    ):
        st, resp = api('/api/v1/agent/tokens', method='POST', body=attempt)
        print('创建 %s -> HTTP=%s %s' % (attempt['name'], st,
              '' if st == 201 else str(resp)[:150]))
        if st != 201:
            continue
        plain = resp.get('plaintext')
        tid = (resp.get('token') or {}).get('id')
        try:
            print('   用该 Token 调 stop ...')
            st2, body = agent_api('/api/v1/agent/instances/%s/stop' % iid, plain, 'POST')
            txt = json.dumps(body, ensure_ascii=False)
            print('   HTTP=%s body=%s' % (st2, txt[:300]))

            awaiting = 'awaiting_approval' in txt.lower() or bool(body.get('ticketId'))
            forbidden = st2 == 403

            v = Verdict('FR-492', '高危动作在 Agent 路径必须走审批')
            if awaiting:
                v.dim('capability', True, '已返 awaiting_approval，审批闸生效')
                v.dim('semantics', bool(body.get('ticketId')),
                       '带 ticketId=%s' % body.get('ticketId'))
                v.judge('PASS', '有权限时正确进入审批闸')
            elif forbidden:
                v.dim('capability', None, '权限仍不足（403），未到审批闸')
                v.judge('BLOCKED', 'Token 权限配置未能授予 instance_stop')
            else:
                v.dim('capability', False, '既未拦截也未进审批: HTTP=%s' % st2)
                v.judge('FAIL', '有权限时未走审批闸')
            v.ev('HTTP=%s body=%s' % (st2, txt[:400]))
            v.show()
            rows.append(v.as_dict())
            if awaiting:
                break
        finally:
            api('/api/v1/agent/tokens/%s' % tid, method='DELETE')
            print('   已吊销临时 Token id=%s' % tid)

    save_report(rows, 'p3-gate.json')


if __name__ == '__main__':
    main()
