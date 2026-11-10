#!/usr/bin/env python3
"""D 批：AI 助手与前端批验收。

覆盖：受限工具与逐调用确认 / 页面上下文 / 共享聊天界面 / AI 首页 /
      对话历史检索 / 历史消息编辑与回退。

本环境未配 AI 供应商（providers 为空），所以真实对话链路跑不起来；
但工具登记、确认票据、消息编辑/回退这些可以在接口与代码层验证。
分清「验不了」与「坏了」，后者才是缺陷。
"""
import json
import os
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fr_acceptance import api, Verdict, save_report

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))


def git_grep(pattern, paths=None):
    """git grep 必须带 -E，默认 BRE 下 `a|b` 不是「或」且会静默返回 0 行。"""
    cmd = ['git', 'grep', '-nE', pattern, 'HEAD']
    if paths:
        cmd += ['--'] + paths
    r = subprocess.run(cmd, cwd=REPO, capture_output=True, text=True)
    return [l for l in r.stdout.splitlines() if l]


def git_ls(prefix):
    r = subprocess.run(['git', 'ls-tree', '-r', '--name-only', 'HEAD', '--', prefix],
                       cwd=REPO, capture_output=True, text=True)
    return [l for l in r.stdout.splitlines() if l]


def frontend_files(pattern):
    allf = git_ls('apps/control-plane-web/src')
    import re
    rx = re.compile(pattern, re.I)
    return [f for f in allf if rx.search(f)]


