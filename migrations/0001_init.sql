-- +goose Up
-- 客户端（调用者）。管理员自身也是一行，is_admin = true。
CREATE TABLE clients (
    id         TEXT PRIMARY KEY,
    key_sha256 TEXT NOT NULL UNIQUE,
    key_prefix TEXT NOT NULL,
    label      TEXT NOT NULL DEFAULT '',
    is_admin   BOOLEAN NOT NULL DEFAULT false,
    status     TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ
);

-- 客户端可用的模型（精确 ID）
CREATE TABLE client_models (
    client_id TEXT NOT NULL REFERENCES clients (id) ON DELETE CASCADE,
    model_id  TEXT NOT NULL,
    PRIMARY KEY (client_id, model_id)
);

-- 用量明细：只存元数据，不含正文、认证头或上游错误原文
CREATE TABLE usage_events (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    client_id         TEXT NOT NULL,
    model             TEXT NOT NULL,
    protocol          TEXT NOT NULL CHECK (protocol IN ('chat', 'responses', 'messages', 'count_tokens')),
    status            INTEGER NOT NULL,
    committed         BOOLEAN NOT NULL,
    stream            BOOLEAN NOT NULL,
    -- token 与成本在上游未提供/价格未知时为 NULL。
    -- 不记 0：0 会让汇总把「未知」算成「免费」，产出错误账单。
    input_tokens      BIGINT,
    output_tokens     BIGINT,
    cache_read_tokens BIGINT,
    cost_micros       BIGINT,
    latency_ms        INTEGER NOT NULL,
    first_byte_ms     INTEGER,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX usage_by_client_time ON usage_events (client_id, created_at DESC);
CREATE INDEX usage_by_time ON usage_events (created_at);

-- 按日聚合：明细清理后仍保留长期趋势
CREATE TABLE usage_daily (
    day           DATE NOT NULL,
    client_id     TEXT NOT NULL,
    model         TEXT NOT NULL,
    requests      BIGINT NOT NULL,
    errors        BIGINT NOT NULL,
    input_tokens  BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    cost_micros   BIGINT,
    PRIMARY KEY (day, client_id, model)
);

-- 审计：Key 创建与吊销、策略发布
CREATE TABLE audit_log (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor      TEXT NOT NULL,
    action     TEXT NOT NULL,
    subject    TEXT NOT NULL,
    detail     TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX audit_by_time ON audit_log (created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS usage_daily;
DROP TABLE IF EXISTS usage_events;
DROP TABLE IF EXISTS client_models;
DROP TABLE IF EXISTS clients;
