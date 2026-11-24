# AGENTS.md — Mistral 插件仓

本文件约束在本仓库工作的 agent。仓库目录名为 `cpa-mistral`，插件 id 为 `mistral`，两者的差异见「本仓偏离」。宿主为 CLIProxyAPI（CPA），插件以 Go c-shared 动态库 + JSON ABI v1 加载（`cmd/mistral/main.go`）。

## 产物布局

```text
dist/<goos>/<goarch>/mistral-v<version>.<so|dll>   # 动态库本体
dist/mistral-v<version>-<goos>-<goarch>.zip        # 本地插件源安装包，zip 根只有动态库
```

- 生成入口：`scripts/build-windows.ps1`（windows/amd64）、`scripts/build-linux.ps1`（linux/amd64 或 `-Arch arm64`）、`Dockerfile.build`（linux/amd64）。
- macOS 产物只由 CI 构建（`.github/workflows/release.yml`），本地脚本不产出。
- `dist/` 已被 `.gitignore` 忽略，禁止入库；严禁把动态库写到仓库根，本地插件源按上面的路径发现产物。
- CI 发布资产另用下划线命名 `mistral_<version>_<goos>_<goarch>.zip`，由 workflow 从 `dist/` 改名打包；本地脚本不生成这种名字。

## 版本唯一事实源

`internal/plugin/register.go` 的 `pluginVersion` 常量是全仓唯一版本事实源：

- `cmd/mistral/version.go` 通过 `plugin.Version()` 读取该常量，不另存版本。
- `scripts/build-windows.ps1`、`scripts/build-linux.ps1` 从该常量取版本拼产物名。
- `marketplace/registry.json` 的 `plugins[0].version` 必须与它逐字相等；CI 的 metadata 任务会比对（`.github/workflows/release.yml`），不一致直接失败。
- 发版只改 `pluginVersion` 与 `marketplace/registry.json` 两处，再打 `v<version>` tag。

## 本仓偏离：目录名 ≠ 插件 id

集合约定是插件仓目录名等于插件 id，本仓不满足：

| 项 | 值 |
|---|---|
| 仓库目录名 | `cpa-mistral` |
| 插件 id / 宿主配置键 | `mistral`（`plugins.configs.mistral`） |
| 动态库 / 安装包名 | `mistral-v<version>.<so\|dll>`、`mistral-v<version>-<goos>-<goarch>.zip` |

因此本地插件源登记 **只能用 `make-registry.ps1 -m publish`**：

- 默认的 `update` 模式扫描集合根下含 `internal/plugin/register.go` 的子目录，并直接拿**目录名**当 id，会得到 `cpa-mistral`，与本插件 id `mistral` 不符；该模式不适用于本仓。
- `-m publish` 不从目录名推导 id、不改版本，完全按集合根 `registry.json` 中 `id` 为 `mistral` 的条目发布。发布前，该条目引用的安装包必须已存在于发布目录（默认集合根 `.registry/artifacts/mistral/`）；脚本按条目里的文件名校验，并回写 `sha256` / `size`。
- 首次登记需在集合根 `registry.json` 手工补 `id: mistral` 的条目，并填写 `name` / `description` / `author`（`publish` 只按条目发布，不校验这些展示字段）。
- 本仓 `store/meta.json` 提供本仓侧的 `name` / `description` / `author`，供按仓读取展示元数据的集合级工具使用。

## 构建与自检

```powershell
# 双平台本地构建（产物布局见上）
pwsh -NoProfile -ExecutionPolicy Bypass -File scripts\build-windows.ps1
pwsh -NoProfile -ExecutionPolicy Bypass -File scripts\build-linux.ps1            # 默认 amd64，经 docker golang:1.27-bookworm
pwsh -NoProfile -ExecutionPolicy Bypass -File scripts\build-linux.ps1 -Arch arm64

# 提交前自检（c-shared 构建，需 C 编译器 / CGO_ENABLED=1）
gofmt -l .
go vet ./...
go build ./...
go test ./cmd/mistral
```

`go test ./cmd/mistral` 与 CI 实际执行的命令一致；当前测试只覆盖主包。

发版完成定义：改动 `pluginVersion` 或影响产物的代码后，收尾前必须产出 linux/amd64 与 windows/amd64 两个安装包，放入发布目录后再执行 `make-registry.ps1 -m publish`；缺安装包的插件不会上架，本地源会停留在旧版本。

## 发版约定

1. 改 `internal/plugin/register.go` 的 `pluginVersion`，同步 `marketplace/registry.json` 的 `plugins[0].version`。
2. 推送 `v<version>` tag；workflow 先核对 registry 版本、常量与 tag 名，不一致直接失败。
3. 构建并打包 linux amd64、linux arm64、darwin amd64、darwin arm64、windows amd64 五个 zip，生成 `checksums.txt`，创建或更新 GitHub Release 并标 latest。
4. Release 步骤只在归属满足 workflow 条件的仓库上执行；fork 或本地运行时只跑构建与测试任务。

## 脱敏红线

1. 不写个人本机路径与 Windows 用户名（示例、日志、构建输出都不行）；文档里的路径一律用容器路径或仓库相对路径。
2. 不写真实凭据 / token / API key / 账号数据；示例 key 用 `mstrl_...` 这类占位。
3. 除代码默认的 Mistral 官方服务地址外，不写其他真实上游端点；示意一律用保留域与商用泛称。
4. 不写真实错误负载原文；文档案例一律合成并标注。
5. 不写本工作区其他仓库名、内部会话标识与本机目录结构。
6. `/CLIProxyAPI/plugin-data/mistral/` 下的 `quota.json`、`requests.jsonl` 只存在于运行环境，不得把其中的真实内容带进仓库。

## 文档路由

| 要做的事 / 要回答的问题 | 先读 |
|---|---|
| 安装、配置字段、`config-path` 查找顺序 | `README.md` |
| 中文能力概览 | `README.zh.md` |
| 版本变更历史 | `CHANGELOG.md` |
| 插件 id、版本常量、宿主注册元数据 | `internal/plugin/register.go`、`cmd/mistral/main.go` 的 `registrationJSON` |
| 配置解析与写回、缩进定位、配置路径解析 | `cmd/mistral/settings.go` |
| 控制台页面与管理接口 | `cmd/mistral/page.go`、`cmd/mistral/admin.go` |
| 请求转换、失败切换、流式泵、日志与额度 | `cmd/mistral/convert.go`、`cmd/mistral/direct.go`、`cmd/mistral/quota.go`、`cmd/mistral/logs.go` |
| 构建与发版 | `scripts/`、`Dockerfile.build`、`.github/workflows/release.yml` |
| 商店展示元数据 | `marketplace/registry.json`、`store/meta.json` |
