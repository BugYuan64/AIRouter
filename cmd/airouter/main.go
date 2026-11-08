package main

import (
	"airouter/internal/access"
	"airouter/internal/admin"
	"airouter/internal/config"
	"airouter/internal/cpa"
	"airouter/internal/httpapi"
	"airouter/internal/store"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// version 传给管理面展示；构建时可用 -ldflags 覆盖。
var version = "dev"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, func(addr net.Addr) { log.Printf("AIRouter listening on %s", addr.String()) }); err != nil {
		fmt.Fprintln(os.Stderr, "AIRouter:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, started func(net.Addr)) error {
	cfg, err := config.Load(os.Getenv, os.ReadFile)
	if err != nil {
		return err
	}
	upstream, err := cpa.New(cfg.CPABaseURL, cfg.CPAAPIKey, cfg.UpstreamTimeout)
	if err != nil {
		return err
	}
	defer upstream.Close()

	// 用量记录：配置了数据库才启用，否则退化为不记录（不影响生成）。
	var recorder *store.Recorder
	var db *store.DB
	if cfg.DatabaseURL != "" {
		db, err = store.Open(ctx, cfg.DatabaseURL, store.PoolOptions{MaxConns: 10, MinConns: 2})
		if err != nil {
			return err
		}
		defer db.Close()
		recorder = store.NewRecorder(db.UsageInserter(), store.RecorderOptions{})
		defer recorder.Close()
	}
	// nil 指针赋给接口后接口不等于 nil，必须显式判空再赋值，
	// 否则 httpapi 会调用到 (*store.Recorder)(nil) 的方法。
	var usage httpapi.UsageRecorder
	if recorder != nil {
		usage = recorder
	}
	var usageReader store.UsageReader
	if db != nil {
		usageReader = db
	}

	// 认证来源：有数据库时用动态认证（支持即时撤权），否则回退到文件。
	var auth httpapi.Authenticator
	if db != nil {
		dynamic, authErr := access.NewDynamic(clientLookup{db})
		if authErr != nil {
			return authErr
		}
		auth = dynamic
	} else {
		static, authErr := access.New(cfg.Clients)
		if authErr != nil {
			return authErr
		}
		auth = static
	}
	// 单端口：数据面与控制台合并到同一个 http.Server。
	// 数据面按路径前缀分流，其余路径交给控制台。
	// 这样部署只有一个进程、一个端口，Caddy 也只需一行反代。
	dataPlane := httpapi.New(auth, upstream, httpapi.Options{
		RequestTimeout: cfg.RequestTimeout, MaxRequestBytes: cfg.MaxRequestBytes, Usage: usage,
	})
	handler := admin.Mount(dataPlane, admin.Options{
		AdminKey: cfg.AdminKey,
		Catalog:  upstream,
		Usage:    usageReader,
		Clients:  clientStore(db),
		Version:  version,
	})
	server := &http.Server{
		Handler: handler,
		// 不设 ReadTimeout/WriteTimeout 整体上限：流式生成可能持续数分钟，
		// 总时限由每个请求自身的 context 控制。
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return errors.New("cannot listen on configured address")
	}
	defer listener.Close()
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	started(listener.Addr())

	select {
	case err = <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("HTTP server stopped unexpectedly")
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err = server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return errors.New("HTTP shutdown exceeded its grace period")
		}
		err = <-result
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("HTTP server stopped unexpectedly")
	}
}

// startAdmin 在配置了管理地址时启动独立管理服务。
// 未配置（空字符串）时返回 nil，不启动管理面。
// clientStore 返回账户存储；db 为 nil 时返回 nil，让管理 API 明确返回 503。
func clientStore(db *store.DB) store.ClientStore {
	if db == nil {
		return nil
	}
	return db
}

// clientLookup 把 store.DB 适配为 access.Store，
// 避免 access 包直接依赖具体存储实现。
type clientLookup struct{ db *store.DB }

func (c clientLookup) Lookup(digest string) (*access.Account, error) {
	client, err := c.db.LookupByDigest(context.Background(), digest)
	if err != nil || client == nil {
		return nil, err
	}
	return &access.Account{ID: client.ID, IsAdmin: client.IsAdmin, Models: client.Models}, nil
}
