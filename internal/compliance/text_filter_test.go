package compliance

import (
	"strings"
	"testing"
)

func TestTextFilter(t *testing.T) {
	words := map[string]struct{}{"secret": {}, "secret project": {}, "机密": {}, "a+b": {}}
	for _, tc := range []struct {
		action, mask, want string
	}{
		{"", "", "A ************** / ** / *** / secrets"},
		{"mask", "#", "A ############## / ## / ### / secrets"},
		{"mask", "🔒", "A " + strings.Repeat("🔒", 14) + " / 🔒🔒 / 🔒🔒🔒 / secrets"},
		{"remove", "*", "A  /  /  / secrets"},
	} {
		t.Run(tc.action+tc.mask, func(t *testing.T) {
			f, err := newTextFilter(words, tc.action, tc.mask)
			if err != nil {
				t.Fatal(err)
			}
			// Literal substring matching also masks the prefix in "secrets".
			want := strings.ReplaceAll(tc.want, "secrets", f.replacement("secret")+"s")
			input := "A SECRET project / 机密 / a+b / secrets"
			if got := f.Filter(input); got != want {
				t.Fatalf("Filter = %q, want %q", got, want)
			}
			// Exercise every byte boundary, including splits inside UTF-8 runes.
			for split := 0; split <= len(input); split++ {
				s := f.Stream()
				got := s.Write(input[:split]) + s.Write(input[split:]) + s.Flush()
				if got != want {
					t.Fatalf("split %d: got %q, want %q", split, got, want)
				}
			}
			s := f.Stream()
			var got strings.Builder
			for i := 0; i < len(input); i++ {
				got.WriteString(s.Write(input[i : i+1]))
			}
			got.WriteString(s.Flush())
			if got.String() != want {
				t.Fatalf("byte stream = %q, want %q", got.String(), want)
			}
		})
	}
}

func TestTextFilterOverlapAndPassThrough(t *testing.T) {
	f, err := newTextFilter(map[string]struct{}{"aba": {}, "ab": {}, "bab": {}}, "mask", "#")
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"abab", "ababa", "babab", "ordinary", "", "a"} {
		for split := 0; split <= len(input); split++ {
			s := f.Stream()
			if got := s.Write(input[:split]) + s.Write(input[split:]) + s.Flush(); got != f.Filter(input) {
				t.Fatalf("%q at %d: got %q, want %q", input, split, got, f.Filter(input))
			}
		}
	}
	var disabled TextFilter
	s := disabled.Stream()
	if got := s.Write("unchanged") + s.Flush(); got != "unchanged" {
		t.Fatalf("disabled stream = %q", got)
	}
}

func TestTextFilterFields(t *testing.T) {
	f, err := newTextFilter(map[string]struct{}{"secret": {}}, "mask", "#")
	if err != nil {
		t.Fatal(err)
	}
	chunk := map[string]any{"id": "secret", "content": "a secret", "document_keyword": "secret.txt", "url": "https://secret", "score": 0.7}
	got := f.FilterFields(map[string]any{"chunks": []map[string]any{chunk}}).(map[string]any)["chunks"].([]map[string]any)[0]
	if got["content"] != "a ######" || got["document_keyword"] != "######.txt" || got["id"] != "secret" || got["url"] != "https://secret" || got["score"] != 0.7 {
		t.Fatalf("filtered fields = %#v", got)
	}
	if chunk["content"] != "a secret" {
		t.Fatal("filter mutated its input")
	}
	values := f.FilterValues(map[string]any{"custom_result": []any{"secret", map[string]any{"doc_id": "secret", "value": "secret"}}}).(map[string]any)["custom_result"].([]any)
	if values[0] != "######" || values[1].(map[string]any)["doc_id"] != "secret" || values[1].(map[string]any)["value"] != "######" {
		t.Fatalf("filtered component values = %#v", values)
	}
}

func TestTextFilterInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct{ action, mask string }{{"replace", "*"}, {"mask", "ab"}, {"mask", "\xff"}} {
		if _, err := newTextFilter(nil, tc.action, tc.mask); err == nil {
			t.Fatalf("accepted invalid configuration %+v", tc)
		}
	}
}
