#!/usr/bin/env python3
"""P5 AI 助手批：在无供应商配置下的可验收面

现状：providers=0, models=0, conversations=0（从未配置过外部供应商）

因此本批区分三类：
  A. 可验（无需供应商）：接口存在性、配置 CRUD、降级语义、内置工具开关
  B. 无法验收（需外部凭据）：模型发现、真实对话、工具调用
  C. 可验（前端/静态）：Dashboard 切换、页面上下文

关键：不能把「未配置」污名为「不达标」。
"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fr_acceptance import api, Verdict, save_report


def main():
    rows = []
    print('=== P5 AI 助手批（无供应商配置环境）===\n')

    # ---- A1. 接口面存在性 ----
    print('A1. 接口面存在性')
    apis = [
        ('供应商管理', '/api/v1/assistant/admin/providers'),
        ('模型管理',   '/api/v1/assistant/admin/models'),
        ('平台设置',   '/api/v1/assistant/admin/settings'),
        ('可用模型',   '/api/v1/assistant/models'),
        ('会话列表',   '/api/v1/assistant/conversations'),
    ]
    ok_n = 0
    for name, p in apis:
        st, _ = api(p)
        good = st == 200
        ok_n += 1 if good else 0
        print('   %-12s %-6s %s' % (name, st, 'ok' if good else 'NO'))
    v1 = Verdict('FR-514', 'AI 供应商/模型/平台配置接口')
    v1.dim('capability', ok_n == len(apis), '%d/%d 接口可用' % (ok_n, len(apis)))
    v1.judge('PASS' if ok_n == len(apis) else 'FAIL', '')
    v1.show()
    rows.append(v1.as_dict())

    # ---- A2. 设置语义 ----
    print('\nA2. 平台设置语义（FR-541 系统提示词/思考强度、FR-540 内置工具）')
    st, s = api('/api/v1/assistant/admin/settings')
    if isinstance(s, dict):
        print('   builtinToolsEnabled=%s' % s.get('builtinToolsEnabled'))
        print('   systemPrompt=%r' % (s.get('systemPrompt') or '')[:40])
        print('   defaultModelId=%s' % s.get('defaultModelId'))
        print('   insecureAllowed=%s' % s.get('insecureAllowed'))
        has_keys = all(k in s for k in
                       ('builtinToolsEnabled', 'systemPrompt', 'defaultModelId'))
        v2 = Verdict('FR-540', '内置 MCP 工具默认启用')
        v2.dim('capability', s.get('builtinToolsEnabled') is True,
               'builtinToolsEnabled=%s（默认应为 true）' % s.get('builtinToolsEnabled'))
        v2.judge('PASS' if s.get('builtinToolsEnabled') is True else 'COND', '')
        v2.show()
        rows.append(v2.as_dict())

        v3 = Verdict('FR-541', '系统提示词可配置')
        v3.dim('capability', has_keys, '设置包含 systemPrompt 字段')
        v3.judge('PASS' if has_keys else 'FAIL', '字段齐全' if has_keys else '缺字段')
        v3.show()
        rows.append(v3.as_dict())

    # ---- B. 需外部凭据的项 ----
    print('\nB. 需外部供应商凭据的项（无凭据 → 无法验收，不扣分）')
    blocked = [
        ('FR-538', '供应商协议三选与模型自动发现'),
        ('FR-539', '供应商级出站代理'),
        ('FR-515', '内置 Agent 真实对话'),
        ('FR-517', '外部 Streamable HTTP MCP 连接'),
        ('FR-544', '工具渐进式披露与执行路径'),
    ]
    for fr, title in blocked:
        v = Verdict(fr, title)
        v.dim('capability', None, '环境无供应商配置，需真实 API Key 才能验')
        v.judge('BLOCKED', '需外部供应商凭据；本环境 providers=0，不构成功能不达标')
        v.show()
        rows.append(v.as_dict())

    # ---- C. 无凭据也能验的降级语义 ----
    print('\nC. 无供应商时的降级语义（语义诚实性）')
    st, models = api('/api/v1/assistant/models')
    degraded_ok = (st == 200 and isinstance(models, dict)
                   and models.get('defaultModelId') is None
                   and models.get('items') == [])
    print('   /assistant/models → %s' % json.dumps(models, ensure_ascii=False)[:150])
    v4 = Verdict('FR-514', '无供应商时语义诚实（不报错、不伪装可用）')
    v4.dim('semantics', degraded_ok,
           '无供应商时返回空列表 + defaultModelId=null，而非 5xx 或假模型')
    v4.judge('PASS' if degraded_ok else 'COND',
             '降级语义正确：明确表达“无可用模型”' if degraded_ok else '降级行为存疑')
    v4.show()
    rows.append(v4.as_dict())

    save_report(rows, 'p5-assistant.json')

    from collections import Counter
    print('\n裁定汇总: %s' % dict(Counter(r['result'] for r in rows)))


if __name__ == '__main__':
    main()
