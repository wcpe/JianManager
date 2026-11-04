#!/usr/bin/env python3
"""Search 首屏 p95 测量仪（FR-498 / 250ms 门）。

口径来源（不得放宽）：
  - 门限 250ms 不在此脚本内定义，也不在此调整；脚本只测量与如实报告。
  - 输入固定：60 源 × 30 行/秒、10k 预热、30 秒生成、8 查询槽 / 3 秒回溯 / limit 200。
  - 规格明确「首屏 FAST Pending 不充作完整 Search 样本」（worker-log-platform-contract
    §「6/30 steady / recovery」节）。因此本脚本把每次响应按 coverage 分成两桶：
      fast     —— 返回了数据但 coverage.complete=false（含 partial 原因）
      complete —— coverage.complete=true
    两桶分别给分位数，**绝不把 fast 桶的数字当作完整 Search 成绩**。

不做什么：
  - 不改产品代码、不改门限、不缩短窗口、不跳过用例。
  - 不在宿主高负载时给出任何结论（由调用方保证窗口，脚本只记录 loadavg 供复核）。
"""
from __future__ import annotations

import argparse
import json
import os
import statistics
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, field
from pathlib import Path

RETENTION_SLOTS = 8
BACKTRACK_SEC = 3.0
LIMIT = 200


@dataclass
class Sample:
    slot: int
    started_ms: float
    elapsed_ms: float
    status: int
    items: int
    coverage_complete: bool | None
    partial_reasons: list[str]
    error: str = ""

    @property
    def bucket(self) -> str:
        if self.error or self.status != 200:
            return "error"
        if self.coverage_complete is True:
            return "complete"
        return "fast"


@dataclass
class Result:
    samples: list[Sample] = field(default_factory=list)
    lock: threading.Lock = field(default_factory=threading.Lock)

    def add(self, s: Sample) -> None:
        with self.lock:
            self.samples.append(s)


def quantile(values: list[float], q: float) -> float | None:
    """线性插值分位数；样本为空返回 None（不补零，避免把「没有样本」伪装成 0ms）。"""
    if not values:
        return None
    v = sorted(values)
    if len(v) == 1:
        return v[0]
    pos = q * (len(v) - 1)
    lo = int(pos)
    hi = min(lo + 1, len(v) - 1)
    frac = pos - lo
    return v[lo] * (1 - frac) + v[hi] * frac


def summarize(values: list[float]) -> dict:
    if not values:
        return {"count": 0, "p50_ms": None, "p95_ms": None, "p99_ms": None,
                "max_ms": None, "min_ms": None, "mean_ms": None}
    return {
        "count": len(values),
        "p50_ms": round(quantile(values, 0.50), 3),
        "p95_ms": round(quantile(values, 0.95), 3),
        "p99_ms": round(quantile(values, 0.99), 3),
        "max_ms": round(max(values), 3),
        "min_ms": round(min(values), 3),
        "mean_ms": round(statistics.fmean(values), 3),
    }


def one_query(base: str, token: str, slot: int, now_ms: float,
              timeout: float) -> Sample:
    """发一次首屏查询：回溯 3 秒、limit 200。"""
    frm = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime((now_ms - BACKTRACK_SEC * 1000) / 1000))
    to = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(now_ms / 1000))
    qs = urllib.parse.urlencode({"limit": LIMIT, "from": frm, "to": to})
    url = f"{base}/api/v1/logs/federation/search?{qs}"
    req = urllib.request.Request(url, headers={"Authorization": f"Bearer {token}"})
    t0 = time.perf_counter()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = resp.read()
            elapsed = (time.perf_counter() - t0) * 1000
            status = resp.status
    except urllib.error.HTTPError as e:
        elapsed = (time.perf_counter() - t0) * 1000
        return Sample(slot, t0 * 1000, elapsed, e.code, 0, None, [],
                      error=f"HTTP {e.code}")
    except Exception as e:  # noqa: BLE001 - 测量仪必须记录任何失败而不是崩掉
        elapsed = (time.perf_counter() - t0) * 1000
        return Sample(slot, t0 * 1000, elapsed, 0, 0, None, [],
                      error=f"{type(e).__name__}: {e}")

    items = 0
    complete = None
    reasons: list[str] = []
    try:
        doc = json.loads(body)
        items = len(doc.get("items") or [])
        cov = doc.get("coverage") or {}
        complete = cov.get("complete")
        reasons = list(cov.get("partial_reasons") or [])
    except Exception:  # noqa: BLE001 - 响应不是 JSON 也属可测事实
        pass
    return Sample(slot, t0 * 1000, elapsed, status, items, complete, reasons)


def worker_loop(slot: int, base: str, token: str, args, stop_at: float,
                res: "Result", pacer: "Pacer") -> None:
    """单槽循环：每轮先向共享节流器取许可，再发一次首屏查询。

    为什么要节流：CP 的日志路由分类预算是 10 请求/秒、容量 20（ratelimit.go 注释，
    2026-10-09 生产实证）。8 槽无节制重试会瞬间打满桶、把绝大部分样本变成 429，
    那样测到的是限流而非查询延迟。
    验收语义是「8 查询槽 / 3 秒」——即每槽每 3 秒一轮，本循环按此节奏发送，
    不额外加快也不放慢。
    """
    while time.time() < stop_at:
        pacer.wait()
        if time.time() >= stop_at:
            break
        now_ms = time.time() * 1000
        s = one_query(base, token, slot, now_ms, args.timeout)
        # 429 不是延迟样本：记入 error 桶并在下一轮按 Retry-After 语义退避。
        if s.status == 429:
            pacer.penalize()
        res.add(s)


