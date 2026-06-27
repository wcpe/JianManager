.PHONY: build build-cp build-worker build-jmctl build-web build-bot dev-cp dev-web lint vet test e2e clean proto embed-web embed-install-scripts embed-probe embed-cfr embed-client-updater gen-licenses docker

# 构建所有（含前端嵌入）。gen-licenses 先行，确保许可清单与即将打包的前端一致。
# embed-install-scripts 同步一键安装脚本内嵌副本（FR-080），保持与 canonical scripts/ 一致。
build: gen-licenses build-web embed-web embed-install-scripts build-cp build-worker

# 构建 Control Plane（含嵌入前端）
build-cp:
	go build -o bin/control-plane.exe ./cmd/control-plane

# 构建 Worker Node
build-worker:
	go build -o bin/worker.exe ./cmd/worker

# 构建 jmctl 紧急控制台 CLI（独立轻量二进制，仅链 daemon 帧协议包，~3.6MB，FR-184/ADR-041）
build-jmctl:
	go build -o bin/jmctl.exe ./cmd/jmctl

# 构建前端
build-web:
	cd web && npm run build

# 扫描三源依赖与许可证，生成 web/public/licenses.json（FR-135，开源许可页 /licenses 数据源）。
# 需 web/bot-worker 已 npm install；Go 侧需 go-licenses（go install github.com/google/go-licenses@latest），
# 缺失时回退 go list 启发式（见脚本）。输出确定性（按来源+包名排序、不含时间戳），构建期再生、无依赖变更不产生 diff。
gen-licenses:
	node scripts/gen-licenses.mjs

# 将前端构建产物复制到嵌入目录
embed-web:
	mkdir -p internal/controlplane/embed/dist
	cp -r web/dist/* internal/controlplane/embed/dist/

# 同步 Worker 一键安装脚本内嵌副本（FR-080，见 ADR-020 §2 CP 静态托管）。
# canonical 真源在 scripts/install-worker.{sh,ps1}（随发布分发 / 手动拷贝）；本目标把它们复制到
# CP 内嵌目录，go:embed 在 go build 时拉入二进制，使 `curl <cp>/install-worker.sh` 可拉。
# 内嵌副本已入库（保证 fresh checkout 即可 build），install_scripts_test 守护两者字节一致防漂移。
embed-install-scripts:
	mkdir -p internal/controlplane/embed/install-scripts
	cp scripts/install-worker.sh internal/controlplane/embed/install-scripts/install-worker.sh
	cp scripts/install-worker.ps1 internal/controlplane/embed/install-scripts/install-worker.ps1

# 构建 ServerProbe 探针 jar 并注入 CP 内嵌目录（FR-010 建服自动部署，可选）。
# 需 JDK 21（设 JAVA_HOME 指向 JDK21）+ 子模块已拉取（git submodule update --init）。
# 不跑此目标时 CP 不捆绑探针，建服时自动部署优雅跳过，不影响其它构建。
embed-probe:
	cd third_party/ServerProbe && ./gradlew :plugin:jar :plugin:taboolibMainTask
	mkdir -p internal/controlplane/embed/probe
	cp third_party/ServerProbe/plugin/build/libs/ServerProbe-*.jar internal/controlplane/embed/probe/ServerProbe.jar

# 构建客户端 OTA 更新器两件套 jar 并注入 CP 内嵌目录（FR-107 运营方接入指引，可选）。
# 需 client-updater 可构建（toolchain 解析 Java 8）。不跑此目标时 CP 不捆绑更新器 jar，
# 接入指引页下载按钮显示「未内嵌」，不影响其它构建。
embed-client-updater:
	cd client-updater && ./gradlew :wedge:jar :updater-core:jar
	mkdir -p internal/controlplane/embed/client-updater
	cp client-updater/wedge/build/libs/wedge-*.jar internal/controlplane/embed/client-updater/wedge.jar
	cp client-updater/updater-core/build/libs/updater-core-*.jar internal/controlplane/embed/client-updater/updater-core.jar

# 下载并校验 CFR 反编译器 jar 注入 Worker 内嵌目录（FR-075 反编译，可选；#14）。
# 内容靠 SHA-256 pin 校验（不信传输通道，只信内容指纹）；版本/指纹与 decompiler/cfr.go 常量一致。
# 不跑此目标时 Worker 不捆绑 CFR，首次反编译回退到数据根缓存 / 按需下载（联网）。
embed-cfr:
	mkdir -p internal/worker/embed/cfr
	curl -fsSL -o internal/worker/embed/cfr/cfr.jar https://repo1.maven.org/maven2/org/benf/cfr/0.152/cfr-0.152.jar
	echo "f686e8f3ded377d7bc87d216a90e9e9512df4156e75b06c655a16648ae8765b2  internal/worker/embed/cfr/cfr.jar" | sha256sum -c -

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
	rm -rf bin/ web/dist/ bot-worker/dist/ data/ internal/controlplane/embed/dist/

# 安装所有依赖
install:
	go mod tidy
	cd web && npm install
	cd bot-worker && npm install
