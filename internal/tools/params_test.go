package tools

import (
	"strings"
	"testing"
	"time"

	"zenmoney-mcp/internal/store"
)

func TestParsePeriod(t *testing.T) {
	tests := []struct {
		name     string
		prefix   string
		from, to string
		start    startFunc
		want     store.Period
		wantErr  string
	}{
		{"начало выводится из date_to", "date_", "", "2026-09-30", monthStart,
			store.Period{From: "2026-09-01", To: "2026-09-30"}, ""},
		{"30 дней включая конец", "date_", "", "2026-10-15", lastDays(30),
			store.Period{From: "2026-09-16", To: "2026-10-15"}, ""},
		{"явные границы", "date_", "2026-01-01", "2026-01-31", monthStart,
			store.Period{From: "2026-01-01", To: "2026-01-31"}, ""},
		{"начало позже конца", "date_", "2026-10-02", "2026-10-01", monthStart, store.Period{}, "date_from"},
		{"неверный формат", "date_", "01.10.2026", "", monthStart, store.Period{}, "date_from"},
		{"без умолчаний границы обязательны", "a_", "2026-10-01", "", nil, store.Period{}, "a_to"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePeriod(tt.prefix, tt.from, tt.to, tt.start)

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want ошибку про %s", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("parsePeriod() = %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

func TestParsePeriod_DefaultsToToday(t *testing.T) {
	today := time.Now()

	got, err := parsePeriod("date_", "", "", lastDays(30))
	if err != nil {
		t.Fatal(err)
	}
	if want := today.AddDate(0, 0, -29).Format(time.DateOnly); got.From != want || got.To != today.Format(time.DateOnly) {
		t.Errorf("parsePeriod() = %+v, want %s..сегодня", got, want)
	}
}
