# 更新日志

本插件用户可感知的变更按版本记录于此。

## [0.5.1] - 2026-11-25

### 修复

- 配置写回路径：新增 `config-path` 设置与 `CPA_MISTRAL_CONFIG_PATH` 环境变量，保存控制台设置时按「显式配置 → 环境变量 → `/CLIProxyAPI/config.yaml` → CPA 可执行文件同目录 → 进程工作目录」查找 `config.yaml`，不再固定写 `/CLIProxyAPI/config.yaml`；显式指定的路径不存在时直接报错，不会新建文件。带反斜杠的路径在引号转义往返后也不再丢失。
- 插件块缩进：写回 `plugins.configs.mistral` 时沿用原键的缩进层级（子键 +2、列表项 +4、列表字段 +6），并按 `plugins → configs → mistral` 的层级定位；非 4 空格缩进的配置文件保存后不再错位。
