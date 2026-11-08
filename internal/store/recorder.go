package store

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Recorder 把生成请求的用量投递到有界队列，由后台 goroutine 批量落库。
//
// 关键约束：Record 永不阻塞调用方。队列满时丢弃并计数，
// 因为数据面吞吐优先于统计完整性。丢弃数是可观测的，不是静默丢失。
type Recorder struct {
	queue    chan UsageEvent
	inserter Inserter
	batch    int
	every    time.Duration

	dropped atomic.Int64
	flushed atomic.Int64
	failed  atomic.Int64

	closeOnce sync.Once
	stop      chan struct{}
	done      chan struct{}
}

// RecorderOptions 配置队列与批处理参数。
type RecorderOptions struct {
	// QueueSize 是内存队列容量，默认 4096。
	QueueSize int
	// BatchSize 是单次提交的最大条数，默认 200。
	BatchSize int
	// FlushEvery 是定时刷盘间隔，默认 500ms。
	FlushEvery time.Duration
	// InsertTimeout 是单次批量写入的截止时间，默认 10s。
	// 必须有上限，否则数据库卡住会让 writer 永久挂起。
	InsertTimeout time.Duration
}

func (o RecorderOptions) withDefaults() RecorderOptions {
	if o.QueueSize <= 0 {
		o.QueueSize = 4096
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 200
	}
	if o.FlushEvery <= 0 {
		o.FlushEvery = 500 * time.Millisecond
	}
	if o.InsertTimeout <= 0 {
		o.InsertTimeout = 10 * time.Second
	}
	return o
}

// Stats 是记录器的累计统计，用于指标暴露。
type Stats struct {
	Dropped int64
	Flushed int64
	Failed  int64
}

// NewRecorder 创建并启动后台写入协程。
// inserter 为 nil 时只计数不写入，用于未配置数据库的场景。
func NewRecorder(inserter Inserter, opts RecorderOptions) *Recorder {
	o := opts.withDefaults()
	r := &Recorder{
		queue:    make(chan UsageEvent, o.QueueSize),
		inserter: inserter,
		batch:    o.BatchSize,
		every:    o.FlushEvery,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go r.run(o.InsertTimeout)
	return r
}

// Record 投递一条用量记录。永不阻塞。
func (r *Recorder) Record(event UsageEvent) {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now()
	}
	select {
	case r.queue <- event:
	default:
		// 队列满：丢弃并计数。不阻塞、不反压生成请求。
		r.dropped.Add(1)
	}
}

// Stats 返回累计统计。
func (r *Recorder) Stats() Stats {
	return Stats{Dropped: r.dropped.Load(), Flushed: r.flushed.Load(), Failed: r.failed.Load()}
}

// Close 停止接收、刷出剩余记录并等待后台协程退出。
// 可重复调用。
func (r *Recorder) Close() {
	r.closeOnce.Do(func() { close(r.stop) })
	<-r.done
}

func (r *Recorder) run(insertTimeout time.Duration) {
	defer close(r.done)
	ticker := time.NewTicker(r.every)
	defer ticker.Stop()

	buf := make([]UsageEvent, 0, r.batch)
	flush := func() {
		if len(buf) == 0 {
			return
		}
		if r.inserter == nil {
			r.flushed.Add(int64(len(buf)))
			buf = buf[:0]
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), insertTimeout)
		err := r.inserter.InsertUsage(ctx, buf)
		cancel()
		if err != nil {
			// 写入失败不重试、不 panic、不阻塞数据面；计数以便告警。
			r.failed.Add(int64(len(buf)))
		} else {
			r.flushed.Add(int64(len(buf)))
		}
		buf = buf[:0]
	}

	for {
		select {
		case event := <-r.queue:
			buf = append(buf, event)
			if len(buf) >= r.batch {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-r.stop:
			// 关闭前把队列里已有的记录尽量刷出，再退出。
			for {
				select {
				case event := <-r.queue:
					buf = append(buf, event)
					if len(buf) >= r.batch {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}
