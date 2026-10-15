package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestLikeMatch(t *testing.T) {
	tests := []struct {
		pattern, s string
		escape     rune
		want       bool
	}{
		{"%еда%", "еда и кафе", -1, true},
		{"еда", "еда", -1, true},
		{"еда", "едаа", -1, false},
		{"_да", "еда", -1, true},
		{"_да", "ееда", -1, false},
		{"%", "", -1, true},
		{"", "", -1, true},
		{"", "а", -1, false},
		{"а%", "", -1, false},
		{"%а%б", "хахб", -1, true},
		{"%а%б", "хахбх", -1, false},
		{`50\%`, "50%", '\\', true},
		{`50\%`, "500", '\\', false},
		{`a\_b`, "a_b", '\\', true},
		{`a\_b`, "axb", '\\', false},
	}

	for _, tt := range tests {
		if got := likeMatch([]rune(tt.pattern), []rune(tt.s), tt.escape); got != tt.want {
			t.Errorf("likeMatch(%q, %q) = %v, want %v", tt.pattern, tt.s, got, tt.want)
		}
	}
}

// Модель напишет category LIKE '%еда%', а в базе «Еда»: без своих функций итог молча занижался бы.
func TestQuery_FoldsCyrillicCase(t *testing.T) {
	ctx := context.Background()
	st := openSyncedStore(t, mixedDiff)

	res, err := st.Query(ctx, `SELECT
		(SELECT count(*) FROM operations WHERE category LIKE '%еда%'),
		'Такси Москва' LIKE 'такси%',
		lower('ЕДА'), upper('кафе'),
		'50%' LIKE '50!%' ESCAPE '!',
		NULL LIKE 'а', lower(NULL)`)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}

	want := `[[1,1,"еда","КАФЕ",1,null,null]]`
	if got, _ := json.Marshal(res.Rows); string(got) != want {
		t.Errorf("строки = %s, want %s", got, want)
	}
}
