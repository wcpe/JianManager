.PHONY: build build-cp build-worker build-jmctl build-web build-bot dev-cp dev-web lint vet test e2e clean proto embed-web embed-install-scripts embed-cfr embed-client-updater embed-worker clear-worker-embed embed-botworker gen-licenses docker dist dist-bin dist-full dist-slim dist-all dist-bin-full dist-bin-slim dist-prep

# Windows 原生终端（PowerShell/cmd）下 GNU make 默认用 cmd.exe 执行 recipe，而本文件 recipe
# 全为 POSIX 命令（mkdir -p / cp -r / sed …），cmd 下会报「命令语法不正确」。检测到
# Git for Windows 的 sh 则强制启用（8.3 短路径 PROGRA~1 避开空格路径坑）；git-bash 内运行不受影响。
# 同时把 Git 的 usr/bin 前置进 PATH：npm 等 sh 包装脚本的 `#!/usr/bin/env bash` 才会命中
# git-bash，而非 PATH 上 system32 的 WSL bash（后者按 WSL 路径体系找不到脚本，报 No such file）。
ifeq ($(OS),Windows_NT)
ifneq ($(wildcard C:/PROGRA~1/Git/usr/bin/sh.exe),)
SHELL := C:/PROGRA~1/Git/usr/bin/sh.exe
export PATH := C:/PROGRA~1/Git/usr/bin;$(PATH)
endif
endif

# 构建所有（含前端嵌入）。gen-licenses 先行，确保许可清单与即将打包的前端一致。
# embed-install-scripts 同步一键安装脚本内嵌副本（FR-080），保持与 canonical scripts/ 一致。
build: gen-licenses build-web embed-web embed-install-scripts build-cp build-worker

# 构建 Control Plane（含嵌入前端）
build-cp:
	go build -o bin/control-plane.exe ./apps/control-plane

# 构建 Worker Node
build-worker:
	go build -o bin/worker.exe ./apps/worker

# 构建 jmctl 紧急控制台 CLI（独立轻量二进制，仅链 daemon 帧协议包，~3.6MB，FR-184/ADR-041）
build-jmctl:
	go build -o bin/jmctl.exe ./apps/jmctl

# 构建前端（FR-283：pnpm workspace，主应用在 apps/control-plane-web）
build-web:
	pnpm --filter control-plane-web build

# 扫描三源依赖与许可证，生成 apps/control-plane-web/public/licenses.json（FR-135，开源许可页 /licenses 数据源）。
# 需 pnpm install / bot-worker npm install 已完成；Go 侧需 go-licenses（go install github.com/google/go-licenses@latest），
# 缺失时回退 go list 启发式（见脚本）。输出确定性（按来源+包名排序、不含时间戳），构建期再生、无依赖变更不产生 diff。
gen-licenses:
	node scripts/gen-licenses.mjs

