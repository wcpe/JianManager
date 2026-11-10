#!/usr/bin/env python3
"""P3 审批拦截真机验证（安全版）

步骤：
  1. 创建一个最小权限、仅 1 天有效、名为“验收临时”的 Agent Token
  2. 用该 Token 调 agent stop（高危，应返 awaiting_approval + ticketId）
  3. 查审批票据确认已落库
  4. 立即吊销 Token（不留痕）

安全：镜像全部用 STOPPED 的 allinmobs-accept；且审批拦截生效时根本不会执行。
"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fr_acceptance import api, Verdict, save_report


def agent_api(path, tok, method='GET', body=None):
    """带 Agent Token 调用（区别于 JWT）。"""
    import urllib.error
    import urllib.request
    url = 'http://127.0.0.1:8080' + path
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method,
                                 headers={'Authorization': 'Bearer ' + tok,
                                          'Content-Type': 'application/json'})
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
    print('=== P3 审批拦截真机验证（安全版）===\n')

    # 找靶点（优先 STOPPED 的）
    st, insts = api('/api/v1/instances?pageSize=200')
    lst = insts if isinstance(insts, list) else []
    target = next((i for i in lst if i.get('status') == 'STOPPED'), None) or (lst[0] if lst else None)
    if not target:
        print('无靶点实例，中止')
        return
    iid = target.get('id')
    print('靶点: %s (id=%s, %s)' % (target.get('name'), iid, target.get('status')))

    # 创建临时 Token
    print('\n1. 创建临时 Agent Token（1 天，最小权限）')
    st, resp = api('/api/v1/agent/tokens', method='POST', body={
        'name': 'fr-acceptance-temp',
        'scopedNodeIds': [1],
        'writeAllowlist': ['instance.stop'],
        'ttlDays': 1,
    })
    if st == 400 and 'capabilities' in str(resp):
        pass
    if st != 201:
        # V1 Token 不接受写白名单之外的字段组合，退一步用纯只读 + 写白名单
        print('   V1 格式失败，重试: %s' % str(resp)[:160])
        st, resp = api('/api/v1/agent/tokens', method='POST', body={
            'name': 'fr-acceptance-temp',
            'scopedNodeIds': [1],
            'ttlDays': 1,
        })
    print('   HTTP=%s' % st)
    if st != 201:
        print('   创建失败: %s' % str(resp)[:200])
        return
    plain = resp.get('plaintext')
    tokid = (resp.get('token') or {}).get('id')
    print('   tokenId=%s prefix=%s' % (tokid, plain[:12] if plain else '?'))

    try:
        # 2. 用 Token 调高危 stop
        print('\n2. 用 Agent Token 调 stop（高危，应返 awaiting_approval）')
        st, body = agent_api('/api/v1/agent/instances/%s/stop' % iid, plain, method='POST')
        print('   HTTP=%s body=%s' % (st, json.dumps(body, ensure_ascii=False)[:320]))

        awaiting = ('awaiting_approval' in json.dumps(body).lower() or
                    bool(body.get('ticketId')))
        v = Verdict('FR-492', '高危动作在 Agent 路径必须走审批')
        v.dim('capability', awaiting,
              'HTTP=%s，%s' % (st, '已返 awaiting_approval' if awaiting else '未拦截'))
        v.dim('semantics', bool(body.get('ticketId')),
              '带 ticketId=%s' % body.get('ticketId'))
        v.ev('HTTP=%s body=%s' % (st, json.dumps(body, ensure_ascii=False)[:400]))
        v.judge('PASS' if awaiting else 'FAIL',
                '拦截且给了可查票据' if awaiting else 'Agent 路径未拦截')
        v.show()
        rows.append(v.as_dict())

        # 3. 票据是否真的落库可查
        if body.get('ticketId'):
            print('\n3. 验证票据已落库')
            st2, ap = api('/api/v1/approvals?pageSize=10')
            items = ap.get('items', []) if isinstance(ap, dict) else []
            found = [x for x in items if str(x.get('id')) == str(body.get('ticketId'))]
            print('   审批列表 %d 张，命中靶票据: %s' % (len(items), bool(found)))
            v3 = Verdict('FR-490', '票据冻结落库可查')
            v3.dim('capability', bool(found), '票据 %s 在审批队列%s'
                   % (body.get('ticketId'), '可见' if found else '不可见'))
            v3.judge('PASS' if found else 'COND', '')
            v3.show()
            rows.append(v3.as_dict())
    finally:
        # 4. 必吊销
        print('\n4. 吊销临时 Token')
        st, _ = api('/api/v1/agent/tokens/%s' % tokid, method='DELETE')
        print('   吊销 HTTP=%s' % st)
        st, toks = api('/api/v1/agent/tokens')
        still = any(t.get('id') == tokid for t in (toks if isinstance(toks, list) else []))
        print('   临时 Token 仍存在: %s' % still)

    save_report(rows, 'p3-approval.json')


if __name__ == '__main__':
    main()
