#!/usr/bin/env python3
"""F 批：审批执行器与 Bot 收敛验收。

覆盖：审批执行器/事务回执/漂移零副作用（FR-491）、跑测会话先收敛后补足（FR-472）。

本批以**真实 go test 跑出的用例与结果**为主要证据，
不是只查符号存在。
"""
import json
import os
import re
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fr_acceptance import api, Verdict, save_report

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
PKG = './internal/controlplane/service/'


def git_grep(pattern, paths=None):
    cmd = ['git', 'grep', '-nE', pattern, 'HEAD']
    if paths:
        cmd += ['--'] + paths
    r = subprocess.run(cmd, cwd=REPO, capture_output=True, text=True)
    return [l for l in r.stdout.splitlines() if l]


def run_go_tests(pattern):
    """跑 go test 并解析每个用例结果。返回 (passed_list, failed_list)。"""
    cmd = ['go', 'test', PKG, '-run', pattern, '-v']
    r = subprocess.run(cmd, cwd=REPO, capture_output=True, text=True, timeout=600)
    out = r.stdout + r.stderr
    passed = re.findall(r'^--- PASS: (\S+)', out, re.M)
    failed = re.findall(r'^--- FAIL: (\S+)', out, re.M)
    return passed, failed, out


def main():
    rows = []
    print('=== F 批：审批与 Bot 收敛 ===\n')

    # ── 先跑审批测试（真跑，不是查符号）──
    print('1. 审批执行器 / 事务回执 / 漂移零副作用')
    ap_pass, ap_fail, ap_out = run_go_tests('Approval|Executor|Drift|FinishCAS')
    print('   审批相关用例：%d 过 / %d 败' % (len(ap_pass), len(ap_fail)))

    v491 = Verdict('FR-491', '审批执行器与漂移零副作用')

    # 关键语义逐条核：这些用例名本身就是断言内容
    key_checks = {
        '票据篡改不执行': 'TestExecutor_TamperedPayloadDoesNotExecute',
        '目标漂移不执行': 'TestExecutor_TargetDriftDoesNotExecute',
        '未注册动作 fail-closed': 'TestExecutor_UnregisteredActionFailsClosed',
        '一票只执行一次': 'TestExecutor_OnlyOncePerTicket',
        '适配器报错记 failed': 'TestExecutor_AdapterErrorMarksFailed',
        '超时记 unverified（不谎称成功）': 'TestExecutor_TimeoutMarksUnverified',
        '并发批准只一个赢': 'TestApproval_ConcurrentApproveOnlyOneWins',
        '过期票不可批准': 'TestApproval_ExpiredCannotBeApproved',
        'Agent 永不能批准': 'TestApproval_AgentCanNeverDecide',
        '载荷篡改可检出': 'TestApproval_PayloadTamperDetected',
    }
    found = {}
    for label, name in key_checks.items():
        ok = name in ap_pass
        bad = name in ap_fail
        found[label] = (ok, bad)
        mark = 'ok' if ok else ('FAIL' if bad else '未命中')
        print('      %-28s %s' % (label, mark))

    all_key_ok = all(ok for ok, _ in found.values())
    none_failed = len(ap_fail) == 0

    v491.dim('capability', all_key_ok, '十条关键语义均有对应用例且通过')
    v491.dim('semantics', none_failed, '%d 条审批用例零失败' % len(ap_pass))
    v491.dim('semantics', found['超时记 unverified（不谎称成功）'][0],
             '超时不被当成成功（单独可观测）')

    # 真机：审批中心可用。注意必须在 judge 之前记，否则这个条件不参与裁定
    # （dim 只负责展示，judge 才是决定 verdict 的地方）。
    st_ap, d_ap = api('/api/v1/approvals?pageSize=1')
    print('   真机 /approvals: %s' % st_ap)
    v491.ev('live approvals endpoint=%s' % st_ap)
    v491.dim('capability', st_ap == 200, '审批中心接口可用')
    live_ok = st_ap == 200

    v491.ev('passed=%d failed=%d' % (len(ap_pass), len(ap_fail)))
    v491.ev('key checks=%s' % json.dumps({k: v[0] for k, v in found.items()}, ensure_ascii=False))

    if all_key_ok and none_failed and live_ok:
        v491.judge('PASS',
                   '%d 条审批用例全绿，十条关键语义逐条命中：篡改/漂移/未注册动作均不执行，'
                   '一票一次性，超时记 unverified，Agent 永不能自批；真机审批中心可用' % len(ap_pass))
    elif all_key_ok and none_failed:
        v491.judge('COND', '测试全绿但真机审批接口不可用')
    elif none_failed:
        v491.judge('COND', '测试全绿但部分关键语义未找到对应用例')
    else:
        v491.judge('FAIL', '有审批用例失败：%s' % ap_fail[:3])
    v491.show()
    rows.append(v491.as_dict())

    # ── FR-472 Bot 会话收敛 ──
    print('\n2. 跑测会话先收敛后补足 / 有在线 Bot 不回收')
    bt_pass, bt_fail, bt_out = run_go_tests('Reclaim|Zombie|BotDesired')
    print('   Bot 收敛用例：%d 过 / %d 败' % (len(bt_pass), len(bt_fail)))

    v472 = Verdict('FR-472', '跑测会话先收敛后补足')

    bt_checks = {
        '有在线 Bot 不回收（epoch 分歧但健康）': 'TestBotReclaim_EpochDivergenceHealthyBotKept',
        '真缺席才回收': 'TestBotReclaim_EpochDivergenceAbsentBotReclaimed',
        '新 Bot 首轮不回收（宽限）': 'TestBotReclaim_NewBotFirstSweepNotReclaimed',
        'V1 Bot 不自动处置': 'TestBotReclaim_V1BotNotAutoDisposed',
        '多分片无快照时保守': 'TestBotReclaim_MultiShardWithoutSnapshotIsConservative',
        '补足不重置在位 Bot': 'TestBotReclaim_RefillDoesNotResetBotPresentInWorker',
        '小缺口不抖动': 'TestBotReclaim_RefillGapThresholdSuppressesTinyGap',
        '退避避免无条件重发': 'TestBotReclaim_RefillBackoffPreventsUnconditionalResend',
    }
    bt_found = {}
    for label, name in bt_checks.items():
        ok = name in bt_pass
        bad = name in bt_fail
        bt_found[label] = (ok, bad)
        print('      %-34s %s' % (label, 'ok' if ok else ('FAIL' if bad else '未命中')))

    # 代码层：停滞阈值确实存在且可配
    threshold = git_grep('zombieIdleThreshold|botZombieSessionIdleThreshold',
                         ['internal/controlplane/service/bot_reclaim.go'])
    zero_online = git_grep('在线 Bot 数为 0|onlineBots',
                           ['internal/controlplane/service/bot_reclaim.go'])
    print('   停滞阈值符号 %d；零在线判据 %d' % (len(threshold), len(zero_online)))

    bt_all_ok = all(ok for ok, _ in bt_found.values())
    bt_none_failed = len(bt_fail) == 0
    v472.dim('capability', bt_all_ok, '八条关键场景均有对应用例且通过')
    v472.dim('semantics', bool(zero_online), '收敛以「在线 Bot 数为 0」为前置（非仅看时长）')
    v472.dim('capability', bool(threshold), '停滞阈值有符号且可由设置项覆盖')
    v472.dim('semantics', bt_none_failed, '%d 条用例零失败' % len(bt_pass))
    v472.ev('passed=%d failed=%d' % (len(bt_pass), len(bt_fail)))
    v472.ev('checks=%s' % json.dumps({k: v[0] for k, v in bt_found.items()}, ensure_ascii=False))
    v472.ev('threshold=%s' % [h[:110] for h in threshold[:2]])
    v472.ev('zeroOnline=%s' % [h[:110] for h in zero_online[:2]])

    if bt_all_ok and bt_none_failed and zero_online:
        v472.judge('PASS',
                   '%d 条用例全绿；「先收敛后补足」与「有在线 Bot 不回收」两条核心语义都有直接断言，'
                   '且覆盖 V1 不自动处置、多分片保守、退避防抖动等边界' % len(bt_pass))
    elif bt_none_failed:
        v472.judge('COND', '测试全绿但部分关键场景未找到对应用例')
    else:
        v472.judge('FAIL', '有收敛用例失败：%s' % bt_fail[:3])
    v472.show()
    rows.append(v472.as_dict())

    p = save_report(rows, 'f-approval-bot.json')
    print('\n已保存: %s' % p)
    ok = sum(1 for r in rows if r['result'] == 'PASS')
    cond = sum(1 for r in rows if r['result'] == 'COND')
    bad = sum(1 for r in rows if r['result'] == 'FAIL')
    print('\n小计: PASS=%d COND=%d FAIL=%d' % (ok, cond, bad))
    return 0


if __name__ == '__main__':
    sys.exit(main())
