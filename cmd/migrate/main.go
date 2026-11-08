// Command migrate 显式执行数据库迁移与旧权限文件导入。
//
// 不在主进程启动时自动迁移：容器重启循环中反复迁移会放大故障，
// 且迁移失败应阻断启动而不是被掩盖。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"airouter/internal/store"
)

func main() {
	dsn := flag.String("dsn", os.Getenv("AIROUTER_DATABASE_URL"), "PostgreSQL DSN (defaults to AIROUTER_DATABASE_URL)")
	migrations := flag.String("migrations", "migrations", "directory containing goose-style SQL files")
	clientsFile := flag.String("clients", "", "optional clients.json to import after migrating")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if *dsn == "" {
		fmt.Fprintln(os.Stderr, "migrate: AIROUTER_DATABASE_URL is required")
		os.Exit(2)
	}
	db, err := store.Open(ctx, *dsn, store.PoolOptions{MaxConns: 2})
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
	defer db.Close()

	files, err := loadMigrations(*migrations)
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
	applied, err := db.Migrate(ctx, files)
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
	if len(applied) == 0 {
		fmt.Println("schema up to date")
	} else {
		for _, name := range applied {
			fmt.Println("applied", name)
		}
	}

	if *clientsFile != "" {
		inserted, skipped, err := importClients(ctx, db, *clientsFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "migrate:", err)
			os.Exit(1)
		}
		fmt.Printf("clients: inserted=%d skipped=%d\n", inserted, skipped)
	}
}

func loadMigrations(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("cannot read migrations directory")
	}
	files := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".sql" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("cannot read %s", e.Name())
		}
		files[e.Name()] = string(raw)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no .sql migrations found in %s", dir)
	}
	return files, nil
}

// importClients 读取旧 clients.json 并导入。
// 旧文件只有摘要，因此不生成新 Key：现有客户端无需更换凭据。
func importClients(ctx context.Context, db *store.DB, path string) (int, int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, fmt.Errorf("cannot read clients file")
	}
	var legacy []struct {
		ID            string   `json:"id"`
		KeySHA256     string   `json:"key_sha256"`
		AllowedModels []string `json:"allowed_models"`
		IsAdmin       bool     `json:"is_admin"`
	}
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return 0, 0, fmt.Errorf("clients file is not valid JSON")
	}
	// 稳定顺序便于审计复现
	sort.Slice(legacy, func(i, j int) bool { return legacy[i].ID < legacy[j].ID })

	clients := make([]store.LegacyClient, 0, len(legacy))
	for _, c := range legacy {
		clients = append(clients, store.LegacyClient{
			ID: c.ID, KeySHA256: c.KeySHA256, AllowedModels: c.AllowedModels, IsAdmin: c.IsAdmin,
		})
	}
	deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return db.MigrateClients(deadline, clients)
}
