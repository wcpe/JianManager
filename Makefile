.PHONY: build build-cp build-worker build-web build-bot dev-cp dev-web lint vet test e2e clean proto embed-web docker

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

# E2E 端到端测试（需启动真实 CP + Worker 进程）
# 全链路用例（FR-043）会 spawn 真实 bot-worker(Node) 并让真实 Bot 进服，
# 故依赖已构建的 bot-worker dist；需预先 `make install`（含 bot-worker npm i）。
e2e: build-bot
	go test -tags=e2e -run TestE2E ./internal/e2e/ -v -timeout 240s

# Go 测试覆盖率
test-cover:
	go test -race -cover ./...

# 前端类型检查 + lint
lint-web:
	cd web && npx tsc --noEmit && npm run lint

# Bot Worker 类型检查 + lint
lint-bot:
	cd bot-worker && npx tsc --noEmit && npm run lint

# 生成 protobuf 代码（module 选项确保按 go_package 写入 proto/workerpb，而非嵌套 github.com 目录）
proto:
	protoc --go_out=. --go_opt=module=github.com/wxys233/JianManager \
		--go-grpc_out=. --go-grpc_opt=module=github.com/wxys233/JianManager \
		proto/worker.proto

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
