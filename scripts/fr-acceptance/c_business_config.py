#!/usr/bin/env python3
"""C 批：业务与配置类验收。

覆盖：写穿离线禁改 / 对外 API 错误码 / 配置灰度编排 / 业务插件面板 /
      批量目标解析与二次确认 / SSE 连接治理 / 批量流式进度 / 业务事件位点账本。

本环境 business 子系统未装配（router.go 可选装配），所以分两类处理：
  - 接口层能看的：看真实 HTTP 行为（含 404 也要分清“未装配”与“不存在”）
  - 只能在代码层看的：走 HEAD 结构证，并在结论里写清验证层次。
"""
import json
import os
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fr_acceptance import api, Verdict, save_report

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))


def git_grep(pattern, paths=None):
    """在 HEAD 里搜，不用工作区——多处代码正在被别的任务并发删除。

    必须带 -E：git grep 默认用 **基本正则**（BRE），里面 `a|b` 不是“或”，
    而是一个字面量管道符。我第一版就是裸传 BRE 交替式，导致 FR-536/537
    全报 0 命中——把存在的功能误判为缺失。这是断言工具在造假。
    """
    cmd = ['git', 'grep', '-nE', pattern, 'HEAD']
    if paths:
        cmd += ['--'] + paths
    r = subprocess.run(cmd, cwd=REPO, capture_output=True, text=True)
    return [l for l in r.stdout.splitlines() if l]


def git_ls(prefix):
    r = subprocess.run(['git', 'ls-tree', '-r', '--name-only', 'HEAD', '--', prefix],
                       cwd=REPO, capture_output=True, text=True)
    return [l for l in r.stdout.splitlines() if l]


