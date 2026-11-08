# AIRouter

以 CLIProxyAPI（CPA）为执行内核的 API 网关。当前实现**指定模型一对一 HTTP 转发**：保留 model、原始 JSON 正文和原生协议，不启用智能选模、网关重试或跨模型回退。

## 已实现范围

| 端点 | 行为 |
| --- | --- |
| `GET /healthz` | 进程存活检查，不调用 CPA |
| `GET /readyz` | CPA 目录就绪检查，最多一个并发探测，2 秒截止，不暴露目录或凭据 |
| `GET /v1/models` | 认证后读取 CPA 目录，只返回调用者允许的精确模型 ID |
| `POST /v1/chat/completions` | Chat Completions 原生 JSON/SSE 透传 |
| `POST /v1/responses` | 完整输入随请求提交的 Responses JSON/SSE 透传 |
| `POST /v1/messages` | Anthropic Messages 原生 JSON/SSE 透传 |
| `POST /v1/messages/count_tokens` | 原生计数请求转发，当前仅模拟链路验收 |

客户端认证、JSON、精确模型权限和受限特性校验后，网关向固定 CPA 发送一次请求。成功正文保持原样，流式即时刷新，取消传播到 CPA。工具定义、结果、reasoning 和扩展字段在允许的请求中保持原始字节，网关不执行工具。

网关与 CPA 的 Key 分离。Messages 接受 Bearer 或 `x-api-key`，其他端点使用 Bearer；重复或同时提交两种认证会被拒绝。客户端 Cookie、额外凭据和伪造代理来源头不转给 CPA；协议版本、beta、会话等其他头保留。请求 Trailer 拒绝；响应 Trailer 不转发，1xx 和最终响应均清洗凭据头。

2026-11-05 已通过 `gpt-6.1-sol` 的三协议普通/流式真实调用，共 6 项，均 HTTP 200、有输出；流式均收到终止事件。同日晚间追加进程级验收：独立网关进程经认证与目录过滤后，对 `gpt-6.1-sol`、`deepseek-v4.1-flash`、`qwen3.8-max`、`glm-5.3` 各 13 项全部通过。这仍不是 20 个目录模型全部可用、工具执行或完整 Codex/Claude Code 工作流的验收。

## 本地运行

需要 Go 1.27 或以上。运行依赖全部为标准库，不需要数据库或 Docker。

1. 复制 [clients.example.json](clients.example.json) 为 `clients.json`，填写客户端 ID、独立网关 Key 的 SHA256 摘要和精确模型允许列表。示例占位值不能直接启动。
2. 在进程环境中设置固定 CPA 地址、CPA 数据面 Key 和权限文件路径。
3. 运行 `go run ./cmd/airouter`，默认监听 `127.0.0.1:8080`。

使用下列命令交互输入网关 Key 并生成摘要；输入不会回显。自行安全保存原始网关 Key，客户端提交原始值，权限文件只存摘要：

```powershell
python -c "import getpass,hashlib; print(hashlib.sha256(getpass.getpass('Gateway key: ').encode()).hexdigest())"
```

PowerShell 启动示例：

```powershell
$env:CPA_BASE_URL = 'https://cpa.example.com'
$env:CPA_API_KEY = [Net.NetworkCredential]::new('', (Read-Host 'CPA client key' -AsSecureString)).Password
$env:AIROUTER_CLIENTS_FILE = (Resolve-Path './clients.json').Path
go run ./cmd/airouter
```

替换示例域名。`CPA_BASE_URL` 不含 `/v1`、路径、查询或用户信息。远程连接要求 HTTPS；HTTP 仅允许回环。只有 CPA 同机或已有 SSH 本地转发时，才能使用 `http://127.0.0.1:8317`；容器自身回环不是宿主机。

程序不自动读取 `.env`；[.env.example](.env.example) 仅说明变量。不要把真实 Key 写进命令参数、源码或提交文件。

| 变量 | 默认/要求 |
| --- | --- |
| `CPA_BASE_URL` | 必填，固定 CPA origin |
| `CPA_API_KEY` | 必填，CPA 客户端 Key，禁止与网关 Key 相同 |
| `AIROUTER_CLIENTS_FILE` | 必填，客户端摘要和精确模型权限 JSON |
| `AIROUTER_LISTEN_ADDR` | `127.0.0.1:8080` |
| `AIROUTER_UPSTREAM_TIMEOUT` | `10s`，只用于目录请求 |
| `AIROUTER_REQUEST_TIMEOUT` | `10m`，整个生成转发的截止时间，含流式和下游写入 |
| `AIROUTER_MAX_REQUEST_BYTES` | `16777216`，16 MiB |
| `AIROUTER_ADMIN_KEY` | 空（管理 API 返回 503）。管理面密钥，与客户端网关 Key、CPA Key 三者互不复用 |
| `AIROUTER_DATABASE_URL` | 空（不记录用量、Key 管理不可用）。PostgreSQL DSN；配置后启用动态认证与用量记录 |

