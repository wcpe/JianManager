#!/usr/bin/env bash
# FR-410 部署脚本的本地回归测试：使用伪 SSH/SCP 与临时目标目录，不连接真实主机。
set -euo pipefail

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

# Git Bash 对目录符号链接的替换与 GNU/Linux 不同；部署目标和 CI 都是 Linux，
# 因此在 Windows 开发机上通过已安装的 WSL 执行同一份测试。
case "$(uname -s)" in
    MINGW*|MSYS*)
        if [ "${JM_VERSIONED_TEST_WSL:-0}" != 1 ]; then
            command -v wsl.exe >/dev/null 2>&1 || {
                echo '测试失败: Windows 上运行此测试需要 WSL 的 GNU/Linux 语义' >&2
                exit 1
            }
            WINDOWS_ROOT=$(cygpath -w "$ROOT_DIR")
            WSL_ROOT=$(wsl.exe -e wslpath -a "$WINDOWS_ROOT" | tr -d '\r')
            exec wsl.exe -e env JM_VERSIONED_TEST_WSL=1 bash -lc "cd \"$WSL_ROOT\" && bash scripts/deploy-versioned.test.sh"
        fi
        ;;
esac

TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/jm-versioned-deploy.XXXXXX")
FAKE_ROOT="$TEST_ROOT/remote"
FAKE_HOME="$FAKE_ROOT/home/deploy"
FAKE_BIN="$TEST_ROOT/bin"
TEST_SCRIPT_DIR="$TEST_ROOT/scripts"

cleanup() {
    if [ "${KEEP_TEST_ROOT:-0}" = 1 ]; then
        printf '%s\n' "保留测试目录: $TEST_ROOT" >&2
        return
    fi
    rm -rf "$TEST_ROOT"
}
trap cleanup EXIT

fail() {
    echo "测试失败: $*" >&2
    exit 1
}

require_file() {
    [ -f "$1" ] || fail "缺少文件: $1"
}

require_lf_line_endings() {
    if LC_ALL=C grep -q "$(printf '\r')" "$1"; then
        fail "脚本包含 CRLF 换行，Linux 不能直接执行: $1"
    fi
}

assert_eq() {
    [ "$1" = "$2" ] || fail "期望 [$1]，实际 [$2]"
}

assert_contains() {
    grep -F -- "$2" "$1" >/dev/null || fail "文件 $1 未包含: $2"
}

assert_not_exists() {
    [ ! -e "$1" ] && [ ! -L "$1" ] || fail "不应存在: $1"
}

assert_mode_600() {
    local path=$1
    assert_contains "$FAKE_ROOT/chmod.log" "600 $path"
    case "$(uname -s)" in
        Linux) assert_eq 600 "$(stat -c '%a' "$path")" ;;
    esac
}

version_of() {
    "$1" --version
}

version_dir_count() {
    find "$1/versions" -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d ' '
}

