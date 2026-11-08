// Package store owns PostgreSQL persistence for the control plane.
//
// 边界：
//   - 只存元数据，不含请求正文、认证头或上游错误原文。
//   - 用量写入必须不阻塞数据面：队列有界，溢出丢弃并计数。
//   - token 与成本未知时存 NULL，不存 0。
package store

import (
	"context"
	"time"
)

// Protocol 是三种原生协议加上计数端点。
type Protocol string

const (
	ProtocolChat        Protocol = "chat"
	ProtocolResponses   Protocol = "responses"
	ProtocolMessages    Protocol = "messages"
	ProtocolCountTokens Protocol = "count_tokens"
)

// Valid 报告协议值是否受支持，与数据库 CHECK 约束保持一致。
func (p Protocol) Valid() bool {
	switch p {
	case ProtocolChat, ProtocolResponses, ProtocolMessages, ProtocolCountTokens:
		return true
	}
	return false
}

// UsageEvent 是一次生成调用的用量元数据。
//
// 指针字段为 nil 表示"未知"（上游未提供），与值为 0 有严格区别：
// 把未知记成 0 会让月度汇总把未知成本算成免费。
type UsageEvent struct {
	ClientID     string
	Model        string
	Protocol     Protocol
	Status       int
	Committed    bool
	Stream       bool
	InputTokens  *int64
	OutputTokens *int64
	CacheTokens  *int64
	CostMicros   *int64
	LatencyMS    int
	FirstByteMS  *int
	CreatedAt    time.Time
}

// Inserter 抽象一次批量写入，便于测试替换与失败注入。
type Inserter interface {
	InsertUsage(ctx context.Context, events []UsageEvent) error
}
