#!/bin/bash
# 从 proto/worker.proto 生成 Go gRPC 代码到 proto/workerpb/
# 需要: protoc, protoc-gen-go, protoc-gen-go-grpc
#
# 生成链口径（2026-10-02 实测核实，ADR-101 后续项②）：
#   1) 生成物一律由工具**重跑**产出，不得手改（含注释）；rawDesc（描述符字节数组）
#      的 md5 是"生成物是否等价"的最硬判据——重跑后必须不变。
#      提取脚本示例：.tmp/protoc-verify/rawdesc_md5.py <worker.pb.go>。
#   2) 本机没有 protoc 时，可用纯 Go 离线链生成（protoc-gen-go v1.34.2 +
#      protocompile，见 .tmp/protogen）。实测：离线链产物与官方 protoc 29.3
#      的产物**逐字节相同**，唯一差异是头部 protoc 版本注释行
#      （`protoc (unknown)` vs `protoc v5.29.3`）——它是生成器元信息，不进 rawDesc，
#      也不影响任何行为；换用哪条链生成都不需要为它补丁生成物。
#   3) 用官方 protoc 时把发行版解压到 .tmp（不要装系统包）：
#      curl -sL -o .tmp/protoc.zip https://github.com/protocolbuffers/protobuf/releases/download/v29.3/protoc-29.3-linux-x86_64.zip
#      unzip -q -o .tmp/protoc.zip -d .tmp/protoc-bin
#      PATH="$PWD/.tmp/protoc-bin/bin:$HOME/go/bin:$PATH" scripts/proto-gen.sh
set -euo pipefail

PROTO_DIR="proto"
OUT_DIR="proto/workerpb"
MODULE="github.com/wcpe/JianManager"

# 清理旧的生成文件
rm -f "${OUT_DIR}/worker.pb.go" "${OUT_DIR}/worker_grpc.pb.go"

protoc \
  --go_out=. --go_opt=module="${MODULE}" \
  --go-grpc_out=. --go-grpc_opt=module="${MODULE}" \
  "${PROTO_DIR}/worker.proto"

echo "proto 代码已生成到 ${OUT_DIR}/"