# 将前端构建产物复制到嵌入目录（go:embed 目标目录不变，仅源路径随 FR-283 迁移）
embed-web:
	mkdir -p internal/controlplane/embed/dist
	cp -r apps/control-plane-web/dist/* internal/controlplane/embed/dist/

# 同步 Worker 一键安装脚本内嵌副本（FR-080，见 ADR-020 §2 CP 静态托管）。
# canonical 真源在 scripts/install-worker.{sh,ps1}（随发布分发 / 手动拷贝）；本目标把它们复制到
# CP 内嵌目录，go:embed 在 go build 时拉入二进制，使 `curl <cp>/install-worker.sh` 可拉。
# 内嵌副本已入库（保证 fresh checkout 即可 build），install_scripts_test 守护两者字节一致防漂移。
embed-install-scripts:
	mkdir -p internal/controlplane/embed/install-scripts
	cp scripts/install-worker.sh internal/controlplane/embed/install-scripts/install-worker.sh
	cp scripts/install-worker.ps1 internal/controlplane/embed/install-scripts/install-worker.ps1

# 构建客户端 OTA 更新器两件套 jar 并注入 CP 内嵌目录（FR-107 运营方接入指引，可选）。
# 需 client-updater 可构建（toolchain 解析 Java 8）。不跑此目标时 CP 不捆绑更新器 jar，
# 接入指引页下载按钮显示「未内嵌」，不影响其它构建。
embed-client-updater:
	cd client-updater && ./gradlew :wedge:jar :updater-core:jar
	mkdir -p internal/controlplane/embed/client-updater
	cp client-updater/wedge/build/libs/wedge-*.jar internal/controlplane/embed/client-updater/wedge.jar || { echo "错误：客户端更新器楔子 jar 构建产物缺失" >&2; exit 1; }
	cp client-updater/updater-core/build/libs/updater-core-*.jar internal/controlplane/embed/client-updater/updater-core.jar || { echo "错误：客户端 updater-core jar 构建产物缺失" >&2; exit 1; }
	bash scripts/verify-client-updater-embed.sh internal/controlplane/embed/client-updater/wedge.jar internal/controlplane/embed/client-updater/updater-core.jar

# 下载并校验 CFR 反编译器 jar 注入 Worker 内嵌目录（FR-075 反编译，可选；#14）。
# 内容靠 SHA-256 pin 校验（不信传输通道，只信内容指纹）；版本/指纹与 decompiler/cfr.go 常量一致。
# 不跑此目标时 Worker 不捆绑 CFR，首次反编译回退到数据根缓存 / 按需下载（联网）。
embed-cfr:
	mkdir -p internal/worker/embed/cfr
	curl -fsSL -o internal/worker/embed/cfr/cfr.jar https://repo1.maven.org/maven2/org/benf/cfr/0.152/cfr-0.152.jar
	echo "f686e8f3ded377d7bc87d216a90e9e9512df4156e75b06c655a16648ae8765b2  internal/worker/embed/cfr/cfr.jar" | sha256sum -c -

# ── 发布产物交叉编译（windows+linux，对齐 .github/workflows/release.yml build job）──
# 纯 Go（SQLite 用 glebarez 纯 Go 驱动、无 CGO）+ CGO_ENABLED=0，任意宿主（含 Windows）可交叉编译全平台产物。
# 命名与版本注入与 CI 同式：dist/<组件>-<os>-<arch>[.exe]，-X internal/version.Version（ADR-036）。
# VERSION 默认读 internal/version/version.go 当前值，可覆盖：make dist VERSION=1.0.0。
# ServerProbe 不再作为 CP 内嵌物；版本库运行时从来源同步并落既有 CAS，Worker 从 CP 本地拉取。
# client-updater 内嵌物另见 embed-client-updater。
VERSION ?= $(shell sed -n 's/^var Version = "\(.*\)"/\1/p' internal/version/version.go)
DIST_LDFLAGS = -s -w -X github.com/wcpe/JianManager/internal/version.Version=$(VERSION)

# 打包 bot-worker dist 注入 CP 内嵌目录（FR-308/ADR-070）：Worker 经 gRPC 自愈拉取，
# bot 能力不再依赖手工拷贝 dist。产物不入库（目录 .gitignore 占位）；
# 不跑此目标时 CP 不内嵌 bot-worker，Worker 回退本地已有 dist，不影响其它构建。
embed-botworker: build-bot
	go run ./scripts/embed-botworker.go --src apps/bot-worker/dist --out internal/controlplane/embed/botworker --version $(VERSION)

# 交叉编译两平台 Worker 并注入 CP 内嵌目录（FR-278/ADR-062）：CP 随身自带与自身版本一致的
# Worker，一键安装/节点升级不出网。产物与 manifest 不入库（目录 .gitignore 占位）；
# 不跑此目标时 CP 不内嵌 Worker，worker-assets 回退 缓存/远程 链路，不影响其它构建。
embed-worker:
	mkdir -p internal/controlplane/embed/worker
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(DIST_LDFLAGS)" -o internal/controlplane/embed/worker/worker-windows-amd64.exe ./apps/worker
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(DIST_LDFLAGS)" -o internal/controlplane/embed/worker/worker-linux-amd64 ./apps/worker
	go run ./scripts/embed-worker-manifest.go --dir internal/controlplane/embed/worker --version $(VERSION)

# 清除 CP 内嵌 Worker 字节（仅留 .gitignore），供 slim 档构建。go:embed 空目录仍可编译，运行时降级「未内嵌」。
clear-worker-embed:
	@mkdir -p internal/controlplane/embed/worker
	@find internal/controlplane/embed/worker -type f ! -name '.gitignore' -delete 2>/dev/null || true
	@test -f internal/controlplane/embed/worker/.gitignore || printf '%s\n' '*' '!.gitignore' > internal/controlplane/embed/worker/.gitignore

# 两档共用的前端/botworker 等内嵌（不含 Worker 内嵌）。
dist-prep: gen-licenses build-web embed-web embed-install-scripts embed-botworker

# dist 默认 = 完整版（兼容旧习惯）：CP 内嵌双平台 Worker。
dist: dist-full

# 完整版：~100MB+ CP，节点安装/升级可优先从 CP 内嵌物化，无需外网拉 Worker。
dist-full: dist-prep embed-worker dist-bin-full

# 精简版：CP 不内嵌 Worker（体积约减 40MB+）；Worker 走独立产物或本机已有文件/镜像。
dist-slim: dist-prep clear-worker-embed dist-bin-slim

# 一次产出 full + slim CP 与独立 worker（先 full 再 slim，避免 embed 目录互相污染）。
dist-all: dist-full dist-slim

# 完整版二进制命名（ADR-036）：control-plane / worker × linux|windows amd64。
dist-bin-full:
	mkdir -p dist
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(DIST_LDFLAGS)" -o dist/control-plane-windows-amd64.exe ./apps/control-plane
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(DIST_LDFLAGS)" -o dist/worker-windows-amd64.exe ./apps/worker
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(DIST_LDFLAGS)" -o dist/control-plane-linux-amd64 ./apps/control-plane
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(DIST_LDFLAGS)" -o dist/worker-linux-amd64 ./apps/worker

# 精简版仅多出 control-plane-slim-*；独立 worker 与 full 共用命名（同一 worker 二进制）。
# 若已跑过 dist-full，worker 已在 dist/；此处仍重编 worker 以支持「只 make dist-slim」。
dist-bin-slim:
	mkdir -p dist
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(DIST_LDFLAGS)" -o dist/control-plane-slim-windows-amd64.exe ./apps/control-plane
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(DIST_LDFLAGS)" -o dist/worker-windows-amd64.exe ./apps/worker
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(DIST_LDFLAGS)" -o dist/control-plane-slim-linux-amd64 ./apps/control-plane
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(DIST_LDFLAGS)" -o dist/worker-linux-amd64 ./apps/worker

# 兼容旧目标名：dist-bin = 完整版四件套。
dist-bin: dist-bin-full

# 构建 Bot Worker
build-bot:
	cd apps/bot-worker && npm run build

# 开发模式启动 Control Plane
dev-cp:
	go run ./apps/control-plane --dev

# 开发模式启动前端
dev-web:
	pnpm --filter control-plane-web dev

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
# 故依赖已构建的 bot-worker dist；需预先 `make install`（含 apps/bot-worker npm i）。
e2e: build-bot
	go test -tags=e2e -run TestE2E ./internal/e2e/ -v -timeout 240s

# Go 测试覆盖率
test-cover:
	go test -race -cover ./...

# 前端类型检查 + lint
lint-web:
	cd apps/control-plane-web && npx tsc --noEmit && pnpm lint

# Bot Worker 类型检查 + lint
lint-bot:
	cd apps/bot-worker && npx tsc --noEmit && npm run lint

# 生成 protobuf 代码（module 选项确保按 go_package 写入 proto/workerpb，而非嵌套 github.com 目录）
proto:
	protoc --go_out=. --go_opt=module=github.com/wcpe/JianManager \
		--go-grpc_out=. --go-grpc_opt=module=github.com/wcpe/JianManager \
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
	rm -rf bin/ dist/ apps/control-plane-web/dist/ apps/ui-museum/dist/ apps/bot-worker/dist/ data/ internal/controlplane/embed/dist/

# 安装所有依赖（前端经 pnpm workspace 一次装齐；bot-worker 保持 npm 自管，FR-283/ADR-064）
install:
	go mod tidy
	pnpm install
	cd apps/bot-worker && npm install