find_version_dir() {
    for directory in "$1"/versions/*; do
        [ -d "$directory" ] || continue
        if [ "$(version_of "$directory/$2")" = "$3" ]; then
            printf '%s\n' "$directory"
            return 0
        fi
    done
    return 1
}

write_mock_commands() {
    mkdir -p "$FAKE_ROOT/tmp" "$FAKE_HOME" "$FAKE_BIN" "$FAKE_ROOT/state"

    cat > "$FAKE_BIN/ssh" <<'MOCK'
#!/bin/sh
set -eu
while [ "$#" -gt 0 ]; do
    case "$1" in
        -o|-p|-i) shift 2 ;;
        -*) shift ;;
        *) shift; break ;;
    esac
done

run_remote() {
    HOME="$FAKE_REMOTE_HOME" \
    USER=deploy LOGNAME=deploy \
    PATH="$FAKE_BIN:/usr/bin:/bin" \
    FAKE_REMOTE_ROOT="$FAKE_REMOTE_ROOT" \
    FAKE_CURL_FAIL="${FAKE_CURL_FAIL:-0}" \
    FAKE_SYSTEMCTL_FAIL_START_ONCE="${FAKE_SYSTEMCTL_FAIL_START_ONCE:-0}" \
    /bin/sh "$@"
}

if [ "$#" -eq 1 ] && [ "$1" = "sh -s" ]; then
    run_remote -s
elif [ "$#" -eq 1 ]; then
    run_remote -c "$1"
else
    run_remote -c "$*"
fi
MOCK

    cat > "$FAKE_BIN/scp" <<'MOCK'
#!/bin/sh
set -eu
while [ "$#" -gt 0 ]; do
    case "$1" in
        -o|-P|-i) shift 2 ;;
        -q|-*) shift ;;
        *) source="$1"; shift; break ;;
    esac
done
[ "$#" -eq 1 ] || exit 2
destination=${1#*:}
mkdir -p "$(dirname "$destination")"
cp "$source" "$destination"
chmod 0644 "$destination"
MOCK

    cat > "$FAKE_BIN/systemctl" <<'MOCK'
#!/bin/sh
set -eu
scope=system
if [ "${1:-}" = "--user" ]; then
    scope=user
    shift
fi
command=${1:-}
shift || true
if [ "$command" = is-active ] && [ "${1:-}" = --quiet ]; then
    shift
fi
service=${1:-}
service=${service%.service}
printf '%s %s %s\n' "$scope" "$command" "$service" >> "$FAKE_REMOTE_ROOT/systemctl.log"
state="$FAKE_REMOTE_ROOT/state/$service"
case "$command" in
    daemon-reload|enable) exit 0 ;;
    stop)
        printf '%s\n' inactive > "$state"
        exit 0
        ;;
    start|restart)
        if [ "${FAKE_SYSTEMCTL_FAIL_START_ONCE:-0}" = 1 ] && [ ! -f "$FAKE_REMOTE_ROOT/state/start-failure-used" ]; then
            : > "$FAKE_REMOTE_ROOT/state/start-failure-used"
            printf '%s\n' inactive > "$state"
        else
            printf '%s\n' active > "$state"
        fi
        exit 0
        ;;
    is-active)
        if [ "${FAKE_SYSTEMCTL_FAIL_AFTER_FIRST_ACTIVE:-0}" = 1 ]; then
            if [ -f "$FAKE_REMOTE_ROOT/state/active-check-used" ]; then
                printf '%s\n' inactive > "$state"
                printf '%s\n' inactive
                exit 3
            fi
            : > "$FAKE_REMOTE_ROOT/state/active-check-used"
        fi
        if [ -f "$state" ] && [ "$(cat "$state")" = active ]; then
            printf '%s\n' active
            exit 0
        fi
        printf '%s\n' inactive
        exit 3
        ;;
    *) exit 0 ;;
esac
MOCK

    cat > "$FAKE_BIN/loginctl" <<'MOCK'
#!/bin/sh
case "${1:-}" in
    show-user) printf '%s\n' yes ;;
    enable-linger) exit 0 ;;
    *) exit 0 ;;
esac
MOCK

    cat > "$FAKE_BIN/curl" <<'MOCK'
#!/bin/sh
[ "${FAKE_CURL_FAIL:-0}" = 1 ] && exit 1
exit 0
MOCK

    cat > "$FAKE_BIN/sleep" <<'MOCK'
#!/bin/sh
exit 0
MOCK

    cat > "$FAKE_BIN/journalctl" <<'MOCK'
#!/bin/sh
printf '%s\n' '伪造日志'
MOCK

    cat > "$FAKE_BIN/id" <<'MOCK'
#!/bin/sh
case "${1:-}" in
    -u) printf '%s\n' 1000 ;;
    -un) printf '%s\n' deploy ;;
    *) /usr/bin/id "$@" ;;
esac
MOCK

    cat > "$FAKE_BIN/uname" <<'MOCK'
#!/bin/sh
case "${1:-}" in
    -m) printf '%s\n' x86_64 ;;
    -s) printf '%s\n' Linux ;;
    *) /usr/bin/uname "$@" ;;
esac
MOCK

    cat > "$FAKE_BIN/mktemp" <<'MOCK'
#!/bin/sh
set -eu
if [ "${1:-}" = -d ]; then
    template=${2:-jm-deploy.XXXXXX}
    template=${template##*/}
    /usr/bin/mktemp -d "$FAKE_REMOTE_ROOT/tmp/$template"
    exit 0
fi
exec /usr/bin/mktemp "$@"
MOCK

    cat > "$FAKE_BIN/chmod" <<'MOCK'
