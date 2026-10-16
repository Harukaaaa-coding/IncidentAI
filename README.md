# IncidentAI V2

一个可运行的本地 Incident Response 平台：基于工具证据调查事故，比较根因假设，提出受风险策略约束的恢复建议，经人工批准执行模拟恢复，再验证指标、生成复盘与候选经验。

V2 已接入真实模型调用、`rg` / `git` 工具和 stdio MCP 调查接口。模拟事故与恢复之外，现支持独立的本地项目只读检查。生产恢复适配器、身份认证和权限系统尚未实现，不应作为生产服务直接部署。

## 启动

需要 Go 1.26 或更新版本，以及 PATH 中的 `git`、`rg`（ripgrep）。部署回归的合成回放还需要 Python，可用 `INCIDENTAI_PYTHON` 指定解释器；详细发现规则见 [工具说明](docs/tools.md)。在项目根目录运行：

```powershell
go run .
```

打开 [Incident Control Center](http://127.0.0.1:8099/)。默认只监听 `127.0.0.1:8099`；当前工作环境的 8080 由 DeepSearch 使用，不需要停止或修改它。可通过 `INCIDENTAI_ADDR` 指定其他本地监听地址。

在首页创建事故，选择单 Agent 或多 Agent 编排，然后点击「开始调查」。调查结束后可点击假设引用查看原始证据，再审阅并批准恢复。只有指标验证通过才标记已恢复；复盘中的候选经验需要人工核验，确认后才进入历史检索。

## 接入本地项目（无需 MCP）

在顶栏点击「本地项目」→「添加本地项目」，填写仓库绝对路径、仓库内日志路径及可选的本机健康检查地址。也可点击「填入 DeepSearch」，检查建议路径后保存。

「验证连接」→「开始只读调查」会生成可展开的证据、待验证假设、建议和持久化调查历史。默认执行固定的本地安全检查，不调用模型、不运行目标项目代码或测试、不修改源码或服务，也不把健康端点成功当作业务无故障。

可选模型复核必须先「预览外发内容」，检查完整证据包与接收地址，再单独授权。授权只用于这一份证据；模型不能操作目标项目。具体范围、安全限制及界面步骤见 [本地项目接入](docs/local-projects.md)。

## 模型配置

按当前用户选择，模型沿用同级 DeepSearch 项目的 `report_model` 配置。读取优先级为：

1. 环境变量 `INCIDENTAI_MODEL_CONFIG` 指定的 YAML 文件。
2. 本项目 `./config.yaml`。
3. 同级项目 `../DeepSearch/config.yaml`。

配置仅在启动时读取，不复制 DeepSearch 的密钥。当前兼容配置为 `provider: bai`、`model: gpt-5.2`，向 `https://api.b.ai/v1/chat/completions` 发送 `messages`、`temperature: 0.2` 与 `max_tokens`。模板见 [config.example.yaml](config.example.yaml)：

```yaml
report_model:
  provider: bai
  model: gpt-5.2
  api_key: ${BAI_API_KEY}
```

`api_key` 支持环境变量展开。本地 `config.yaml` 已加入 `.gitignore`。也可以明确指定兼容配置并进行最小真实连通性探测：

```powershell
$env:INCIDENTAI_MODEL_CONFIG = "..\DeepSearch\config.yaml"
go run . -check-model
```

探测会调用一次已配置模型，输出模型名称、响应与 token 用量，不打印密钥。模型调查会将选入上下文的日志、指标、示例代码与知识内容发送给配置的模型服务，并可能产生费用；连接真实数据前须确认数据外发权限。模型调查模式按单 / 多 Agent 编排调用专家和诊断模型，并由服务端验证证据引用与动作策略；模型不可直接执行恢复写操作。

无需模型时运行离线规则基线：

```powershell
$env:INCIDENTAI_OFFLINE = "1"
go run .
```

恢复模型模式时将 `INCIDENTAI_OFFLINE` 清空并重启。没有任何配置文件时也会进入离线模式；存在但无效的配置会报错，不静默伪装成模型成功。界面明确显示「LLM」或「离线规则基线」。

## 当前能力

- 五类模拟事故：部署回归、数据库过载、Redis / 缓存故障、支付供应商故障、内存 / CPU 回归。部署场景可定位 `retry_limit: 5 → 2`，其他场景不依赖部署回归这个答案。
- Commander 组织观测、代码、知识、诊断、恢复和 Reviewer；支持单 / 多 Agent 编排。Skills 定义可执行的只读调查计划，证据、工具状态、假设与反向证据均可检查。
- Memory 分为当前工作状态、历史事故经验和系统知识。候选经验保留 confirmed / probable / rejected / outdated 审阅与审计记录；只有 confirmed 历史经验可检索。
- RAG 使用 BM25 与确定性哈希概念向量，并按元数据、时效性和服务相关性排序；不是预训练模型的语义向量嵌入。Context Builder 聚合日志并按字符预算筛选来源，token 数是估计值。
- 风险策略允许只读调用；中高风险恢复需要人工批准和匹配的事故 revision；数据库写入等未支持动作被阻止。所有支持度均为未校准的证据评估，不是统计概率。
- 前端提供事故列表、指标、计划、假设、时间线、证据、历史经验、审批和完整复盘，以及经验库、评测和平台能力页。

模拟恢复只改变对应事故的模拟状态，不修改真实部署。状态默认持久化到 `./data`，可用 `INCIDENTAI_DATA_DIR` 调整；每个数据目录只运行一个服务进程。已验证恢复的本地模拟状态可在重启后重建；中断的调查或恢复动作不会自动重试，而是要求重新调查核验。

## 外部工具与 MCP

默认使用本地模拟观测数据，并真实执行 ripgrep 和 Git diff。设置 `INCIDENTAI_MCP_CONFIG` 可改用 stdio MCP 只读工具：

```powershell
$env:INCIDENTAI_MCP_CONFIG = ".\config\mcp.example.json"
go run .
```

示例启动项目自带 MCP 模拟服务器；第三方服务器需要适配约定的输入输出结构，不能仅修改工具名称。外部 MCP 模式禁止恢复写操作。协议、工具映射与边界见 [工具与 MCP](docs/tools.md)。

## API 示例

```powershell
$base = "http://127.0.0.1:8099/api/v2"
Invoke-RestMethod "$base/status"
$incident = Invoke-RestMethod "$base/incidents" -Method Post -ContentType "application/json" -Body '{"scenario_id":"deployment-regression","mode":"multi"}'
$result = Invoke-RestMethod "$base/incidents/$($incident.id)/investigate" -Method Post -ContentType "application/json" -Body '{}'
$result.hypotheses
```

主要接口如下，所有 POST 请求均使用 JSON；调查与评测请求会等待执行结束，期间可以 GET 事故读取进度。

| 接口 | 用途 |
| --- | --- |
| `GET /api/v2/status` | 引擎、模型、工具、Skills、策略和场景 |
| `GET /api/v2/projects`、`POST /api/v2/projects` | 查看 / 登记本地项目，字段 `name`、`repository_path`、可选 `log_path`、`health_url` |
| `POST /api/v2/projects/{id}/validate` | 验证本地只读连接，内容为 `{}` |
| `GET /api/v2/projects/{id}/investigations`、`POST /api/v2/projects/{id}/investigations` | 历史 / 新建本地检查，POST 内容为 `question` |
| `POST /api/v2/projects/{id}/investigations/{run}/model-preview` | 生成本地预览，不调用模型 |
| `POST /api/v2/projects/{id}/investigations/{run}/model-review` | 明确授权模型复核：`approved: true` 与未过期、单次使用的 `digest` |
| `GET /api/v2/incidents`、`GET /api/v2/incidents/{id}` | 事故列表、当前证据与状态 |
| `POST /api/v2/incidents` | 创建事故：`scenario_id`、可选 `title`、`mode` |
| `POST /api/v2/incidents/{id}/investigate` | 调查：`{}` |
| `POST /api/v2/tools/{name}` | 手动只读工具：`incident_id`、`arguments` |
| `POST /api/v2/incidents/{id}/actions/{action}/approve` | 人工批准：`approved: true`、最新 `revision` |
| `POST /api/v2/incidents/{id}/memory/review` | 经验审阅：`status`、`reviewer` |
| `GET /api/v2/memory` | 查看历史经验及质量状态 |
| `GET /api/v2/evaluation`、`POST /api/v2/evaluation` | 读取 / 运行离线评测，POST 内容为 `{}` |

## 评测与验证

```powershell
go test ./...
go vet ./...
go run . -eval
```

`-eval` 在独立临时存储中执行 60 个案例 × 完整基线与四组消融（无 RAG、无 Memory、无 Context Builder、单 Agent），共 300 次离线调查，并将 JSON 报告输出到标准输出。它不读取模型凭证、不调用模型、不改变工作台事故；输入 / 输出模型 tokens 为零，模型费用为 `null`。

案例来自五类合成模板，包含噪声、缺失与冲突证据、过时 Runbook、错误 Memory 和恢复未改善等变化。评测用于系统回归验证，不证明真实事故准确率、LLM 多 Agent 收益或 API 成本节省。指标口径和限制见 [评测说明](docs/evaluation.md)。Windows 的 `go test -race` 还需要可用的 C 编译器，普通测试通过不代表完成 race 检查。

## 代码与文档

- `internal/platform/`：V2 编排、模型、风险审批、恢复、评测与 HTTP 服务。
- `internal/projects/`：真实本地项目登记、受限读取、脱敏、只读调查及历史。
- `internal/tooling/`、`cmd/mcp-simulator/`：本地工具、stdio MCP 与模拟服务器。
- `internal/memory/`、`knowledge/v2/`、`skills/`：持久化、检索、上下文与调查方法。
- `simulation/v2/`、`evaluation/cases.json`：多事故模拟数据与基准案例。
- [Memory 与上下文](docs/memory.md)、[界面使用说明](docs/ui.md)、[配置模板](config.example.yaml)。

V1 实现保留在 `internal/incident/`，用于参考与原有测试；V2 启动入口不再提供 V1 页面或旧 `/api/*` 接口。

## 克隆与本地数据

仓库仅包含源码、文档和合成事故样例，不包含本地模型密钥、真实项目日志或调查记录。首次克隆后可设置 `INCIDENTAI_OFFLINE=1`，无需 API 密钥即可运行模拟调查与本地项目只读检查。

V1 / V2 的版本差异使用仓库内可追踪的模拟快照比较，不需要额外克隆子模块或恢复嵌套 Git 历史。请始终将本机 `config.yaml`、`.env` 和 `data/` 保持在版本控制之外。