class Pacer:
    """按「8 槽 / 每槽 3 秒一轮」的验收语义放行请求的共享节流器。

    速率推导：8 槽 / 3 秒 = 每秒约 2.67 次请求，低于日志分类预算 10/秒，
    因此在正常延迟下不会触发限流；若确实被限流，说明系统在超预期的速率上拒绝，
    这本身是要如实记录的事实（记入 error 桶），不能靠调大参数掩盖。
    """

    def __init__(self, slots: int, period_sec: float) -> None:
        self._interval = period_sec / max(1, slots)
        self._next = time.monotonic()
        self._mu = threading.Lock()

    def wait(self) -> None:
        with self._mu:
            now = time.monotonic()
            if self._next < now:
                self._next = now
            target = self._next
            self._next += self._interval
        delay = target - time.monotonic()
        if delay > 0:
            time.sleep(delay)

    def penalize(self) -> None:
        """被限流时整体后移一个完整周期，模拟客户端正确退避。"""
        with self._mu:
            self._next = max(self._next, time.monotonic() + BACKTRACK_SEC)


def main() -> int:
    ap = argparse.ArgumentParser(description="Search 首屏 p95 测量仪")
    ap.add_argument("--base", required=True, help="CP base URL，如 http://127.0.0.1:28080")
    ap.add_argument("--token", help="Bearer token（或用 --token-file）")
    ap.add_argument("--token-file", help="从文件读 token，与 --token 二选一")
    ap.add_argument("--duration", type=float, default=30.0, help="测量时长（秒）")
    ap.add_argument("--slots", type=int, default=RETENTION_SLOTS, help="并发槽数（固定 8）")
    ap.add_argument("--timeout", type=float, default=30.0, help="单请求超时（秒）")
    ap.add_argument("--out", required=True, help="结果 JSON 输出路径")
    args = ap.parse_args()

    if args.slots != RETENTION_SLOTS:
        print(f"拒绝执行：验收口径固定 {RETENTION_SLOTS} 槽，收到 {args.slots}。"
              f"改槽数会改变测量口径，必须另行裁定。", file=sys.stderr)
        return 2

    token = args.token
    if args.token_file:
        token = Path(args.token_file).read_text().strip()

    loadavg = Path("/proc/loadavg").read_text().split()[:3]
    cores = os.cpu_count()
    res = Result()
    stop_at = time.time() + args.duration
    pacer = Pacer(args.slots, BACKTRACK_SEC)

    threads = [threading.Thread(target=worker_loop,
                                args=(i, args.base, token, args, stop_at, res, pacer),
                                daemon=True)
               for i in range(args.slots)]
    t_start = time.time()
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    wall = time.time() - t_start

    buckets: dict[str, list[float]] = {"fast": [], "complete": [], "error": []}
    for s in res.samples:
        buckets[s.bucket].append(s.elapsed_ms)

    # 错误明细（不进分位数，单独列出，不得混入 p95）
    err_detail: dict[str, int] = {}
    for s in res.samples:
        if s.error:
            key = s.error.split(":")[0][:60]
            err_detail[key] = err_detail.get(key, 0) + 1

    reasons_all: dict[str, int] = {}
    for s in res.samples:
        for r in s.partial_reasons:
            reasons_all[r] = reasons_all.get(r, 0) + 1

    out = {
        "schema": "search-firstscreen-p95/v1",
        "generated_at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
        "envelope": {
            "gate_ms": 250,
            "gate_unchanged": True,
            "slots": args.slots,
            "backtrack_sec": BACKTRACK_SEC,
            "limit": LIMIT,
            "duration_sec": args.duration,
            "wall_sec": round(wall, 3),
            "host_cores": cores,
            "host_loadavg_at_start": loadavg,
            "note": "脚本不改门限；FAST 桶不得充作完整 Search 成绩",
        },
        "buckets": {k: summarize(v) for k, v in buckets.items()},
        "total_samples": len(res.samples),
        "error_detail": err_detail,
        "partial_reasons": reasons_all,
        # 逐样本保留：只有分位数无法判断长尾形态（是持续慢还是极少数超长），
        # 也无法与宿主争用时间线对齐。此前缺此字段导致无法分析 p50=10.6ms 而 max=1692ms 的成因。
        "samples": [
            {
                "slot": s.slot,
                "started_ms": round(s.started_ms, 3),
                "elapsed_ms": round(s.elapsed_ms, 3),
                "status": s.status,
                "items": s.items,
                "bucket": s.bucket,
                "coverage_complete": s.coverage_complete,
                "partial_reasons": s.partial_reasons,
                "error": s.error,
            }
            for s in sorted(res.samples, key=lambda x: x.started_ms)
        ],
        "verdict": {},
    }
    # 判决：只用 complete 桶判达/未达；样本量为 0 时明确「不可判定」，绝不当通过。
    comp = out["buckets"]["complete"]
    if comp["count"] == 0:
        out["verdict"] = {
            "status": "UNDETERMINED",
            "reason": "完整 Search 样本为 0（全部落在 fast 桶或错误桶），按规格不得判达标",
            "fast_p95_ms": out["buckets"]["fast"]["p95_ms"],
        }
    else:
        p95 = comp["p95_ms"]
        out["verdict"] = {
            "status": "PASS" if p95 <= 250 else "FAIL",
            "metric": "complete_search_firstscreen_p95_ms",
            "value": p95,
            "gate_ms": 250,
        }

    Path(args.out).write_text(json.dumps(out, ensure_ascii=False, indent=2), encoding="utf-8")
    print(json.dumps(out, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