OpenAI 兼容客户端的 Base URL 可设为 `http://127.0.0.1:8080/v1`；Anthropic 客户端按自身端点拼接方式使用网关 origin。Key 填网关原始 Key，model 填允许列表中的精确 CPA ID。网关不改写别名，不承诺控制 CPA 内部最终渠道或账号。

## 明确限制

- 尚未实现智能路由，`airouter/auto` 无特殊行为。开放自动路由需先满足三项前置条件：响应提交点可判定、策略原子快照、决策日志不阻塞，未实现前不得上线。
- Responses 不开放 `previous_response_id`、`conversation`、`background=true`、`store=true`、item reference 或 ID-only item；响应 ID 查询/删除/后台操作返回 501。不支持 WebSocket。
- 不开放上传文件 ID、vector store ID 和已有 container ID 等协议资源引用，因首版没有跨调用者资源归属管理。内联文件数据和普通 function 工具参数中的业务字段可透传。未知扩展的资源语义尚未验收。
- 不提供文件上传、图片生成、音视频等独立端点，模型名称不代表这些端点已支持。
- 请求必须是未压缩 `application/json`，拒绝重复 JSON 字段和超过 256 层的嵌套。不会裁剪上下文或静默删参数。
- 权限文件不支持热更新，修改后重启本地网关。
- 网关不重试；输出开始后断流终止连接，不拼接其他模型或 JSON 错误。取消不能保证上游绝无后续用量。
- 上游非成功错误使用固定格式脱敏；保留 400/404/422/429/5xx 等状态和 Retry-After。CPA 401/403 映射 502，重定向映射 502，不跟随或返回 Location。
- CPA 仍按自身既有策略执行和重试，本轮未修改其配置；一次网关转发不等于供应商只尝试一次。
- 目录正文读取阶段超时目前统一作为 502；响应头之前的超时为 504。这项错误分类一致性待后续完善。
- 当前未部署公网服务、TLS 入口、商业管理、配额或整机高可用，也未验收完整编程客户端工作流。

## 验证

普通测试只连接临时本机模拟 CPA，不消耗真实模型额度：

```powershell
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/airouter
```

Windows race 测试需要可用 C 工具链。覆盖认证、权限、目录、原始正文、工具参数、JSON/体积/状态限制、超时、取消、SSE 首段、错误不重放、Trailer/1xx 清洗和不读取响应的慢客户端。默认跳过真实模型测试。

显式真实验收器：[验证CPA直连.py](辅助脚本/验证CPA直连.py)。它使用严格主机校验的 SSH 隧道，私下读取现有数据面凭据并在进程内存传递，发起 6 次真实生成，每次输出预算 256 token，不写秘密或修改 CPA：

```powershell
python '辅助脚本/验证CPA直连.py' --ssh-target ubuntu@YOUR_HOST --identity 'C:/path/to/key.pem' --cpa-config /path/to/cliproxyapi/data/config.yaml --model EXACT_MODEL_ID --report live-check.json
```

需本机 Python、SSH、Go，以及远端可读配置的 Python/PyYAML；host key 须先核验。报告只含模型、协议、状态和耗时等非秘密结果。一次六项通过不能推断所有模型能力。

## 技术选型

2026-11-06 确定。

| 层 | 选型 |
| --- | --- |
| 数据面 `/v1/*` | 标准库 `net/http`，不引入框架 |
| 控制面 `/admin/api` | 标准库 `ServeMux`，按需加 `chi`（仅管理端口） |
| 持久化 | PostgreSQL 17 + `pgx/v5` + `goose`，不用 ORM |
| 部署形态 | 容器化；网关与数据库各自独立容器，均不与 CPA 混装 |
| 管理前端 | Vue 3 + Vite + TS + Element Plus + Tailwind v4（仅布局），`embed.FS` 打包 |
| 日志与指标 | `log/slog`、`prometheus/client_golang` |

数据面不引入框架不是风格问题：当前转发层依赖 `SetWriteDeadline`、`FlushInterval: -1`、原始 `Request` 字段清空和原始字节透传，`gin`/`echo` 的 Context 包装与 `fiber` 的 fasthttp 语义都会威胁已验收的三协议流式。

## 实际目录

