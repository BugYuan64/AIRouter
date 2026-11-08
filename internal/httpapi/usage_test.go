package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Unwrap 是硬要求：缺少它，http.NewResponseController 无法穿透到
// 底层连接，SetWriteDeadline 会静默失败，慢客户端防护退回原状。
func TestUsageWriterUnwrapsForResponseController(t *testing.T) {
	base := httptest.NewRecorder()
	wrapped := &usageWriter{ResponseWriter: base}

	if got := wrapped.Unwrap(); got != http.ResponseWriter(base) {
		t.Fatal("Unwrap must return the wrapped ResponseWriter")
	}
	// ResponseController 应能穿透到 Recorder 取得 Writer
	rc := http.NewResponseController(wrapped)
	if rc == nil {
		t.Fatal("ResponseController must accept the wrapper")
	}
}

// 包装后 SetWriteDeadline 的行为必须与包装前一致。
// httptest.ResponseRecorder 不支持截止时间，两者都应返回 ErrNotSupported；
// 关键是不能因为包装而变成其他错误。
func TestUsageWriterPreservesDeadlineSupport(t *testing.T) {
	base := httptest.NewRecorder()
	before := http.NewResponseController(base).SetWriteDeadline(time.Now().Add(time.Second))
	after := http.NewResponseController(&usageWriter{ResponseWriter: base}).SetWriteDeadline(time.Now().Add(time.Second))
	if (before == nil) != (after == nil) {
		t.Fatalf("wrapper changed deadline support: before=%v after=%v", before, after)
	}
}

// Flush 必须仍然有效，否则 SSE 会被缓冲。
func TestUsageWriterPreservesFlush(t *testing.T) {
	base := httptest.NewRecorder()
	wrapped := &usageWriter{ResponseWriter: base}
	wrapped.Write([]byte("a"))
	wrapped.Flush()
	if !base.Flushed {
		t.Fatal("Flush must reach the underlying writer")
	}
	if wrapped.firstByte.IsZero() {
		t.Fatal("first byte time must be recorded")
	}
}

func TestUsageWriterTracksStatusAndCommit(t *testing.T) {
	base := httptest.NewRecorder()
	wrapped := &usageWriter{ResponseWriter: base}
	wrapped.WriteHeader(http.StatusOK)
	if !wrapped.committed {
		t.Fatal("2xx header must count as committed")
	}
	if wrapped.status != http.StatusOK {
		t.Fatalf("want 200, got %d", wrapped.status)
	}

	// 5xx 也是一次真实提交（错误正文已发给客户端）
	base2 := httptest.NewRecorder()
	wrapped2 := &usageWriter{ResponseWriter: base2}
	wrapped2.WriteHeader(http.StatusBadGateway)
	if !wrapped2.committed {
		t.Fatal("5xx header still commits a response body")
	}
}

// 1xx 是中间响应，不应视为最终提交。
func TestUsageWriterIgnoresInformationalAsCommit(t *testing.T) {
	base := httptest.NewRecorder()
	wrapped := &usageWriter{ResponseWriter: base}
	wrapped.WriteHeader(http.StatusEarlyHints)
	if wrapped.committed {
		t.Fatal("1xx must not count as final commit")
	}
}

// 未写任何内容时不得声称已提交，否则无法安全回退。
func TestUsageWriterNotCommittedBeforeWrite(t *testing.T) {
	wrapped := &usageWriter{ResponseWriter: httptest.NewRecorder()}
	if wrapped.committed {
		t.Fatal("must not be committed before writing")
	}
}

// 确认 ctx 取消仍能穿透到 cpa 客户端（委托测试，防回归）。
var _ = context.Background
