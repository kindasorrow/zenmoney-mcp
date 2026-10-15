package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"zenmoney-mcp/internal/zenmoney"
)

// t4 помечена удалённой и в базу никогда не попадала: удалённой её считать нельзя.
const fullDiff = `{
	"serverTimestamp": 100,
	"instrument": [{"id": 2, "shortTitle": "RUB"}],
	"account": [
		{"id": "card", "title": "Карта", "type": "ccard", "instrument": 2, "balance": 1000, "archive": false},
		{"id": "old", "title": "Старый вклад", "type": "deposit", "instrument": 2, "balance": 0, "archive": true}
	],
	"tag": [
		{"id": "food", "title": "Еда", "parent": null},
		{"id": "cafe", "title": "Кафе", "parent": "food"}
	],
	"merchant": [{"id": "m1", "title": "Кофейня"}],
	"transaction": [
		{"id": "t1", "date": "2026-10-01", "income": 0, "outcome": 300, "incomeAccount": "card", "outcomeAccount": "card",
		 "incomeInstrument": 2, "outcomeInstrument": 2, "tag": ["cafe"], "merchant": "m1", "payee": "COFFEE 123", "deleted": false},
		{"id": "t2", "date": "2026-10-02", "income": 0, "outcome": 500, "incomeAccount": "card", "outcomeAccount": "card",
		 "incomeInstrument": 2, "outcomeInstrument": 2, "tag": ["food"], "payee": "Пятёрочка", "deleted": false},
		{"id": "t3", "date": "2026-10-03", "income": 0, "outcome": 700, "incomeAccount": "card", "outcomeAccount": "card",
		 "incomeInstrument": 2, "outcomeInstrument": 2, "tag": null, "payee": "Такси", "deleted": false},
		{"id": "t4", "date": "2025-01-01", "income": 0, "outcome": 100, "incomeAccount": "card", "outcomeAccount": "card",
		 "incomeInstrument": 2, "outcomeInstrument": 2, "deleted": true}
	]
}`

// Инкремент: t1 изменилась, t2 удалена флагом, t3 — записью в deletion.
const incrementalDiff = `{
	"serverTimestamp": 200,
	"transaction": [
		{"id": "t1", "date": "2026-10-01", "income": 0, "outcome": 450, "incomeAccount": "card", "outcomeAccount": "card",
		 "incomeInstrument": 2, "outcomeInstrument": 2, "tag": ["cafe"], "merchant": "m1", "deleted": false},
		{"id": "t2", "date": "2026-10-02", "income": 0, "outcome": 500, "incomeAccount": "card", "outcomeAccount": "card",
		 "incomeInstrument": 2, "outcomeInstrument": 2, "tag": ["food"], "deleted": true}
	],
	"deletion": [
		{"id": "t3", "object": "transaction", "stamp": 150, "user": 1},
		{"id": "r1", "object": "reminder", "stamp": 150, "user": 1}
	]
}`

var year2026 = Period{From: "2026-01-01", To: "2026-12-31"}

