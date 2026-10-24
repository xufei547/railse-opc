.PHONY: web build test gofmt lint release-check e2e

# 工具版本钉死在这里；CI（.github/workflows）与发布 Action 用同一个值，升级时一起改。
# 两个工具都只经 go run 使用，不进 go.mod。
GOLANGCI_LINT_VERSION := v2.15.0
GORELEASER_VERSION := v2.19.0

# 构建网页到 internal/hub/webui/dist（go:embed 把它嵌进二进制；产物不入库，只留 .gitkeep）
web:
	cd web && npm ci && npm run build

# 先构建网页再编译二进制 ./railse；不带网页构建的二进制也能跑，只是网页路径回 503“网页未构建”
build: web
	go build -o railse ./cmd/railse

# gofmt（见下）、lint（见下）、vet、Go 测试（-race）、网页检查（tsc 与 vitest）
test: gofmt lint
	go vet ./...
	go test ./... -race
	cd web && npm ci && npm run check

# gofmt 查入库的与新建还没 git add 的 Go 文件（git ls-files -co --exclude-standard），不查 .gitignore 里的：
# 主 checkout 下的 .claude/worktrees、web/node_modules 里的不算。
# 有要格式化的文件就列出并失败；gofmt 读不了、解析不了（语法错误等，它的报错在上面）同样失败。
gofmt:
	@files="$$(git ls-files -co --exclude-standard '*.go')" && [ -n "$$files" ] || { echo "gofmt: git ls-files 没有列出 Go 文件（不在 git 仓库里？）"; exit 1; }; \
	out="$$(gofmt -l $$files)" || { echo "gofmt 出错（见上面的报错：语法错误，或文件读不了，例如删了但还没 git rm）"; exit 1; }; \
	if [ -n "$$out" ]; then echo "gofmt -l 有输出，请先格式化:"; echo "$$out"; exit 1; fi

# golangci-lint（配置见 .golangci.yml：standard 一组加 gofmt；测试文件不查 errcheck）
lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

# 校验 .goreleaser.yaml
release-check:
	go run github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION) check

# 网页端到端：真实的 hub、守护进程与钩子（临时目录、随机端口），Playwright 跑 web/e2e
e2e:
	scripts/e2e-web.sh
