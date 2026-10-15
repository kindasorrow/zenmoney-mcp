package zenmoney

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testToken = "secret-token"

// В теле только две метки: любое другое поле /v8/diff/ принял бы за изменение данных.
func TestDiff_SendsOnlyTimestamps(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v8/diff/" {
			t.Errorf("запрос %s %s, want POST /v8/diff/", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("тело не JSON: %v", err)
		}

		_, _ = w.Write([]byte(`{
			"serverTimestamp": 200,
			"account": [{"id": "a1", "title": "Карта", "type": "ccard", "instrument": 2, "balance": 100.5, "archive": false}],
			"transaction": [{"id": "t1", "date": "2026-10-01", "income": 0, "outcome": 350,
				"tag": null, "merchant": null, "payee": null, "comment": null, "deleted": false}],
			"budget": [{"date": "2026-10-01"}]
		}`))
	}))
	defer srv.Close()

	diff, err := NewClient(srv.URL, testToken).Diff(context.Background(), 100)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	if len(body) != 2 {
		t.Fatalf("в теле запроса лишние поля: %v", body)
	}
	if body["serverTimestamp"] != float64(100) {
		t.Errorf("serverTimestamp = %v, want 100", body["serverTimestamp"])
	}
	if ts, _ := body["currentClientTimestamp"].(float64); ts < float64(time.Now().Add(-time.Minute).Unix()) {
		t.Errorf("currentClientTimestamp = %v, want текущее время", body["currentClientTimestamp"])
	}

	if diff.ServerTimestamp != 200 || len(diff.Account) != 1 || len(diff.Transaction) != 1 {
		t.Fatalf("дифф разобран неверно: %+v", diff)
	}
	if tx := diff.Transaction[0]; tx.Outcome != 350 || tx.Tag != nil || tx.Merchant != "" {
		t.Errorf("транзакция разобрана неверно: %+v", tx)
	}
}

// Протухший токен — частая поломка: ошибка должна отличаться от сетевой и не содержать токен.
func TestDiff_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, testToken).Diff(context.Background(), 0)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	assertNoToken(t, err)
}

func TestDiff_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"maintenance"}`, http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, testToken).Diff(context.Background(), 0)
	if err == nil || errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ошибку статуса", err)
	}
	if !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), "maintenance") {
		t.Errorf("в ошибке нет статуса или тела: %v", err)
	}
	assertNoToken(t, err)
}

func TestDiff_NetworkError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()

	_, err := NewClient(srv.URL, testToken).Diff(context.Background(), 0)
	if err == nil {
		t.Fatal("закрытый сервер обязан давать ошибку")
	}
	assertNoToken(t, err)
}

func TestDiff_RespectsDeadline(t *testing.T) {
	// Обрыв сервер замечает не всегда, поэтому обработчик отпускаем явно, иначе srv.Close зависнет.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := NewClient(srv.URL, testToken).Diff(ctx, 0)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

// Даже если API ответит редиректом, клиент не идёт на другой хост.
func TestDiff_DoesNotFollowRedirects(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("клиент ушёл по редиректу на другой хост")
	}))
	defer other.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/v8/diff/", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	if _, err := NewClient(srv.URL, testToken).Diff(context.Background(), 0); err == nil {
		t.Fatal("редирект обязан давать ошибку")
	}
}

func assertNoToken(t *testing.T, err error) {
	t.Helper()
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("токен попал в текст ошибки: %v", err)
	}
}
