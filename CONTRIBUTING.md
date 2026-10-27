# 参与贡献

English version: [Contributing](#contributing).

## 跑测试

需要 Go（版本见 `go.mod`）和 Node 22。golangci-lint 经 `go run` 取用，不用另装。

```sh
make test    # gofmt、golangci-lint、go vet、go test -race、网页的 tsc 与 vitest
make e2e     # Playwright 驱动真实的 hub、守护进程与钩子，第一次会下载 Chromium
```

`make e2e` 的进程都在临时目录、随机端口上，不碰你的 `~/.railse` 和各 CLI 的配置。

## 改规则引擎（internal/policy）

在 `internal/policy/testdata/shell_corpus.tsv` 加语料：一行一条命令，一个制表符，再写期望的风险档（从低到高 `read`、`write_in_project`、`unknown`、`write_outside`、`network`、`outward`、`destructive`）。`#` 开头的是注释。测试里项目根是 `/home/u/proj`，家目录是 `/home/u`，命令在项目根里执行。

## 改脱敏（internal/redact）

1. 新输入加进 `internal/redact/shell_test.go` 的 `reviewCases`，写成 `{输入, 期望输出}`。这些输入也是模糊测试的种子。
2. 跑模糊测试：`go test ./internal/redact -run '^$' -fuzz FuzzString -fuzztime 1m`。
3. bash 差分核对：bash 会执行的命令不能被藏进 `<redacted>`。核对脚本还不在仓库里，合并前由维护者跑。

## 提交与 PR

- **提交信息用中文为主**：不会写中文就写英文。开头写类型，可以带范围，如 `fix(policy):`、`feat(hub):`、`docs:`。
- **PR 要过 CI**：`.github/workflows/ci.yml` 的五个作业。`go` 和 `web` 对应 `make test`，`e2e` 对应 `make e2e`，`install-script` 跑 `scripts/test-install.sh`（改了 `install.sh` 先在本地跑一遍），`cross` 交叉编译 macOS arm64、Linux amd64 和 arm64。
- **安全问题不要开 issue**：见 [SECURITY.md](SECURITY.md)。

# Contributing

## Running the tests

You need Go (version in `go.mod`) and Node 22. golangci-lint runs through `go run`, so there's nothing else to install.

```sh
make test    # gofmt, golangci-lint, go vet, go test -race, web tsc and vitest
make e2e     # Playwright drives a real hub, daemon and hooks; downloads Chromium the first time
```

`make e2e` runs everything in temp directories on random ports and doesn't touch your `~/.railse` or any CLI's config.

## Changing the policy engine (internal/policy)

Add cases to `internal/policy/testdata/shell_corpus.tsv`: one command per line, a tab, then the expected risk tier (lowest to highest: `read`, `write_in_project`, `unknown`, `write_outside`, `network`, `outward`, `destructive`). Lines starting with `#` are comments. In the test the project root is `/home/u/proj`, the home directory is `/home/u`, and commands run in the project root.

## Changing redaction (internal/redact)

1. Add new inputs to `reviewCases` in `internal/redact/shell_test.go` as `{input, expected output}`. These inputs also seed the fuzzer.
2. Run the fuzzer: `go test ./internal/redact -run '^$' -fuzz FuzzString -fuzztime 1m`.
3. Bash differential check: anything bash would execute must not end up hidden inside `<redacted>`. The script for this isn't in the repo yet; the maintainer runs it before merging.

## Commits and pull requests

- **Commit messages are mostly in Chinese.** If you don't write Chinese, English is fine. Start with a type and an optional scope, like `fix(policy):`, `feat(hub):` or `docs:`.
- **Pull requests need to pass CI**, the five jobs in `.github/workflows/ci.yml`. `go` and `web` match `make test`, `e2e` matches `make e2e`, `install-script` runs `scripts/test-install.sh` (run it locally if you change `install.sh`), and `cross` builds for macOS arm64 and Linux amd64 and arm64.
- **Don't open an issue for security problems.** See [SECURITY.md](SECURITY.md).