// fakeAPI отдаёт ответы по очереди и запоминает присланные метки; с failing отвечает 503.
type fakeAPI struct {
	mu         sync.Mutex
	responses  []string
	timestamps []int64
	failing    bool
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServerTimestamp int64 `json:"serverTimestamp"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	f.mu.Lock()
	defer f.mu.Unlock()

	f.timestamps = append(f.timestamps, req.ServerTimestamp)
	if f.failing {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	resp := `{"serverTimestamp": 300}`
	if len(f.responses) > 0 {
		resp, f.responses = f.responses[0], f.responses[1:]
	}
	_, _ = w.Write([]byte(resp))
}

func (f *fakeAPI) setFailing(failing bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failing = failing
}

func (f *fakeAPI) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.timestamps)
}

func openTestStore(t *testing.T) *Store {
	t.Helper()

	st, err := Open(filepath.Join(t.TempDir(), "zm.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	return st
}

func newTestSyncer(t *testing.T, ttl time.Duration, responses ...string) (*Syncer, *Store, *fakeAPI) {
	t.Helper()

	api := &fakeAPI{responses: responses}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	st := openTestStore(t)

	return NewSyncer(zenmoney.NewClient(srv.URL, "token"), st, ttl), st, api
}

func TestSync_IncrementalWithDeletions(t *testing.T) {
	ctx := context.Background()
	syncer, st, api := newTestSyncer(t, time.Hour, fullDiff, incrementalDiff)

	first, err := syncer.Sync(ctx)
	if err != nil {
		t.Fatalf("первый синк: %v", err)
	}
	if first.Updated["transaction"] != 3 || first.Updated["account"] != 2 || first.Deleted != 0 {
		t.Errorf("первый синк: %+v", first.Changes)
	}

	second, err := syncer.Sync(ctx)
	if err != nil {
		t.Fatalf("второй синк: %v", err)
	}
	// Удаление напоминания база не хранит и не считает.
	if second.Updated["transaction"] != 1 || second.Deleted != 2 {
		t.Errorf("второй синк: %+v", second.Changes)
	}

	if len(api.timestamps) != 2 || api.timestamps[0] != 0 || api.timestamps[1] != 100 {
		t.Fatalf("метки запросов = %v, want [0 100]", api.timestamps)
	}

	txs, err := st.Transactions(ctx, year2026)
	if err != nil {
		t.Fatalf("Transactions: %v", err)
	}
	if len(txs) != 1 {
		t.Fatalf("осталось %d транзакций, want 1: %+v", len(txs), txs)
	}

	got := txs[0]
	if got.Outcome != 450 || got.Category != "Кафе" || got.ParentCategory != "Еда" ||
		got.Payee != "Кофейня" || got.OutcomeAccount != "Карта" || got.OutcomeCurrency != "RUB" {
		t.Errorf("транзакция собрана неверно: %+v", got)
	}

	ts, err := st.ServerTimestamp(ctx)
	if err != nil || ts != 200 {
		t.Errorf("ServerTimestamp = %d, %v; want 200", ts, err)
	}
}

// Дифф, полученный раньше, но записанный позже чужого, не должен откатить данные и метку.
func TestApply_SkipsOlderDiff(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	tx := func(outcome float64) zenmoney.Transaction {
		return zenmoney.Transaction{ID: "t1", Date: "2026-10-01", Outcome: outcome, OutcomeAccount: "card", IncomeAccount: "card"}
	}

	if _, err := st.Apply(ctx, &zenmoney.Diff{ServerTimestamp: 200, Transaction: []zenmoney.Transaction{tx(450)}}, time.Now()); err != nil {
		t.Fatalf("Apply 200: %v", err)
	}
	changes, err := st.Apply(ctx, &zenmoney.Diff{ServerTimestamp: 140, Transaction: []zenmoney.Transaction{tx(300)}}, time.Now())
	if err != nil {
		t.Fatalf("Apply 140: %v", err)
	}
	if len(changes.Updated) != 0 {
		t.Errorf("старый дифф применён: %+v", changes)
	}

	txs, err := st.Transactions(ctx, year2026)
	if err != nil || len(txs) != 1 || txs[0].Outcome != 450 {
		t.Errorf("Transactions = %+v, %v; want outcome 450", txs, err)
	}
	if ts, _ := st.ServerTimestamp(ctx); ts != 200 {
		t.Errorf("ServerTimestamp = %d, want 200", ts)
	}
}

// Без мерчанта получатель берётся из выписки, а сам текст выписки остаётся в RawPayee.
func TestTransactions_Payee(t *testing.T) {
	ctx := context.Background()
	syncer, st, _ := newTestSyncer(t, time.Hour, fullDiff)

	if _, err := syncer.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	txs, err := st.Transactions(ctx, Period{From: "2026-10-01", To: "2026-10-03"})
	if err != nil || len(txs) != 3 {
		t.Fatalf("Transactions: %v, %+v", err, txs)
	}

	taxi, coffee := txs[0], txs[2]
	if taxi.Payee != "Такси" || taxi.Category != "" {
		t.Errorf("операция без мерчанта: %+v", taxi)
	}
	if coffee.Payee != "Кофейня" || coffee.RawPayee != "COFFEE 123" {
		t.Errorf("операция с мерчантом: %+v", coffee)
	}
}

func TestTransactions_SeveralPeriods(t *testing.T) {
	ctx := context.Background()
	syncer, st, _ := newTestSyncer(t, time.Hour, fullDiff)

	if _, err := syncer.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	txs, err := st.Transactions(ctx,
		Period{From: "2026-10-01", To: "2026-10-01"},
		Period{From: "2026-10-03", To: "2026-10-03"},
	)
	if err != nil || len(txs) != 2 || txs[0].Date != "2026-10-03" || txs[1].Date != "2026-10-01" {
		t.Errorf("Transactions = %+v, %v; want 03 и 01 октября", txs, err)
	}
}

func TestAccounts_HidesArchived(t *testing.T) {
	ctx := context.Background()
	syncer, st, _ := newTestSyncer(t, time.Hour, fullDiff)

	if _, err := syncer.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	active, err := st.Accounts(ctx, false)
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(active) != 1 || active[0].Title != "Карта" || active[0].Currency() != "RUB" || active[0].Balance != 1000 {
		t.Errorf("действующие счета: %+v", active)
	}

	all, err := st.Accounts(ctx, true)
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(all) != 2 || !all[1].Archive {
		t.Errorf("все счета: %+v", all)
	}
}

func TestEnsureFresh(t *testing.T) {
	ctx := context.Background()

	t.Run("свежие данные не синкаются повторно", func(t *testing.T) {
		syncer, _, api := newTestSyncer(t, time.Hour, fullDiff)

		for range 3 {
			if err := syncer.EnsureFresh(ctx); err != nil {
				t.Fatalf("EnsureFresh: %v", err)
			}
		}
		if api.calls() != 1 {
			t.Errorf("запросов к API: %d, want 1", api.calls())
		}
	})

	t.Run("устаревшие данные синкаются", func(t *testing.T) {
		syncer, _, api := newTestSyncer(t, 0, fullDiff)

		for range 2 {
			if err := syncer.EnsureFresh(ctx); err != nil {
				t.Fatalf("EnsureFresh: %v", err)
			}
		}
		if len(api.timestamps) != 2 || api.timestamps[1] != 100 {
			t.Errorf("метки запросов = %v, want [0 100]", api.timestamps)
		}
	})

	// Недоступный API не должен держать каждый тул: после ошибки автосинк ждёт retryAfter,
	// а явный sync идёт в API сразу.
	t.Run("после ошибки пауза", func(t *testing.T) {
		syncer, _, api := newTestSyncer(t, 0, fullDiff)
		if err := syncer.EnsureFresh(ctx); err != nil {
			t.Fatalf("EnsureFresh: %v", err)
		}

		api.setFailing(true)
		first := syncer.EnsureFresh(ctx)
		second := syncer.EnsureFresh(ctx)
		if first == nil || !errors.Is(second, first) {
			t.Fatalf("ошибки = %v, %v; want одну и ту же", first, second)
		}
		if api.calls() != 2 {
			t.Errorf("запросов к API: %d, want 2", api.calls())
		}

		if _, err := syncer.Sync(ctx); err == nil || api.calls() != 3 {
			t.Errorf("явный sync: %v, запросов %d; want ошибку и 3 запроса", err, api.calls())
		}
	})

	t.Run("ожидание очереди прерывается отменой", func(t *testing.T) {
		syncer, _, _ := newTestSyncer(t, 0, fullDiff)
		if err := syncer.acquire(ctx); err != nil {
			t.Fatal(err)
		}
		defer syncer.release()

		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if err := syncer.EnsureFresh(canceled); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
}

// В файле вся финансовая история: права 0600 у самой базы, у журнала WAL
// и у файла с 0644 от прошлых запусков.
func TestOpen_PrivateFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zm.db")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	if _, err := st.Apply(context.Background(), &zenmoney.Diff{ServerTimestamp: 1}, time.Now()); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	for _, name := range []string{path, path + "-wal"} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("права %s = %o, want 600", filepath.Base(name), perm)
		}
	}
}

// DSN разбирается как URL: «#», «?» и «%» в пути не должны уводить базу в другой файл.
func TestOpen_PathWithSpecialChars(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a#b?c%d.db")

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	if _, err = st.Apply(context.Background(), &zenmoney.Diff{ServerTimestamp: 7}, time.Now()); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if name := e.Name(); name != "a#b?c%d.db" && name != "a#b?c%d.db-wal" && name != "a#b?c%d.db-shm" {
			t.Errorf("база создала посторонний файл %q", name)
		}
	}
	if ts, _ := st.ServerTimestamp(context.Background()); ts != 7 {
		t.Errorf("ServerTimestamp = %d, want 7", ts)
	}
}