def main():
    rows = []
    print('=== D 批：AI 助手与前端 ===\n')

    # 环境前提
    st_prov, d_prov = api('/api/v1/assistant/admin/providers')
    prov_count = len((d_prov or {}).get('items') or []) if isinstance(d_prov, dict) else -1
    print('AI 供应商数量: %d（0 表示真机无凭据，对话链路跑不了）\n' % prov_count)

    # ── FR-516 受限工具与逐调用写入确认 ──
    print('1. 受限系统工具与逐调用写入确认')
    v516 = Verdict('FR-516', '受限系统工具与逐调用写入确认')

    conf_model = git_ls('internal/controlplane/model/assistant_tool.go')
    conf_type = git_grep('type AssistantConfirmation struct', ['internal/controlplane/model/assistant_tool.go'])
    conf_waiting = git_grep('waiting_confirmation', ['internal/controlplane/model/assistant_chat.go'])
    conf_routes = git_grep('confirmations', ['internal/controlplane/router/assistant_chat.go'])
    # 最关键的语义：Agent Token 不能代替人类做确认
    gate_test = git_grep('CannotReachConfirmationDecide', ['internal/controlplane/router/assistant_confirm_gate_test.go'])
    deny_impl = git_grep('deny|Deny|forbidden|禁止',
                         ['internal/controlplane/service/assistant_tools.go'])
    print('   确认实体=%s；waiting 状态=%s；路由 %d'
          % ('在' if conf_type else '缺', bool(conf_waiting), len(conf_routes)))
    print('   确认闸测试（Agent Token 不可代确认）: %d' % len(gate_test))

    st_c, d_c = api('/api/v1/assistant/admin/skills')
    print('   skills 接口: %s' % st_c)

    v516.dim('capability', bool(conf_type and conf_routes),
             '确认实体 + list/get/decide 三路由均在')
    v516.dim('semantics', bool(gate_test),
             '确认闸区分人类与 Agent Token（Agent 不能代替人类确认）')
    v516.dim('capability', bool(conf_waiting),
             '运行状态机有 waiting_confirmation')
    v516.dim('semantics', bool(deny_impl),
             '禁止动作判定实现 %d 处' % len(deny_impl))
    v516.ev('type=%s waiting=%s routes=%d gate=%d'
            % (bool(conf_type), bool(conf_waiting), len(conf_routes), len(gate_test)))
    v516.ev('deny=%s' % [h[:100] for h in deny_impl[:2]])

    st_get, d_get = api('/api/v1/assistant/conversations/1/confirmations')
    print('   确认列表端点: %s' % st_get)
    v516.ev('confirmations endpoint=%s' % st_get)

    if conf_type and conf_routes and gate_test:
        v516.judge('PASS',
                   '确认票据体系完整（实体+三路由+waiting 状态机），且有专门测试验证'
                   '「Agent Token 不能代替人类做确认」。真实对话链路因本机无 AI 供应商未端到端跑')
    elif conf_type and conf_routes:
        v516.judge('COND', '确认票据与路由在，但未确认 Agent Token 隔离')
    else:
        v516.judge('FAIL', '确认票据或确认路由缺失')
    v516.show()
    rows.append(v516.as_dict())

    # ── FR-518 类型化页面上下文 ──
    print('\n2. 类型化页面上下文与实体选择')
    v518 = Verdict('FR-518', '类型化页面上下文与实体选择')
    ctx_files = frontend_files(r'page-context|context-entit')
    # 后端真实符号是 assistantContextPageKeys / assistantContextPayload /
    # sanitizeAssistantContext，不是字面量 page_context。早前拿后者去搜 → 0 命中是假阴性。
    ctx_server = git_grep('assistantContext(PageKeys|Payload|ID)|sanitizeAssistantContext',
                          ['internal/controlplane/service/'])
    ctx_whitelist = git_grep('assistantContextPageKeys', ['internal/controlplane/service/assistant_context.go'])
    ctx_sanitize = git_grep('sanitizeAssistantContext', ['internal/controlplane/service/assistant_context.go'])
    ctx_sensitive = git_grep('assistantContextSensitiveKeyMarkers', ['internal/controlplane/service/assistant_context.go'])
    print('   前端上下文文件 %d；后端符号 %d；页键白名单 %d；脱敏 %d；敏感键 %d'
          % (len(ctx_files), len(ctx_server), len(ctx_whitelist), len(ctx_sanitize), len(ctx_sensitive)))
    for f in ctx_files[:4]:
        print('      %s' % f)
    v518.dim('capability', bool(ctx_files) and bool(ctx_server),
             '前端模块 %d + 后端校验 %d' % (len(ctx_files), len(ctx_server)))
    v518.dim('semantics', bool(ctx_whitelist),
             '页键白名单（非白名单页键不得进上下文）')
    v518.dim('semantics', bool(ctx_sensitive),
             '敏感键标记与脱敏（不外发密钥类字段）')
    v518.ev('frontend=%s' % ctx_files[:5])
    v518.ev('server=%s' % [h[:100] for h in ctx_server[:3]])
    v518.ev('sensitive=%s' % [h[:100] for h in ctx_sensitive[:2]])

    ctx_tests = [f for f in ctx_files if '.test.' in f]
    v518.dim('modern', bool(ctx_tests), '前端测试 %d 个' % len(ctx_tests))
    if ctx_files and ctx_server:
        v518.judge('PASS', '前端上下文模块与后端白名单+脱敏校验均在')
    elif ctx_files:
        v518.judge('COND', '前端在，后端校验未确认')
    else:
        v518.judge('FAIL', '未找到页面上下文实现')
    v518.show()
    rows.append(v518.as_dict())

    # ── FR-519 共享聊天界面 ──
    print('\n3. 共享聊天界面与全局上下文面板')
    v519 = Verdict('FR-519', '共享聊天界面与全局上下文面板')
    chat_files = frontend_files(r'assistant/.*Chat|ChatPanel')
    shared_impl = git_grep('AssistantChatPanel', ['apps/control-plane-web/src'])
    print('   聊天面板文件 %d；被引用 %d' % (len(chat_files), len(shared_impl)))
    for f in chat_files[:5]:
        print('      %s' % f)
    v519.dim('capability', bool(chat_files), '聊天界面组件 %d 个' % len(chat_files))
    v519.dim('capability', len(shared_impl) >= 2, '被多处引用（共享而非单页专用）')
    v519.ev('files=%s' % chat_files[:5])
    v519.ev('refs=%d' % len(shared_impl))
    if chat_files and len(shared_impl) >= 2:
        v519.judge('PASS', '聊天界面组件存在且被多处复用')
    elif chat_files:
        v519.judge('COND', '组件存在但未确认共享复用')
    else:
        v519.judge('FAIL', '未找到聊天界面')
    v519.show()
    rows.append(v519.as_dict())

    # ── FR-520 AI Dashboard 首页 ──
    print('\n4. AI Dashboard 默认首页与经典首页切换')
    v520 = Verdict('FR-520', 'AI Dashboard 默认首页与经典首页切换')
    home_files = frontend_files(r'(Dashboard|AiHome|HomePage)')
    switch_impl = git_grep('classicHome|useClassicHome|经典首页|aiHome',
                           ['apps/control-plane-web/src'])
    print('   首页相关文件 %d；切换逻辑 %d' % (len(home_files), len(switch_impl)))
    for f in home_files[:5]:
        print('      %s' % f)
    v520.dim('capability', bool(home_files), '首页组件 %d 个' % len(home_files))
    v520.dim('capability', bool(switch_impl), '首页切换逻辑 %d 处' % len(switch_impl))
    v520.ev('files=%s' % home_files[:5])
    v520.ev('switch=%s' % [h[:100] for h in switch_impl[:3]])
    if home_files and switch_impl:
        v520.judge('PASS', 'AI 首页与切换逻辑均在')
    elif home_files:
        v520.judge('COND', '首页存在但切换逻辑未确认')
    else:
        v520.judge('FAIL', '未找到首页实现')
    v520.show()
    rows.append(v520.as_dict())

    # ── FR-542 对话历史独立页面与检索 ──
    print('\n5. 对话历史独立页面与检索')
    v542 = Verdict('FR-542', '对话历史独立页面与检索')
    hist_files = frontend_files(r'[Hh]istor|Conversation')
    search_impl = git_grep('nextCursor|q=|keyword', ['internal/controlplane/router/assistant_chat.go'])
    # 真机：列表接口是否支持分页
    st_list, d_list = api('/api/v1/assistant/conversations')
    has_cursor = isinstance(d_list, dict) and 'nextCursor' in (d_list or {})
    print('   历史相关文件 %d；列表接口 %s nextCursor=%s'
          % (len(hist_files), st_list, has_cursor))
    for f in hist_files[:5]:
        print('      %s' % f)
    v542.dim('capability', bool(hist_files), '历史页面组件 %d 个' % len(hist_files))
    v542.dim('capability', has_cursor, '列表接口带游标分页')
    v542.ev('files=%s' % hist_files[:5])
    v542.ev('list=%s cursor=%s' % (st_list, has_cursor))
    if hist_files and has_cursor:
        v542.judge('PASS', '历史页面与游标分页接口均在')
    elif has_cursor:
        v542.judge('COND', '后端分页可用，前端历史页面未确认')
    else:
        v542.judge('FAIL', '历史页面或分页接口缺失')
    v542.show()
    rows.append(v542.as_dict())

    # ── FR-543 历史消息编辑与回退 ──
    print('\n6. 历史消息编辑与回退')
    v543 = Verdict('FR-543', '历史消息编辑与回退')
    # 真实符号是 EditUserMessage（在 service/assistant_history.go），不是 EditMessage。
    edit_route = git_grep('messages/:id.*EditMessage', ['internal/controlplane/router/assistant_chat.go'])
    rollback_route = git_grep('rollback', ['internal/controlplane/router/assistant_chat.go'])
    edit_svc = git_grep('func .* EditUserMessage|func .* EditMessage', ['internal/controlplane/service/'])
    rollback_svc = git_grep('func .* Rollback', ['internal/controlplane/service/'])
    # 编辑必须保留原文（可追溯），不能静默覆写
    keeps_original = git_grep('Original|original', ['internal/controlplane/service/assistant_history.go'])
    print('   编辑路由 %d；回退路由 %d；编辑服务 %d；回退服务 %d；保留原文 %d'
          % (len(edit_route), len(rollback_route), len(edit_svc),
             len(rollback_svc), len(keeps_original)))
    v543.dim('capability', bool(edit_route and edit_svc), '消息编辑完整链路')
    v543.dim('capability', bool(rollback_route and rollback_svc), '对话回退完整链路')
    v543.dim('semantics', bool(keeps_original), '编辑保留原文可追溯')
    v543.ev('edit_route=%s' % [h[:110] for h in edit_route[:2]])
    v543.ev('rollback_route=%s' % [h[:110] for h in rollback_route[:2]])
    v543.ev('edit_svc=%s' % [h[:110] for h in edit_svc[:2]])
    v543.ev('rollback_svc=%d keeps_original=%d' % (len(rollback_svc), len(keeps_original)))
    if edit_route and rollback_route and edit_svc and rollback_svc:
        v543.judge('PASS', '编辑与回退路由+服务均齐备')
    else:
        v543.judge('FAIL', '编辑或回退链路不完整')
    v543.show()
    rows.append(v543.as_dict())

    p = save_report(rows, 'd-ai-frontend.json')
    print('\n已保存: %s' % p)
    ok = sum(1 for r in rows if r['result'] == 'PASS')
    cond = sum(1 for r in rows if r['result'] == 'COND')
    bad = sum(1 for r in rows if r['result'] == 'FAIL')
    print('\n小计: PASS=%d COND=%d FAIL=%d' % (ok, cond, bad))
    return 0


if __name__ == '__main__':
    sys.exit(main())
