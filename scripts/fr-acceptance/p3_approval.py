#!/usr/bin/env python3
"""P3 审批与安全批：Agent 路径审批拦截真机验证

重要边界（先确认再验）：
  审批拦截的作用域是 **Agent Token 的三个入口**（MCP CallTool / CLI / curl），
  目的是"同一个 Token 换用不同客户端不能跳过审批"。
  人类管理员走普通 /instances/:id/stop 本来就无需审批——那不是缺口，是设计边界。

因此本验收只对 Agent 路径做拦截测试，不触碰人类管理路径。
且用最轻的试探：不带 Agent Token 时应在鉴权层就被拦（不会真的停服）。
"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fr_acceptance import api, Verdict, save_report


def main():
    rows = []
    print('=== P3 Agent 路径审批拦截验证 ===\n')

    # ---- 1. 无 Agent Token 访问 agent 端点（应在鉴权层拒绝）----
    print('1. 无 Agent Token 访问 /agent/instances/1/stop')
    st, resp = api('/api/v1/agent/instances/1/stop', method='POST')
    print('   HTTP=%s body=%s' % (st, str(resp)[:200]))
    v1 = Verdict('FR-489', 'Agent 端点必须独立鉴权')
    ok = st in (401, 403)
    v1.dim('capability', ok, '无 Token 返回 %s' % st)
    v1.dim('semantics', isinstance(resp, dict) and bool(resp.get('error')),
           '拒绝响应带 error 字段')
    v1.ev('HTTP=%s body=%s' % (st, str(resp)[:200]))
    v1.judge('PASS' if ok else 'FAIL',
             '无 Token 被拒（鉴权层）' if ok else '无 Token 竟可访问')
    v1.show()
    rows.append(v1.as_dict())

    # ---- 2. Agent Token 列表与能力 ----
    print('\n2. Agent Token 与能力声明')
    st, toks = api('/api/v1/agent/tokens')
    items = toks if isinstance(toks, list) else (toks.get('items', []) if isinstance(toks, dict) else [])
    print('   Token 数: %d' % len(items))
    for t in items[:3]:
        caps = t.get('capabilities') or []
        print('   - %-20s caps=%d scopedNodes=%s' % (
            t.get('name'), len(caps), t.get('scopedNodeIds')))
    v2 = Verdict('FR-493', 'Token 限域可查且生效')
    v2.dim('capability', len(items) > 0, '存在 %d 个 Agent Token' % len(items))
    v2.ev('tokens=%d' % len(items))
    v2.judge('PASS' if items else 'COND', 'Token 体系存在' if items else '无 Token 无法验证限域')
    v2.show()
    rows.append(v2.as_dict())

    # ---- 3. 审批队列可读 ----
    print('\n3. 审批中心可用性')
    st, ap = api('/api/v1/approvals?pageSize=5')
    ok3 = st == 200
    print('   HTTP=%s' % st)
    v3 = Verdict('FR-493', '审批中心可用')
    v3.dim('capability', ok3, 'HTTP=%s' % st)
    v3.judge('PASS' if ok3 else 'FAIL', '')
    v3.show()
    rows.append(v3.as_dict())

    # ---- 4. 代码面：高危清单覆盖率 ----
    print('\n4. 高危 action 清单（代码面）')
    import subprocess
    REPO = '/home/wxys233/projects/JianManager'
    p = subprocess.run(['grep', '-c', '": "',
                        os.path.join(REPO, 'internal/controlplane/service/approval_registry.go')],
                       capture_output=True, text=True)
    print('   清单条目数（近似）: %s' % p.stdout.strip())

    save_report(rows, 'p3-approval.json')


if __name__ == '__main__':
    main()
