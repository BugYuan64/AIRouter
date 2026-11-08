package httpapi

import (
	"net/http"
	"time"
)

// usageWriter 观察响应的状态码、首字节时间与是否已提交，
// 用于生成用量记录。它不缓冲任何字节，因此不影响流式转发。
//
// 必须实现 Unwrap()：http.NewResponseController 依赖它穿透到底层连接，
// 缺少时 SetWriteDeadline 会静默失效，慢客户端防护会退回原状。
type usageWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	firstByte   time.Time
	committed   bool
}

func (u *usageWriter) Unwrap() http.ResponseWriter { return u.ResponseWriter }

func (u *usageWriter) WriteHeader(status int) {
	if !u.wroteHeader {
		u.status = status
		u.wroteHeader = true
		// 1xx 是中间响应，不代表最终提交
		if status >= 200 {
			u.committed = true
		}
	}
	u.ResponseWriter.WriteHeader(status)
}

func (u *usageWriter) Write(p []byte) (int, error) {
	if !u.wroteHeader {
		u.status = http.StatusOK
		u.wroteHeader = true
	}
	if len(p) > 0 && u.firstByte.IsZero() {
		u.firstByte = time.Now()
	}
	u.committed = true
	return u.ResponseWriter.Write(p)
}

// Flush 保留即时刷新语义，同时记录首字节时间。
func (u *usageWriter) Flush() {
	if f, ok := u.ResponseWriter.(http.Flusher); ok {
		if u.firstByte.IsZero() {
			u.firstByte = time.Now()
		}
		f.Flush()
	}
}
