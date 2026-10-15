package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"zenmoney-mcp/internal/config"
	"zenmoney-mcp/internal/store"
	"zenmoney-mcp/internal/tools"
	"zenmoney-mcp/internal/zenmoney"
)

// version уходит клиенту в ответе initialize.
const version = "0.1.0"

// syncTTL — возраст данных, после которого тулы синхронизируются сами.
const syncTTL = 10 * time.Minute

// Вся работа в run: os.Exit пропускает defer, а там закрывается база.
func main() {
	check := flag.Bool("check", false, "синхронизироваться один раз, вывести число счетов и транзакций и выйти")
	flag.Parse()

	if err := run(*check); err != nil {
		slog.Error("zenmoney-mcp failed", "error", err)
		os.Exit(1)
	}
}

func run(check bool) error {
	// stdout занят протоколом MCP, поэтому логи только в stderr.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		return err
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	syncer := store.NewSyncer(zenmoney.NewClient(zenmoney.BaseURL, cfg.Token), st, syncTTL)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if check {
		return runCheck(ctx, syncer, st)
	}

	server := tools.NewServer(st, syncer, slog.Default(), version)
	// Отмена по сигналу — штатная остановка контейнера, а не сбой.
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("mcp server: %w", err)
	}

	return nil
}

// runCheck — проверка токена и сети без MCP-клиента: один синк и итог в stdout.
func runCheck(ctx context.Context, syncer *store.Syncer, st *store.Store) error {
	if _, err := syncer.Sync(ctx); err != nil {
		return err
	}

	accounts, transactions, err := st.Counts(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("OK: %d счетов, %d транзакций\n", accounts, transactions)

	return nil
}
