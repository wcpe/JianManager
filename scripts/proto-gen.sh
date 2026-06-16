#!/bin/bash
# 从 proto/worker.proto 生成 Go gRPC 代码到 proto/workerpb/
# 需要: protoc, protoc-gen-go, protoc-gen-go-grpc
set -euo pipefail

PROTO_DIR="proto"
OUT_DIR="proto/workerpb"
MODULE="github.com/wxys233/JianManager"

# 清理旧的生成文件
rm -f "${OUT_DIR}/worker.pb.go" "${OUT_DIR}/worker_grpc.pb.go"

protoc \
  --go_out=. --go_opt=module="${MODULE}" \
  --go-grpc_out=. --go-grpc_opt=module="${MODULE}" \
  "${PROTO_DIR}/worker.proto"

echo "proto 代码已生成到 ${OUT_DIR}/"