#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_REMOTE_ROOT/chmod.log"
exec /usr/bin/chmod "$@"
MOCK

    chmod +x "$FAKE_BIN"/*
}

prepare_scripts_under_test() {
    mkdir -p "$TEST_SCRIPT_DIR/lib"
    for script in deploy-cp.sh deploy-worker.sh rollback-cp.sh rollback-worker.sh install-worker.sh; do
        require_lf_line_endings "$ROOT_DIR/scripts/$script"
        cp "$ROOT_DIR/scripts/$script" "$TEST_SCRIPT_DIR/$script"
        chmod +x "$TEST_SCRIPT_DIR/$script"
    done
    require_lf_line_endings "$ROOT_DIR/scripts/lib/versioned-user-layout.sh"
    cp "$ROOT_DIR/scripts/lib/versioned-user-layout.sh" "$TEST_SCRIPT_DIR/lib/versioned-user-layout.sh"
    chmod +x "$TEST_SCRIPT_DIR/lib/versioned-user-layout.sh"
}

new_remote() {
    rm -rf "$FAKE_ROOT"
    mkdir -p "$FAKE_ROOT/tmp" "$FAKE_HOME" "$FAKE_ROOT/state"
    : > "$FAKE_ROOT/systemctl.log"
    : > "$FAKE_ROOT/chmod.log"
    rm -f "$FAKE_ROOT/state/start-failure-used"
}

make_artifact() {
    local path=$1
    local version=$2
    cat > "$path" <<EOF
#!/bin/sh
if [ "\${1:-}" = "--version" ]; then
    printf '%s\\n' '$version'
    exit 0
fi
data="\${JIANMANAGER_DATA_DIR:-}"
if [ -n "\$data" ]; then
    mkdir -p "\$data/etc"
    : > "\$data/etc/node-identity.json"
fi
exit 0
EOF
    chmod +x "$path"
}

make_legacy_artifact_without_version() {
    local path=$1
    cat > "$path" <<'EOF'
#!/bin/sh
exit 1
EOF
    chmod +x "$path"
}

run_cp() {
    local dist=$1
    local log=$2
    PATH="$FAKE_BIN:$PATH" \
    FAKE_BIN="$FAKE_BIN" FAKE_REMOTE_ROOT="$FAKE_ROOT" FAKE_REMOTE_HOME="$FAKE_HOME" \
    JM_SSH_HOST=mock JM_SSH_USER=deploy JM_SERVICE_SCOPE=user JM_DIST_DIR="$dist" \
    sh "$TEST_SCRIPT_DIR/deploy-cp.sh" > "$log" 2>&1
}

run_worker() {
    local dist=$1
    local install=$2
    local log=$3
    PATH="$FAKE_BIN:$PATH" \
    FAKE_BIN="$FAKE_BIN" FAKE_REMOTE_ROOT="$FAKE_ROOT" FAKE_REMOTE_HOME="$FAKE_HOME" \
    JM_SSH_HOST=mock JM_SSH_USER=deploy JM_SERVICE_SCOPE=user JM_DIST_DIR="$dist" \
    JM_INSTALL_DIR="$install" \
    sh "$TEST_SCRIPT_DIR/deploy-worker.sh" > "$log" 2>&1
}

run_rollback_cp() {
    local target=$1
    local log=$2
    PATH="$FAKE_BIN:$PATH" \
    FAKE_BIN="$FAKE_BIN" FAKE_REMOTE_ROOT="$FAKE_ROOT" FAKE_REMOTE_HOME="$FAKE_HOME" \
    JM_SSH_HOST=mock JM_SSH_USER=deploy JM_SERVICE_SCOPE=user \
    sh "$TEST_SCRIPT_DIR/rollback-cp.sh" "$target" > "$log" 2>&1
}

run_rollback_worker() {
    local install=$1
    local target=$2
    local log=$3
    PATH="$FAKE_BIN:$PATH" \
    FAKE_BIN="$FAKE_BIN" FAKE_REMOTE_ROOT="$FAKE_ROOT" FAKE_REMOTE_HOME="$FAKE_HOME" \
    JM_SSH_HOST=mock JM_SSH_USER=deploy JM_SERVICE_SCOPE=user JM_INSTALL_DIR="$install" \
    sh "$TEST_SCRIPT_DIR/rollback-worker.sh" "$target" > "$log" 2>&1
}

expect_failure() {
    if "$@"; then
        fail "命令应失败却成功: $*"
    fi
}

test_required_scripts() {
    require_file "$ROOT_DIR/scripts/rollback-cp.sh"
    require_file "$ROOT_DIR/scripts/rollback-worker.sh"
}

test_cp_first_install_and_legacy_migration() {
    local dist="$TEST_ROOT/cp-dist"
    local install="$FAKE_HOME/jianmanager-cp"
    mkdir -p "$dist"
    make_artifact "$dist/control-plane-linux-amd64" 'v9.2.0'

    new_remote
    run_cp "$dist" "$TEST_ROOT/cp-first.log"
    require_file "$install/service.env"
    assert_mode_600 "$install/service.env"
    assert_contains "$install/service.env" 'JIANMANAGER_JWT_SECRET='
    assert_contains "$FAKE_HOME/.config/systemd/user/jianmanager-cp.service" "ExecStart=$install/current/jianmanager-cp"
    assert_contains "$FAKE_HOME/.config/systemd/user/jianmanager-cp.service" "EnvironmentFile=$install/service.env"
    assert_eq v9.2.0 "$(version_of "$install/current/jianmanager-cp")"
    require_file "$install/current/manifest.env"

    new_remote
    mkdir -p "$install/data"
    printf '%s\n' stable-data > "$install/data/preserve.txt"
    printf '%s\n' 'server:' '  port: 18080' > "$install/control-plane.yml"
    make_artifact "$install/jianmanager-cp" 'v9.0.0'
    make_artifact "$install/jianmanager-cp.bak" 'v8.9.0'
    mkdir -p "$FAKE_HOME/.config/systemd/user"
    cat > "$FAKE_HOME/.config/systemd/user/jianmanager-cp.service" <<EOF
[Service]
ExecStart=$install/jianmanager-cp $install/control-plane.yml
EOF
    make_artifact "$dist/control-plane-linux-amd64" 'v9.1.0'
    run_cp "$dist" "$TEST_ROOT/cp-migrate.log"
    assert_eq stable-data "$(cat "$install/data/preserve.txt")"
    assert_not_exists "$install/jianmanager-cp"
    assert_not_exists "$install/jianmanager-cp.bak"
    assert_eq v9.1.0 "$(version_of "$install/current/jianmanager-cp")"
    [ "$(version_dir_count "$install")" -ge 3 ] || fail 'CP 迁移未保留旧二进制、.bak 与新版本'
    [ -n "$(find_version_dir "$install" jianmanager-cp v9.0.0)" ] || fail 'CP 旧二进制未归档'
    [ -n "$(find_version_dir "$install" jianmanager-cp v8.9.0)" ] || fail 'CP .bak 未归档'

    make_artifact "$dist/control-plane-linux-amd64" 'v9.1.1'
    run_cp "$dist" "$TEST_ROOT/cp-second.log"
    assert_eq v9.1.1 "$(version_of "$install/current/jianmanager-cp")"
    [ "$(version_dir_count "$install")" -ge 4 ] || fail 'CP 连续部署未永久保留版本'

    make_artifact "$dist/control-plane-linux-amd64" 'v9.1.2'
    if FAKE_CURL_FAIL=1 run_cp "$dist" "$TEST_ROOT/cp-health-failure.log"; then
        fail 'CP 探活失败时部署应返回失败'
    fi
    assert_eq v9.1.1 "$(version_of "$install/current/jianmanager-cp")"

    local rollback_target before
    rollback_target=$(find_version_dir "$install" jianmanager-cp v9.1.0)
    run_rollback_cp "$(basename "$rollback_target")" "$TEST_ROOT/cp-rollback.log"
    assert_eq v9.1.0 "$(version_of "$install/current/jianmanager-cp")"
    before=$(version_of "$install/current/jianmanager-cp")
    expect_failure run_rollback_cp '../../outside' "$TEST_ROOT/cp-invalid-rollback.log"
    assert_eq "$before" "$(version_of "$install/current/jianmanager-cp")"
    rollback_target=$(find_version_dir "$install" jianmanager-cp v9.0.0)
    printf '%s\n' invalid > "$rollback_target/manifest.env"
    expect_failure run_rollback_cp "$(basename "$rollback_target")" "$TEST_ROOT/cp-bad-manifest.log"
    assert_eq "$before" "$(version_of "$install/current/jianmanager-cp")"
}

test_worker_node2_migration_and_failure_rollback() {
    local dist="$TEST_ROOT/worker-dist"
    local install="$FAKE_HOME/jianmanager-node2"
    mkdir -p "$dist" "$install/data/etc" "$FAKE_HOME/.config/systemd/user"
    printf '%s\n' keep-node-identity > "$install/data/etc/node-identity.json"
    printf '%s\n' worker-config > "$install/data/worker.yml"
    make_artifact "$install/jianmanager-worker" 'v5.0.0'
    make_legacy_artifact_without_version "$install/jianmanager-worker.bak"
    cat > "$FAKE_HOME/.config/systemd/user/jianmanager-worker-node2.service" <<EOF
[Service]
ExecStart=$install/jianmanager-worker
EOF
    make_artifact "$dist/worker-linux-amd64" 'v5.1.0'

    new_remote
    mkdir -p "$install/data/etc" "$FAKE_HOME/.config/systemd/user"
    printf '%s\n' keep-node-identity > "$install/data/etc/node-identity.json"
    printf '%s\n' worker-config > "$install/data/worker.yml"
    make_artifact "$install/jianmanager-worker" 'v5.0.0'
    make_legacy_artifact_without_version "$install/jianmanager-worker.bak"
    cat > "$FAKE_HOME/.config/systemd/user/jianmanager-worker-node2.service" <<EOF
[Service]
ExecStart=$install/jianmanager-worker
EOF
    run_worker "$dist" "$install" "$TEST_ROOT/worker-migrate.log"
    assert_eq keep-node-identity "$(cat "$install/data/etc/node-identity.json")"
    assert_eq worker-config "$(cat "$install/data/worker.yml")"
    assert_not_exists "$install/jianmanager-worker"
    assert_eq v5.1.0 "$(version_of "$install/current/jianmanager-worker")"
    assert_contains "$FAKE_HOME/.config/systemd/user/jianmanager-worker-node2.service" "ExecStart=$install/current/jianmanager-worker"
    assert_contains "$FAKE_ROOT/systemctl.log" 'user stop jianmanager-worker-node2'
    [ "$(version_dir_count "$install")" -ge 3 ] || fail 'Worker 迁移未保留旧二进制、无法报告版本的 .bak 与新版本'
    find "$install/versions" -mindepth 1 -maxdepth 1 -type d -name 'unknown--*' | grep -q . || fail 'Worker 无版本参数的 .bak 未以 unknown 迁移归档'

    make_artifact "$dist/worker-linux-amd64" 'v5.1.1'
    run_worker "$dist" "$install" "$TEST_ROOT/worker-second.log"
    assert_eq v5.1.1 "$(version_of "$install/current/jianmanager-worker")"
    [ "$(version_dir_count "$install")" -ge 3 ] || fail 'Worker 连续部署未永久保留版本'

    rm -f "$FAKE_ROOT/state/start-failure-used"
    make_artifact "$dist/worker-linux-amd64" 'v5.1.2'
    if FAKE_SYSTEMCTL_FAIL_START_ONCE=1 run_worker "$dist" "$install" "$TEST_ROOT/worker-failure.log"; then
        fail 'Worker 启动状态异常时部署应返回失败'
    fi
    assert_eq v5.1.1 "$(version_of "$install/current/jianmanager-worker")"

    rm -f "$FAKE_ROOT/state/active-check-used"
    make_artifact "$dist/worker-linux-amd64" 'v5.1.3'
    if FAKE_SYSTEMCTL_FAIL_AFTER_FIRST_ACTIVE=1 run_worker "$dist" "$install" "$TEST_ROOT/worker-delayed-failure.log"; then
        fail 'Worker 延迟状态异常时部署应返回失败'
    fi
    assert_eq v5.1.1 "$(version_of "$install/current/jianmanager-worker")"

    local rollback_target before
    rollback_target=$(find_version_dir "$install" jianmanager-worker v5.1.0)
    run_rollback_worker "$install" "$(basename "$rollback_target")" "$TEST_ROOT/worker-rollback.log"
    assert_eq v5.1.0 "$(version_of "$install/current/jianmanager-worker")"
    before=$(version_of "$install/current/jianmanager-worker")
    expect_failure run_rollback_worker "$install" '../../outside' "$TEST_ROOT/worker-invalid-rollback.log"
    assert_eq "$before" "$(version_of "$install/current/jianmanager-worker")"
    rollback_target=$(find_version_dir "$install" jianmanager-worker v5.0.0)
    printf '%s\n' invalid > "$rollback_target/manifest.env"
    expect_failure run_rollback_worker "$install" "$(basename "$rollback_target")" "$TEST_ROOT/worker-bad-manifest.log"
    assert_eq "$before" "$(version_of "$install/current/jianmanager-worker")"
}

main() {
    write_mock_commands
    test_required_scripts
    prepare_scripts_under_test
    test_cp_first_install_and_legacy_migration
    test_worker_node2_migration_and_failure_rollback
    printf '%s\n' '通过: FR-410 版本化 user-systemd 部署脚本测试'
}

main "$@"
