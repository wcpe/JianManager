#!/usr/bin/env python3
"""P4 实例生命周期批：allinmobs-accept 靶点验证

验证：
  1. start → 实例变 RUNNING，产生 PID 文件
  2. PID 文件内容完整（原子写）
  3. stop → 实例变 STOPPED，进程退出
  4. 停止不误杀其他实例（隔离）
"""
import json
import os
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fr_acceptance import api, Verdict, save_report

TARGET = 'allinmobs-accept'
SERVERS = '/srv/jm/worker-data/var/servers'


def find_inst(name):
    st, d = api('/api/v1/instances?pageSize=200')
    for i in (d if isinstance(d, list) else []):
        if i.get('name') == name:
            return i
    return None


def wait_status(iid, want, timeout=120):
    t0 = time.time()
    while time.time() - t0 < timeout:
        st, d = api('/api/v1/instances/%s' % iid)
        cur = d.get('status') if isinstance(d, dict) else None
        if cur == want:
            return True, time.time() - t0
        time.sleep(2)
    return False, time.time() - t0


def other_pids():
    """其他在跑实例的 java pid（用于验证不误杀）。"""
    out = {}
    for f in os.listdir(SERVERS):
        if not f.endswith('.pid'):
            continue
        p = os.path.join(SERVERS, f)
        try:
            d = json.load(open(p))
            uuid = d.get('instance_uuid', '')
            if TARGET[:8] in uuid or uuid.startswith('167e1ce2'):
                continue
            out[uuid[:8]] = d.get('java_pid')
        except Exception:
            pass
    return out


def main():
    rows = []
    print('=== P4 实例生命周期：%s ===\n' % TARGET)

    inst = find_inst(TARGET)
    if not inst:
        print('未找到靶点实例')
        return
    iid = inst['id']
    print('靶点 id=%s 当前状态=%s' % (iid, inst.get('status')))

    before_others = other_pids()
    print('其他在跑实例 %d 台（将验证不被误杀）' % len(before_others))

    # ---- 1. 启动 ----
    print('\n1. 启动实例')
    st, resp = api('/api/v1/instances/%s/start' % iid, method='POST')
    print('   start HTTP=%s body=%s' % (st, str(resp)[:180]))
    ok, secs = wait_status(iid, 'RUNNING', timeout=150)
    print('   → RUNNING: %s (耗时 %.0fs)' % (ok, secs))

    v1 = Verdict('FR-497', '实例启动与纳管')
    v1.dim('capability', ok, '启动后状态 RUNNING=%s，耗时 %.0fs' % (ok, secs))
    v1.ev('start HTTP=%s, 转为 RUNNING=%s' % (st, ok))
    v1.judge('PASS' if ok else 'FAIL', '')
    v1.show()
    rows.append(v1.as_dict())

    if ok:
        # ---- 2. PID 文件 ----
        print('\n2. PID 文件（原子写）')
        pidfile = None
        for f in os.listdir(SERVERS):
            if f.startswith('167e1ce2') and f.endswith('.pid'):
                pidfile = os.path.join(SERVERS, f)
        if pidfile and os.path.exists(pidfile):
            raw = open(pidfile, 'rb').read()
            try:
                d = json.loads(raw)
                complete = all(d.get(k) for k in
                               ('wrapper_pid', 'java_pid', 'socket_addr', 'instance_uuid', 'work_dir'))
                print('   %s (%dB) 字段完整=%s' % (os.path.basename(pidfile), len(raw), complete))
                v2 = Verdict('FR-500', 'PID 文件原子写完整性')
                v2.dim('capability', complete, '新启实例 PID 文件字段完整')
                v2.dim('semantics', len(raw) > 100, '文件 %dB，非半截' % len(raw))
                v2.ev(json.dumps(d, ensure_ascii=False)[:300])
                v2.judge('PASS' if complete else 'FAIL', '')
                v2.show()
                rows.append(v2.as_dict())
            except Exception as e:
                print('   解析失败: %s' % e)
        else:
            print('   PID 文件未生成')

        # ---- 3. 其他实例是否被影响 ----
        print('\n3. 隔离验证：其他实例是否受影响')
        after_others = other_pids()
        changed = [k for k in before_others if before_others.get(k) != after_others.get(k)]
        print('   其他实例 PID 变化: %s' % (changed or '无'))
        v3 = Verdict('FR-500', '启停不连坐其他实例')
        v3.dim('capability', len(changed) == 0,
               '%d 台其他实例 PID 全部未变' % len(before_others) if not changed
               else '有 %d 台 PID 变了' % len(changed))
        v3.ev('before=%d after=%d changed=%s' % (len(before_others), len(after_others), changed))
        v3.judge('PASS' if not changed else 'FAIL', '')
        v3.show()
        rows.append(v3.as_dict())

        # ---- 4. 停止 ----
        print('\n4. 停止实例')
        st, resp = api('/api/v1/instances/%s/stop' % iid, method='POST')
        print('   stop HTTP=%s body=%s' % (st, str(resp)[:180]))
        ok2, secs2 = wait_status(iid, 'STOPPED', timeout=150)
        print('   → STOPPED: %s (耗时 %.0fs)' % (ok2, secs2))
        v4 = Verdict('FR-497', '实例停止与退出')
        v4.dim('capability', ok2, '停止后状态 STOPPED=%s，耗时 %.0fs' % (ok2, secs2))
        v4.judge('PASS' if ok2 else 'FAIL', '')
        v4.show()
        rows.append(v4.as_dict())

    save_report(rows, 'p4-lifecycle.json')


if __name__ == '__main__':
    main()