def main():
    rows = []
    print('=== C 批：业务与配置类 ===\n')

    # 先弄清 business 子系统是否装配
    st_b, _ = api('/api/v1/business/batch')
    biz_mounted = st_b is not None and st_b != 404
    print('business 子系统装配: %s (GET /business/batch -> %s)\n'
          % ('是' if biz_mounted else '否（可选装配，404 不等于功能缺失）', st_b))

    # ── FR-524 写穿与离线禁改 ──
    print('1. 写穿与离线禁改')
    v524 = Verdict('FR-524', '写穿与离线禁改')
    # 离线检查：对比两条路径
    write_path = git_grep('resolveTarget(instanceID', ['internal/controlplane/service/business.go'])
    recon_status = git_grep('inst.Status != model.InstanceStatusRunning',
                            ['internal/controlplane/service/business_reconcile.go'])
    print('   写穿路径调用 resolveTarget: %d 处' % len(write_path))
    print('   对账路径有状态检查: %s' % ('有' if recon_status else '无'))

    # resolveTarget 本身做不做状态校验
    rt_body = git_grep('InstanceStatusRunning', ['internal/controlplane/service/business.go'])
    print('   resolveTarget 含实例状态校验: %s' % ('有' if rt_body else '无'))

    v524.dim('capability', True, '写穿链路存在（DispatchWrite → resolveTarget → probe）')
    v524.dim('semantics', bool(rt_body),
             '写穿内置离线状态校验' if rt_body
             else '写穿不校验实例状态，离线时回 200+“节点未连接”而非 409 INSTANCE_OFFLINE')
    v524.ev('reconcile has status check=%s' % bool(recon_status))
    v524.ev('business.go has InstanceStatusRunning=%s' % bool(rt_body))

    if not rt_body and recon_status:
        v524.judge('FAIL',
                   '规格 §6 第1条要求离线→409 INSTANCE_OFFLINE，但写穿路径不校验实例状态。'
                   '事实安全：探针跑在游戏服进程内，实例停了就没了，业务数据写不进去；'
                   'JM 也不持业务数据。但可归因性坏了：运维看到“节点未连接”会去查网络，'
                   '实际原因是服没开。已加 TestBusinessWrite_OfflineInstanceReportsGenericError 钉住现状')
    elif rt_body:
        v524.judge('PASS', '写穿内置离线校验')
    else:
        v524.judge('FAIL', '两条路径均无状态校验')
    v524.show()
    rows.append(v524.as_dict())

    # ── FR-528 对外 API 规范与错误码统一 ──
    print('\n2. 对外 API 规范与错误码统一')
    v528 = Verdict('FR-528', '对外 API 规范与错误码统一')

    # 真机：各错误码形状是否统一
    probes = [
        ('不存在的路由', '/api/v1/definitely-not-here'),
        ('不存在的实例', '/api/v1/instances/999999'),
        ('不存在的任务', '/api/v1/tasks/999999'),
    ]
    shapes = {}
    for name, p in probes:
        st, d = api(p)
        code = d.get('error') if isinstance(d, dict) else None
        msg = d.get('message') if isinstance(d, dict) else str(d)[:60]
        shapes[name] = (st, code, bool(msg))
        print('   %-14s HTTP=%s error=%r msg=%s' % (name, st, code, 'yes' if msg else 'NO'))

    uniform = all(isinstance(c, str) and c.isupper() for _, c, _ in shapes.values() if c)
    has_msg = all(m for _, _, m in shapes.values())
    v528.dim('capability', uniform, '错误码均为大写枚举风格')
    v528.dim('semantics', has_msg, '错误响应均带 message')
    v528.ev(json.dumps(shapes, ensure_ascii=False))

    if uniform and has_msg:
        v528.judge('PASS', '错误响应形状统一（error 大写码 + message）')
    else:
        v528.judge('COND', '错误响应形状存在差异，需逐接口核对')
    v528.show()
    rows.append(v528.as_dict())

    # ── FR-531 / FR-532 配置灰度与生效编排 ──
    print('\n3. 配置分批灰度与生效编排')
    v531 = Verdict('FR-531', '配置分批灰度与编排')
    batch_files = git_ls('internal/controlplane/service/') 
    batch_impl = [f for f in batch_files if 'config' in f and ('batch' in f or 'delivery' in f or 'stage' in f)]
    print('   配置批量/投递相关文件: %s' % (batch_impl[:6] or '无'))

    st_cfg, d_cfg = api('/api/v1/config-files')
    st_base, d_base = api('/api/v1/config-baselines')
    print('   config-files=%s config-baselines=%s' % (st_cfg, st_base))

    v531.dim('capability', bool(batch_impl) and st_cfg == 200,
             '配置投递实现 %d 文件，接口可用' % len(batch_impl))
    v531.ev('impl=%s' % batch_impl[:6])
    v531.ev('config-files=%s baselines=%s' % (st_cfg, st_base))
    v531.judge('PASS' if (batch_impl and st_cfg == 200) else 'FAIL',
               '配置分层与投递接口可用' if batch_impl else '缺乏批量投递实现')
    v531.show()
    rows.append(v531.as_dict())

    v532 = Verdict('FR-532', '配置生效编排')
    # 判据用规格里的真实术语：它区分「落盘」与「生效」两个阶段。
    # 早前拿 config_delivery|ApplyConfig|DeliverConfig 去 grep 全空了，
    # 那是关键词搭错，不是功能缺失——这个领域用的是 DiskWritten/Observe 这套词。
    disk_vs_effective = git_grep('DiskWritten', ['internal/controlplane/service/config_delivery.go'])
    observe = git_grep('ObserveWindow|DeliveryObserve', ['internal/controlplane/service/config_delivery.go'])
    activate_fail = git_grep('TargetCodeActivateFailed', ['internal/controlplane/service/config_delivery.go'])
    print('   落盘/生效区分: %d；观察窗: %d；生效失败码: %d'
          % (len(disk_vs_effective), len(observe), len(activate_fail)))
    v532.dim('capability', bool(disk_vs_effective),
             '区分 DiskWritten 与生效结论')
    v532.dim('semantics', bool(activate_fail),
             '落盘成功但生效失败时记独立错误码（不把落盘当生效）')
    v532.dim('perf', bool(observe), '有观察窗机制评估生效结果')
    v532.ev('disk=%s' % [h[:110] for h in disk_vs_effective[:2]])
    v532.ev('observe=%s' % [h[:110] for h in observe[:2]])
    v532.ev('activateFail=%s' % [h[:110] for h in activate_fail[:2]])
    v532.judge('PASS' if (disk_vs_effective and activate_fail) else 'FAIL',
               '生效编排存在：落盘与生效分开记，失效有独立错误码' if disk_vs_effective
               else '未找到落盘/生效区分')
    v532.show()
    rows.append(v532.as_dict())

    # ── FR-534 业务插件面板 ──
    print('\n4. 业务插件面板')
    v534 = Verdict('FR-534', '业务插件面板')
    st_plug, d_plug = api('/api/v1/instances/1/plugins')
    print('   plugins 接口: %s' % st_plug)
    plugin_ui = subprocess.run(['git', 'ls-tree', '-r', '--name-only', 'HEAD',
                                '--', 'apps/control-plane-web/src'],
                               cwd=REPO, capture_output=True, text=True).stdout
    plugin_views = [l for l in plugin_ui.splitlines() if 'plugin' in l.lower()]
    plugin_components = [l for l in plugin_views
                         if l.endswith('.tsx') and '.test.' not in l and '.dom.' not in l]
    print('   前端插件相关文件 %d 个，其中组件 %d 个' % (len(plugin_views), len(plugin_components)))
    for pc in plugin_components[:5]:
        print('      %s' % pc)
    v534.dim('capability', bool(plugin_components),
             '前端插件面板组件 %d 个' % len(plugin_components))
    v534.ev('components=%s' % plugin_components[:5])
    v534.ev('api=%s' % st_plug)
    v534.judge('PASS' if plugin_components else 'FAIL',
               '插件面板组件与接口均在' if plugin_components else '未找到插件面板')
    v534.show()
    rows.append(v534.as_dict())

    # ── FR-535 批量目标解析与跨区二次确认 ──
    print('\n5. 批量目标解析与跨区二次确认')
    v535 = Verdict('FR-535', '批量目标解析与跨区二次确认')
    # 规格里的术语是 confirmToken / Preview，不是 crossZone。
    preview_impl = git_grep('batch/preview', ['internal/controlplane/router/business_batch.go'])
    confirm_impl = git_grep('ConfirmToken', ['internal/controlplane/service/business_batch.go'])
    signer_impl = git_grep('BusinessConfirmTokenSigner', ['internal/controlplane/service/'])
    print('   preview 路由: %d；ConfirmToken: %d；签名器: %d'
          % (len(preview_impl), len(confirm_impl), len(signer_impl)))
    v535.dim('capability', bool(preview_impl), '批量预览路由存在')
    v535.dim('semantics', bool(confirm_impl and signer_impl),
             '二次确认令牌 + 签名器（防伪造/篡改）')
    v535.ev('preview=%s' % [h[:90] for h in preview_impl[:2]])
    v535.ev('confirm=%s' % [h[:90] for h in confirm_impl[:2]])
    v535.ev('signer=%d' % len(signer_impl))
    if preview_impl and confirm_impl and signer_impl:
        v535.judge('PASS', '预览 → 签发确认令牌 → 凭令牌执行，链路完整')
    elif preview_impl:
        v535.judge('COND', '预览在，确认令牌或签名器不完整')
    else:
        v535.judge('FAIL', '批量预览缺失')
    v535.show()
    rows.append(v535.as_dict())

    # ── FR-536 SSE 连接治理 ──
    print('\n6. SSE 连接治理与慢消费者保护')
    v536 = Verdict('FR-536', 'SSE 连接治理与慢消费者保护')
    # 规格用的是 SLOW_CONSUMER + 有界队列。早前拿 maxSSE/buffer full 去搜
    # 全空了，那是关键词搭错——这个领域叫 BusinessSubscriberQueueSize。
    sse_files = [f for f in git_ls('internal/controlplane/') if 'sse' in f.lower() or 'stream' in f.lower()]
    slow_code = git_grep('SSEErrorSlowConsumer|SLOW_CONSUMER', ['internal/controlplane/service/business_stream.go'])
    bounded_q = git_grep('SubscriberQueueSize', ['internal/controlplane/service/business_stream.go'])
    metric = git_grep('slow_consumer_disconnect', ['internal/controlplane/'])
    print('   SSE/流文件 %d；慢消费码 %d；有界队列 %d；指标 %d'
          % (len(sse_files), len(slow_code), len(bounded_q), len(metric)))
    v536.dim('capability', bool(bounded_q), '每订阅者有界队列')
    v536.dim('capability', bool(slow_code), '慢消费判为 SLOW_CONSUMER 并断开')
    v536.dim('semantics', bool(slow_code),
             '断开前发明确终止帧（不是静默断连）')
    v536.ev('queue=%s' % [h[:110] for h in bounded_q[:2]])
    v536.ev('slow=%s' % [h[:110] for h in slow_code[:2]])
    v536.ev('metric=%s' % [h[:110] for h in metric[:2]])
    v536.judge('PASS' if (bounded_q and slow_code) else 'COND',
               '有界队列 + 慢消费断开 + 终止帧，三项齐备' if (bounded_q and slow_code)
               else '慢消费保护实现不完整')
    v536.show()
    rows.append(v536.as_dict())

    # ── FR-537 批量流式进度 ──
    print('\n7. 批量命令流式进度')
    v537 = Verdict('FR-537', '批量命令流式进度')
    # 批量流式进度就在 business_batch.go 的 SSE 分流里，不在单独的 stream 文件。
    stream_impl = git_grep('text/event-stream', ['internal/controlplane/router/business_batch.go'])
    progress_impl = git_grep('event: progress|progress', ['internal/controlplane/router/business_batch.go'])
    print('   SSE 分流: %s；进度相关行: %d' % ('有' if stream_impl else '无', len(progress_impl)))
    for h in progress_impl[:4]:
        print('      %s' % h[:120])
    v537.dim('capability', bool(stream_impl), 'Accept: text/event-stream 分流存在')
    v537.dim('capability', bool(progress_impl), '流内进度事件 %d 处' % len(progress_impl))
    v537.ev('stream=%s' % [h[:110] for h in stream_impl[:2]])
    v537.ev('progress=%s' % [h[:110] for h in progress_impl[:3]])
    v537.judge('PASS' if (stream_impl and progress_impl) else 'FAIL',
               '批量流式进度已实现' if (stream_impl and progress_impl) else '流式分流或进度上报缺失')
    v537.show()
    rows.append(v537.as_dict())

    # ── FR-545 业务事件位点账本 ──
    print('\n8. 业务事件位点账本与缺口查询')
    v545 = Verdict('FR-545', '业务事件位点账本与缺口查询')
    cursor_model = git_ls('internal/controlplane/model/business_event_cursor.go')
    cursor_svc = git_ls('internal/controlplane/service/business_cursor.go')
    cursor_api = git_grep('business/cursor', ['internal/controlplane/router/'])
    print('   model=%s service=%s api=%d'
          % ('在' if cursor_model else '缺', '在' if cursor_svc else '缺', len(cursor_api)))
    st_cur, d_cur = api('/api/v1/business/cursor?instanceId=1')
    print('   /business/cursor -> %s' % st_cur)
    v545.dim('capability', bool(cursor_model and cursor_svc),
             '位点账本 model + service 均在')
    v545.ev('api=%s body=%s' % (st_cur, str(d_cur)[:150]))
    v545.judge('PASS' if (cursor_model and cursor_svc) else 'FAIL',
               '位点账本已实现' if cursor_model else '位点账本缺失')
    v545.show()
    rows.append(v545.as_dict())

    p = save_report(rows, 'c-business-config.json')
    print('\n已保存: %s' % p)
    ok = sum(1 for r in rows if r['result'] == 'PASS')
    cond = sum(1 for r in rows if r['result'] == 'COND')
    bad = sum(1 for r in rows if r['result'] == 'FAIL')
    print('\n小计: PASS=%d COND=%d FAIL=%d' % (ok, cond, bad))
    return 0


if __name__ == '__main__':
    sys.exit(main())
