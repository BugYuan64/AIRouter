# 多阶段构建：前端 -> Go 编译 -> 最小运行镜像
#
# 构建产物不依赖容器内 Node 或 Go 工具链，运行时镜像只含二进制与 CA 证书。

# --- 阶段 1：构建前端 ---
FROM node:22-alpine AS web
WORKDIR /src/web
# 先只复制依赖清单，命中缓存时跳过重复安装
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
# 产物直接写入 internal/admin/dist，供 go:embed 使用
RUN npm run build

# --- 阶段 2：编译 Go ---
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 用阶段 1 的真实前端产物覆盖占位目录
COPY --from=web /src/internal/admin/dist ./internal/admin/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION:-dev}" -o /out/airouter ./cmd/airouter \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/migrate ./cmd/migrate

# --- 阶段 3：运行时 ---
FROM alpine:3.21
# ca-certificates：上游若用 HTTPS 必需；tzdata：日聚合按 Asia/Shanghai 计算
RUN apk add --no-cache ca-certificates tzdata wget \
 && addgroup -g 1000 airouter \
 && adduser -u 1000 -G airouter -D -H airouter

COPY --from=build /out/airouter /usr/local/bin/airouter
COPY --from=build /out/migrate /usr/local/bin/migrate
COPY --from=build /src/migrations /usr/local/share/airouter/migrations

# 非 root 运行：容器被攻破时降低影响面
USER 1000:1000
ENV TZ=Asia/Shanghai \
    AIROUTER_LISTEN_ADDR=0.0.0.0:8080

EXPOSE 8080

# 数据面健康检查，不触碰 CPA 目录（readyz 会调用上游）
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/airouter"]