```text
cmd/airouter/          进程入口与启动测试
internal/config/      配置与权限档案
internal/access/      认证与模型权限
internal/cpa/         CPA 目录与原生转发
internal/httpapi/     HTTP、策略检查和显式真实测试
辅助脚本/              可复用真实验证器
.env.example          变量说明
clients.example.json  无秘密权限模板
```

控制面规划（尚未创建）：`internal/admin/`（管理 HTTP，**已创建**）、`internal/store/`（PostgreSQL 访问层）、`internal/route/`（纯函数选模）、`internal/profile/`（能力档案）、`internal/eval/`（离线评测）、`web/`（Vue 工程，**已创建**）。按实际需要创建，不预建空目录。

## 前端开发

管理控制台是竖直滚动的单页，产物经 `embed.FS` 打进 Go 二进制。

```bash
cd web
npm install
npm run build        # 输出到 ../internal/admin/dist，随 go build 一并嵌入
npm run dev          # 开发模式，/admin/api 代理到 127.0.0.1:8081
```

`AIROUTER_ADMIN_ADDR` 未设置时不启动管理面。设置了但未构建前端时，页面会显示构建提示，不会返回空白。

管理端口对 `/v1/*` 明确返回 404 JSON——连错端口时得到可读错误，而不是伪装成成功的 HTML 页面。

### 控制台与角色

数据面与控制台合并在同一进程、同一端口。访问网关根路径直接进入登录页，`/v1/*` 走模型数据面。

| 角色 | 登录凭据 | 可见范围 |
| --- | --- | --- |
| 管理员 | `AIROUTER_ADMIN_KEY` | 全部账户与用量、Key 管理、路由决策、设置 |
| 客户端 | 自己的网关 Key | 仅自己的用量与账户、使用文档 |

角色由服务端 `GET /admin/api/whoami` 返回，前端仅用于渲染；全部权限校验在服务端完成。客户端不能创建或吊销 Key（返回 403），也不能通过查询参数扩大可见范围。

### 管理面现状

| 接口 | 状态 |
| --- | --- |
| `GET /admin/api/status` | 已实现，接 CPA 目录探测，带 10 秒缓存 |
| `GET /admin/api/models` | 已实现，只返回目录 ID 与归属，能力三态一律 `unknown` |
| `GET /admin/api/whoami` | 已实现，返回角色与能力 |
| `GET /admin/api/usage` | 已实现，按角色过滤后汇总近 30 天 |
| `GET /admin/api/usage/recent` | 已实现，最近明细 |
| `GET /admin/api/keys` | 已实现，列表不返回摘要 |
| `POST /admin/api/keys` | 已实现，明文 Key 仅返回一次 |
| `DELETE /admin/api/keys/{id}` | 已实现，吊销即时生效 |

管理密钥经 `AIROUTER_ADMIN_KEY` 注入，服务端常量时间比对；未配置时管理 API 返回 503，不放行。前端密钥只存 `sessionStorage`，关闭标签即清除。

数据库迁移与 `clients.json` 导入需显式执行，不在进程启动时自动运行：

```bash
AIROUTER_DATABASE_URL='postgres://...' go run ./cmd/migrate -migrations migrations
AIROUTER_DATABASE_URL='postgres://...' go run ./cmd/migrate -migrations migrations -clients clients.json
```

导入保留原 SHA256 摘要，现有客户端无需更换 Key。迁移与导入均可重复执行。

配置 `AIROUTER_DATABASE_URL` 后认证来源改为数据库（支持即时吊销），否则回退到 `clients.json` 静态认证。

## 容器部署

```bash
cp .env.container.example .env   # 填 POSTGRES_PASSWORD / AIROUTER_ADMIN_KEY / CPA_API_KEY
chmod 600 .env && mkdir -p pgdata && sudo chown -R 1000:1000 pgdata
docker compose up -d
docker compose exec airouter migrate -migrations /usr/local/share/airouter/migrations
```

镜像 51 MB、非 root 运行、多阶段构建。网关与数据库各为独立容器，均不与 CPA 混装；数据库不发布端口。

反向代理需要关闭响应缓冲才能让 SSE 即时通过，并把读写超时放宽到覆盖整段生成。Caddy 示例：

```caddy
<your-domain> {
    encode zstd gzip
    reverse_proxy 127.0.0.1:8080 {
        flush_interval -1
        transport http {
            read_timeout 15m
            write_timeout 15m
        }
    }
}
```

回滚即把镜像标签指回上一个版本并 `docker compose up -d`；数据库迁移均为向前兼容的新增，不随镜像回退撤销。备份使用 `pg_dump` 导出 `airouter` 库，`.env` 与 `pgdata` 需另行保存。

## 项目文档

设计、计划与验收记录保留在本地 `docs/` 目录，不随仓库发布。

