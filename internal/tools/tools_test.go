package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"zenmoney-mcp/internal/report"
	"zenmoney-mcp/internal/store"
	"zenmoney-mcp/internal/zenmoney"
)

// В диффе расход в рублях, расход в долларах и перевод между своими счетами.
const testDiff = `{
	"serverTimestamp": 100,
	"instrument": [{"id": 1, "shortTitle": "USD"}, {"id": 2, "shortTitle": "RUB"}],
	"account": [
		{"id": "card", "title": "Карта", "type": "ccard", "instrument": 2, "balance": 5000, "archive": false},
		{"id": "savings", "title": "Накопления", "type": "deposit", "instrument": 2, "balance": 100000, "archive": false},
		{"id": "usd", "title": "Долларовая", "type": "ccard", "instrument": 1, "balance": 50, "archive": false}
	],
	"tag": [{"id": "food", "title": "Еда"}, {"id": "cafe", "title": "Кафе", "parent": "food"}],
	"transaction": [
		{"id": "t1", "date": "2026-10-01", "income": 0, "outcome": 300, "incomeAccount": "card", "outcomeAccount": "card",
		 "incomeInstrument": 2, "outcomeInstrument": 2, "tag": ["cafe"], "payee": "Кофейня"},
		{"id": "t2", "date": "2026-10-02", "income": 10000, "outcome": 10000, "incomeAccount": "savings", "outcomeAccount": "card",
		 "incomeInstrument": 2, "outcomeInstrument": 2},
		{"id": "t3", "date": "2026-10-03", "income": 0, "outcome": 15, "incomeAccount": "usd", "outcomeAccount": "usd",
		 "incomeInstrument": 1, "outcomeInstrument": 1, "payee": "Netflix"}
	]
}`

// connect поднимает сервер на фиктивном API: первый запрос получает testDiff, остальные — 503.
func connect(t *testing.T, ttl time.Duration) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) > 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(testDiff))
	}))
	t.Cleanup(api.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "zm.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	syncer := store.NewSyncer(zenmoney.NewClient(api.URL, "token"), st, ttl)
	server := NewServer(st, syncer, slog.New(slog.DiscardHandler), "test")

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

// callJSON разбирает последний блок ответа: перед JSON может стоять предупреждение.
func callJSON(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()

	res := call(t, cs, name, args)
	if res.IsError {
		t.Fatalf("%s вернул ошибку: %s", name, text(res, 0))
	}
	if err := json.Unmarshal([]byte(text(res, len(res.Content)-1)), out); err != nil {
		t.Fatalf("%s: ответ не JSON: %v", name, err)
	}
	return res
}

func text(res *mcp.CallToolResult, i int) string {
	return res.Content[i].(*mcp.TextContent).Text
}

// Сервер обещает клиенту, что ничего не меняет: тул без readOnlyHint должен ронять этот тест.
func TestTools_AllReadOnly(t *testing.T) {
	cs := connect(t, time.Hour)

	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	want := map[string]bool{
		"sync": true, "list_accounts": true, "list_transactions": true,
		"spending_summary": true, "compare_periods": true, "recurring_payments": true,
	}
	if len(res.Tools) != len(want) {
		t.Errorf("тулов %d, want %d", len(res.Tools), len(want))
	}
	for _, tool := range res.Tools {
		if !want[tool.Name] {
			t.Errorf("неожиданный тул %q", tool.Name)
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("тул %q без readOnlyHint", tool.Name)
		}
	}
}

func TestSpendingSummary_SkipsTransfersAndSplitsCurrencies(t *testing.T) {
	cs := connect(t, time.Hour)

	var got spendingSummary
	callJSON(t, cs, "spending_summary", map[string]any{"date_from": "2026-10-01", "date_to": "2026-10-31"}, &got)

	if len(got.Currencies) != 2 {
		t.Fatalf("валюты = %+v, want RUB и USD", got.Currencies)
	}
	rub, usd := got.Currencies[0], got.Currencies[1]
	if rub.Currency != "RUB" || rub.Total != 300 || len(rub.Groups) != 1 || rub.Groups[0].Key != "Еда" {
		t.Errorf("RUB = %+v, перевод не должен считаться тратой", rub)
	}
	if usd.Currency != "USD" || usd.Total != 15 {
		t.Errorf("USD = %+v", usd)
	}
}

func TestListTransactions_Transfer(t *testing.T) {
	cs := connect(t, time.Hour)

	var got transactionList
	callJSON(t, cs, "list_transactions", map[string]any{
		"date_from": "2026-10-01", "date_to": "2026-10-31", "type": "transfer",
	}, &got)

	if got.Matched != 1 || len(got.Items) != 1 {
		t.Fatalf("list_transactions = %+v", got)
	}
	item := got.Items[0]
	if item.Account != "Карта" || item.ToAccount != "Накопления" || item.Amount != 10000 || item.ToAmount != 0 {
		t.Errorf("перевод = %+v", item)
	}
}

func TestTools_RejectInvalidArguments(t *testing.T) {
	cs := connect(t, time.Hour)

	tests := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"spending_summary", map[string]any{"group_by": "year"}, "group_by"},
		{"list_transactions", map[string]any{"date_from": "01.10.2026"}, "date_from"},
		{"list_transactions", map[string]any{"limit": 5000}, "limit"},
		{"compare_periods", map[string]any{"a_from": "2026-10-01", "a_to": "2026-09-01", "b_from": "2026-10-01", "b_to": "2026-10-31"}, "a_from"},
	}

	for _, tt := range tests {
		t.Run(tt.tool+" "+tt.want, func(t *testing.T) {
			res := call(t, cs, tt.tool, tt.args)
			if !res.IsError || !strings.Contains(text(res, 0), tt.want) {
				t.Errorf("ответ = %s, want ошибку про %s", text(res, 0), tt.want)
			}
		})
	}
}

// Синк не удался, но копия есть: тул отвечает по ней и предупреждает.
func TestTools_ServeCachedDataWhenSyncFails(t *testing.T) {
	cs := connect(t, 0)

	var first []accountItem
	callJSON(t, cs, "list_accounts", nil, &first)

	var second []accountItem
	res := callJSON(t, cs, "list_accounts", nil, &second)

	if len(res.Content) != 2 || !strings.Contains(text(res, 0), "Синхронизация не удалась") {
		t.Fatalf("нет предупреждения о старых данных: %+v", res.Content)
	}
	if len(second) != 3 {
		t.Errorf("счета из кэша = %+v", second)
	}
}

// Пересекающиеся периоды грузятся одним запросом, но операция из общего дня считается в обоих.
func TestComparePeriods_OverlappingPeriods(t *testing.T) {
	cs := connect(t, time.Hour)

	var got []report.CurrencyComparison
	callJSON(t, cs, "compare_periods", map[string]any{
		"a_from": "2026-10-01", "a_to": "2026-10-01",
		"b_from": "2026-10-01", "b_to": "2026-10-31",
	}, &got)

	if len(got) != 2 || got[0].Currency != "RUB" || got[0].TotalA != 300 || got[0].TotalB != 300 {
		t.Fatalf("compare_periods = %+v", got)
	}
	if usd := got[1]; usd.TotalA != 0 || usd.TotalB != 15 {
		t.Errorf("USD = %+v", usd)
	}
}
