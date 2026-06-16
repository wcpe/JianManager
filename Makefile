.PHONY: build build-cp build-worker build-web build-bot dev-cp dev-web lint vet test clean proto embed-web docker

# 构建所有（含前端嵌入）
build: build-web embed-web build-cp build-worker

# 构建 Control Plane（含嵌入前端）
build-cp:
	go build -o bin/control-plane.exe ./cmd/control-plane

# 构建 Worker Node
build-worker:
	go build -o bin/worker.exe ./cmd/worker

# 构建前端
build-web:
	cd web && npm run build

# 将前端构建产物复制到嵌入目录
embed-web:
	mkdir -p internal/controlplane/embed/dist
	cp -r web/dist/* internal/controlplane/embed/dist/

# 构建 Bot Worker
build-bot:
	cd bot-worker && npm run build

# 开发模式启动 Control Plane
dev-cp:
	go run ./cmd/control-plane --dev

# 开发模式启动前端
dev-web:
	cd web && npm run dev

# Go 静态分析
vet:
	go vet ./...

# Go lint
lint:
	golangci-lint run

# Go 测试
test:
	go test -race ./...

# Go 测试覆盖率
test-cover:
	go test -race -cover ./...

# 前端类型检查 + lint
lint-web:
	cd web && npx tsc --noEmit && npm run lint

# Bot Worker 类型检查 + lint
lint-bot:
	cd bot-worker && npx tsc --noEmit && npm run lint

# 生成 protobuf 代码
proto:
	protoc --go_out=. --go-grpc_out=. proto/worker.proto

# Docker 构建
docker:
	docker compose build

docker-up:
	docker compose up -d

docker-down:
	docker compose down

# 清理
clean:
	rm -rf bin/ web/dist/ bot-worker/dist/ data/ internal/controlplane/embed/dist/

# 安装所有依赖
install:
	go mod tidy
	cd web && npm install
	cd bot-worker && npm install
