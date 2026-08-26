#!/bin/sh
# 用户级 systemd 版本目录的远端公共函数。调用方负责在可信临时目录中 source 本文件。

versioned_sha256_file() {
    [ -f "$1" ] || return 1
    sha256sum "$1" | awk 'NR == 1 { print $1 }'
}

versioned_binary_version() {
    binary=$1
    [ -f "$binary" ] || return 1
    chmod +x "$binary" || return 1
    if command -v timeout >/dev/null 2>&1; then
        raw=$(timeout 10 "$binary" --version 2>/dev/null) || return 1
    else
        raw=$("$binary" --version 2>/dev/null) || return 1
    fi
    raw=$(printf '%s\n' "$raw" | sed -n '1p')
    safe=$(printf '%s' "$raw" | tr -cd 'A-Za-z0-9._+-')
    [ -n "$safe" ] || return 1
    printf '%s\n' "$safe"
}

versioned_legacy_binary_version() {
    versioned_binary_version "$1" || printf '%s\n' unknown
}

versioned_reserve_version_dir() {
    install_dir=$1
    version=$2
    sha256=$3
    case "$version" in ''|*[!A-Za-z0-9._+-]*) return 1 ;; esac
    [ "${#sha256}" -eq 64 ] || return 1
    case "$sha256" in *[!0123456789abcdef]*) return 1 ;; esac

    timestamp=$(date -u +%Y%m%dT%H%M%SZ)
    short_sha=$(printf '%s' "$sha256" | cut -c1-12)
    base="$version--$timestamp--$short_sha"
    release=$base
    sequence=2
    while [ -e "$install_dir/versions/$release" ]; do
        release="$base-$sequence"
        sequence=$((sequence + 1))
    done
    mkdir -p "$install_dir/versions/$release" || return 1
    VERSIONED_RELEASE_NAME=$release
    VERSIONED_RELEASE_DIR="$install_dir/versions/$release"
}

versioned_write_manifest() {
    release_dir=$1
    version=$2
    sha256=$3
    case "$version" in ''|*[!A-Za-z0-9._+-]*) return 1 ;; esac
    [ "${#sha256}" -eq 64 ] || return 1
    case "$sha256" in *[!0123456789abcdef]*) return 1 ;; esac
    umask 077
    {
        printf 'version=%s\n' "$version"
        printf 'sha256=%s\n' "$sha256"
        printf 'created_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    } > "$release_dir/manifest.env"
    chmod 600 "$release_dir/manifest.env"
}

versioned_verify_manifest() {
    release_dir=$1
    binary_name=$2
    manifest="$release_dir/manifest.env"
    [ -f "$manifest" ] && [ -f "$release_dir/$binary_name" ] || return 1

    manifest_version=
    manifest_sha256=
    manifest_created_at=
    seen_version=0
    seen_sha256=0
    seen_created_at=0
    while IFS='=' read -r key value || [ -n "$key" ]; do
        case "$key" in
            version) [ "$seen_version" -eq 0 ] || return 1; manifest_version=$value; seen_version=1 ;;
            sha256) [ "$seen_sha256" -eq 0 ] || return 1; manifest_sha256=$value; seen_sha256=1 ;;
            created_at) [ "$seen_created_at" -eq 0 ] || return 1; manifest_created_at=$value; seen_created_at=1 ;;
            *) return 1 ;;
        esac
    done < "$manifest"
    [ "$seen_version" -eq 1 ] && [ "$seen_sha256" -eq 1 ] && [ "$seen_created_at" -eq 1 ] || return 1
    case "$manifest_version" in ''|*[!A-Za-z0-9._+-]*) return 1 ;; esac
    [ "${#manifest_sha256}" -eq 64 ] || return 1
    case "$manifest_sha256" in *[!0123456789abcdef]*) return 1 ;; esac
    case "$manifest_created_at" in ????-??-??T??:??:??Z) : ;; *) return 1 ;; esac
    actual_sha256=$(versioned_sha256_file "$release_dir/$binary_name") || return 1
    [ "$actual_sha256" = "$manifest_sha256" ]
}

versioned_archive_legacy_binary() {
    install_dir=$1
    binary_name=$2
    legacy_path=$3
    [ -f "$legacy_path" ] || return 1
    sha256=$(versioned_sha256_file "$legacy_path") || return 1
    version=$(versioned_legacy_binary_version "$legacy_path") || return 1
    versioned_reserve_version_dir "$install_dir" "$version" "$sha256" || return 1
    cp -p "$legacy_path" "$VERSIONED_RELEASE_DIR/$binary_name" || return 1
    chmod +x "$VERSIONED_RELEASE_DIR/$binary_name"
    versioned_write_manifest "$VERSIONED_RELEASE_DIR" "$version" "$sha256" || return 1
}

versioned_switch_current() {
    install_dir=$1
    release_name=$2
    case "$release_name" in ''|.|..|*[!A-Za-z0-9._+-]*) return 1 ;; esac
    [ -d "$install_dir/versions/$release_name" ] || return 1
    temporary_link="$install_dir/.current-$$"
    rm -f "$temporary_link"
    ln -s "versions/$release_name" "$temporary_link" || return 1
    mv -Tf "$temporary_link" "$install_dir/current"
}

versioned_restore_current() {
    install_dir=$1
    previous_current=$2
    case "$previous_current" in versions/*) release_name=${previous_current#versions/} ;; *) return 1 ;; esac
    versioned_switch_current "$install_dir" "$release_name"
}
