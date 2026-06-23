# API Spec — FR-078 Docker 容器化实例 + 镜像管理

> 关联 FR: FR-078 | 关联 ADR: ADR-019 | 权威定义见 `docs/API.md`（HTTP）与 `proto/worker.proto`（gRPC），本文件为 feature 视角汇总。

## HTTP（浏览器 ↔ Control Plane）

### 实例创建扩展

`POST /api/v1/instances`（既有，FR-005）新增字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `image` | string | docker 模式容器镜像引用（如 `itzg/minecraft-server:latest`）。`processType=docker` 时必填；其它模式忽略。默认 Docker Hub，本地缺失时启动前自动拉取。 |

docker 模式语义：宿主端口（FR-032 端口池）映射到容器内端口（MC 约定 25565，tcp+udp），工作目录 bind-mount 到容器 `/data`。

### 节点级 Docker 镜像管理（仅平台管理员）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/nodes/:id/docker/images` | 列出节点本机镜像 |
| POST | `/api/v1/nodes/:id/docker/images/pull` | 拉取镜像 `{ "image": "…" }` |
| POST | `/api/v1/nodes/:id/docker/images/remove` | 删除镜像 `{ "image": "…", "force": false }` |

- List 响应：`[{ id, tags[], sizeBytes, created }]`
- 错误码：`503 NODE_OFFLINE`（节点未连接）；`422 DOCKER_UNAVAILABLE`（节点未装/未运行 Docker，仅 List）；`502 DOCKER_OP_FAILED`（拉取/删除失败）

> 删除用 POST 而非 DELETE：镜像引用含 `/` 与 `:`，作为路径参数会破坏路由匹配，故放请求体。

## gRPC（Control Plane ↔ Worker Node）

### CreateInstanceRequest 扩展

```protobuf
string image = 14;                       // docker 模式镜像引用
repeated PortMapping port_mappings = 15; // 容器端口↔宿主端口

message PortMapping {
  int32 container_port = 1; // 容器内端口（MC 约定 25565）
  int32 host_port = 2;      // 宿主端口（FR-032 端口池）
  string protocol = 3;      // tcp（默认）/ udp
}
```

### 镜像管理 RPC（FR-078）

```protobuf
rpc ListImages(ListImagesRequest) returns (ListImagesResponse);
rpc PullImage(PullImageRequest) returns (PullImageResponse);
rpc RemoveImage(RemoveImageRequest) returns (RemoveImageResponse);

message ImageInfo { string id = 1; repeated string tags = 2; int64 size_bytes = 3; int64 created = 4; }
message ListImagesResponse { repeated ImageInfo images = 1; bool docker_available = 2; string error = 3; }
message PullImageRequest { string image = 1; }
message RemoveImageRequest { string image = 1; bool force = 2; }
```

- `ListImages` 在节点 Docker 不可用时回 `docker_available=false`，CP 据此返回 `422 DOCKER_UNAVAILABLE`。
- CP 不直连 Docker，所有镜像/容器操作经 Worker 委托（守架构边界，ADR-002/019）。
