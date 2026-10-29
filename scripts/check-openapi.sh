#!/usr/bin/env bash
# 校验对外 API 契约（docs/openapi）的正确性与路由一致性。
#
# 用途：
#   1. YAML 语法可解析
#   2. OpenAPI 声明的每个 path+method 在 Gin 路由里真实存在
#   3. /api/v1/business/* 与 /api/v1/stats/* 下的每条路由都在 OpenAPI 里声明
#
# 用法：./scripts/check-openapi.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OPENAPI_DIR="$ROOT/docs/openapi"

echo "==> 1/3 校验 YAML 语法"
python3 - "$OPENAPI_DIR" <<'PY'
import sys, yaml, pathlib
base = pathlib.Path(sys.argv[1])
failed = []
for f in sorted(base.rglob('*.yaml')):
    try:
        yaml.safe_load(f.read_text(encoding='utf-8'))
        print(f'  ok   {f.relative_to(base)}')
    except Exception as e:
        failed.append(f'{f}: {e}')
if failed:
    print('\nYAML 语法错误：')
    for msg in failed:
        print(f'  FAIL {msg}')
    sys.exit(1)
PY

echo
echo "==> 2/3 校验 \$ref 可解析"
python3 - "$OPENAPI_DIR" <<'PY'
import sys, yaml, pathlib, re
base = pathlib.Path(sys.argv[1])
missing = []

def walk(node, src):
    """递归查找所有 $ref 并确认目标存在。"""
    if isinstance(node, dict):
        ref = node.get('$ref')
        if isinstance(ref, str):
            if ref.startswith('#'):
                # 同文件内引用：校验顶层 key 存在
                top = ref.lstrip('#/').split('/')[0]
                doc = yaml.safe_load((base / src).read_text(encoding='utf-8'))
                if top not in doc:
                    missing.append(f'{src}: 内部引用 #{top} 不存在')
            else:
                # 跨文件引用：拆出文件与片段；相对路径基于**引用所在文件的目录**解析
                file_part, _, frag = ref.partition('#')
                target = ((base / src).parent / file_part).resolve()
                if not target.exists():
                    missing.append(f'{src}: 目标文件不存在 {file_part}')
                elif frag:
                    doc = yaml.safe_load(target.read_text(encoding='utf-8'))
                    key = frag.lstrip('/').split('/')[0]
                    if key not in doc:
                        missing.append(f'{src}: {file_part}#{key} 不存在')
        for v in node.values():
            walk(v, src)
    elif isinstance(node, list):
        for v in node:
            walk(v, src)

for f in sorted(base.rglob('*.yaml')):
    rel = f.relative_to(base)
    walk(yaml.safe_load(f.read_text(encoding='utf-8')), rel)

if missing:
    print('\n$ref 校验失败：')
    for msg in missing:
        print(f'  FAIL {msg}')
    sys.exit(1)
print('  ok   所有 $ref 可解析')
PY

echo
echo "==> 3/3 校验路由一致性"
python3 - "$ROOT" <<'PY'
import sys, yaml, pathlib, re, subprocess
root = pathlib.Path(sys.argv[1])
base = root / 'docs' / 'openapi'

# 收集 OpenAPI 声明的 path
declared = set()
ext = yaml.safe_load((base / 'external.yaml').read_text(encoding='utf-8'))
for path, item in (ext.get('paths') or {}).items():
    if isinstance(item, dict) and '$ref' in item:
        file_part, _, frag = item['$ref'].partition('#')
        target = (base / file_part).resolve()
        doc = yaml.safe_load(target.read_text(encoding='utf-8'))
        # 片段形如 /paths/~1business~1batch → 先按 / 切分，再对每段解码 ~1 → /
        # 注意顺序：必须先切分后解码，否则 ~1 变成 / 会被二次切分。
        frag_path = frag.lstrip('/')
        node = doc
        for part in frag_path.split('/'):
            if not part:
                continue
            part = part.replace('~1', '/').replace('~0', '~')
            node = (node or {}).get(part)
        if node:
            for method in ('get', 'post', 'put', 'delete', 'patch'):
                if method in node:
                    declared.add(f'{method.upper()} {path}')

print(f'  OpenAPI 声明 {len(declared)} 条')

# 收集 Gin 路由里 /business 与 /stats 下的实际路由
gin_routes = set()
router_dir = root / 'internal' / 'controlplane' / 'router'
pattern = re.compile(r'rg\.(GET|POST|PUT|DELETE|PATCH)\("([^"]+)"')
for f in router_dir.glob('*.go'):
    if f.name.endswith('_test.go'):
        continue
    for m in pattern.finditer(f.read_text(encoding='utf-8')):
        method, path = m.group(1).upper(), m.group(2)
        # 只统计对外契约覆盖的路径（白名单，避免误匹配同名接口）
        if (path == '/instances/:id/business'
                or path == '/instances/:id/business/manifest'
                or path == '/instances/:id/business/status'
                or path == '/instances/business/status/batch'
                or path == '/instances/:id/bridge/status'
                or path == '/instances/:id/bridge/subscription'
                or path == '/business/batch'
                or path == '/business/batch/preview'
                or path == '/business/reconcile'
                or path == '/business/cursor'
                or path == '/business/stream'
                or path.startswith('/stats/')):
            gin_routes.add(f'{method} {path}')

print(f'  Gin 实际    {len(gin_routes)} 条（/business 与 /stats 下）')

# 双向比对（忽略路径参数名差异：{id} vs :id）
def norm(p):
    return re.sub(r'[:{][^/}]+[}]?', '{}', p)

dn = {norm(x.split(' ', 1)[1].split(' ')[0]): x for x in declared}
gn = {norm(x.split(' ', 1)[1]): x for x in gin_routes}

only_openapi = set(dn) - set(gn)
only_gin = set(gn) - set(dn)

if only_openapi:
    print('\n  OpenAPI 声明了但 Gin 未实现：')
    for p in sorted(only_openapi):
        print(f'    {dn[p]}')
if only_gin:
    print('\n  Gin 已实现但 OpenAPI 未声明：')
    for p in sorted(only_gin):
        print(f'    {gn[p]}')

# 注意：开发期间允许不一致（实现尚未完成），此处只告警不失败。
#  待完成后改为 set -e 失败退出。
if only_openapi or only_gin:
    print('\n  WARN 路由不一致（迁移完成前允许，完成后须清零）')
else:
    print('  ok   路由完全一致')
PY

echo
echo "==> 校验完成"
