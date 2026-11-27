package compliance

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

type textFilterContextKey struct{}

func WithTextFilter(ctx context.Context, filter *TextFilter) context.Context {
	return context.WithValue(ctx, textFilterContextKey{}, filter)
}

func TextFilterFromContext(ctx context.Context) *TextFilter {
	filter, _ := ctx.Value(textFilterContextKey{}).(*TextFilter)
	return filter
}

// TextFilter is an immutable, case-insensitive literal word matcher. Its zero
// value passes text through. Each concurrent stream must use its own TextStream.
type TextFilter struct {
	pattern  *regexp.Regexp
	maxRunes int
	action   string
	mask     string
}

func newTextFilter(words map[string]struct{}, action, mask string) (*TextFilter, error) {
	if action == "" {
		action = "mask"
	}
	if mask == "" {
		mask = "*"
	}
	if action != "mask" && action != "remove" {
		return nil, fmt.Errorf("sensitive word filter action must be mask or remove")
	}
	if !utf8.ValidString(mask) || utf8.RuneCountInString(mask) != 1 {
		return nil, fmt.Errorf("sensitive word filter mask must be one Unicode character")
	}
	f := &TextFilter{action: action, mask: mask}
	patterns := make([]string, 0, len(words))
	for word := range words {
		if word == "" {
			continue
		}
		patterns = append(patterns, regexp.QuoteMeta(word))
		f.maxRunes = max(f.maxRunes, utf8.RuneCountInString(word))
	}
	if len(patterns) == 0 {
		return f, nil
	}
	sort.Strings(patterns)
	pattern, err := regexp.Compile("(?i:" + strings.Join(patterns, "|") + ")")
	if err != nil {
		// Compilation errors can include the dictionary. Do not expose its terms.
		return nil, fmt.Errorf("sensitive word dictionary could not be compiled")
	}
	pattern.Longest()
	f.pattern = pattern
	return f, nil
}

func (f *TextFilter) Enabled() bool { return f != nil && f.pattern != nil }

// Filter replaces non-overlapping, leftmost-longest matches in the original text.
func (f *TextFilter) Filter(text string) string {
	if !f.Enabled() || text == "" {
		return text
	}
	return f.pattern.ReplaceAllStringFunc(text, f.replacement)
}

func (f *TextFilter) replacement(text string) string {
	if f.action == "remove" {
		return ""
	}
	return strings.Repeat(f.mask, utf8.RuneCountInString(text))
}

// TextStream retains at most the longest term's unresolved suffix, including
// incomplete UTF-8 runes, so a word split between writes cannot escape filtering.
type TextStream struct {
	filter  *TextFilter
	pending string
}

func (f *TextFilter) Stream() *TextStream { return &TextStream{filter: f} }

func (s *TextStream) Write(text string) string {
	if !s.filter.Enabled() {
		return text
	}
	s.pending += text
	end := len(s.pending)
	for i := end - 1; i >= max(0, end-utf8.UTFMax); i-- {
		if utf8.RuneStart(s.pending[i]) {
			if !utf8.FullRuneInString(s.pending[i:]) {
				end = i
			}
			break
		}
	}
	cutoff := end
	for i := 0; i < s.filter.maxRunes-1 && cutoff > 0; i++ {
		_, size := utf8.DecodeLastRuneInString(s.pending[:cutoff])
		cutoff -= size
	}
	if cutoff == 0 {
		return ""
	}
	var out strings.Builder
	consumed := 0
	for _, match := range s.filter.pattern.FindAllStringIndex(s.pending[:end], -1) {
		if match[0] >= cutoff {
			break
		}
		out.WriteString(s.pending[consumed:match[0]])
		out.WriteString(s.filter.replacement(s.pending[match[0]:match[1]]))
		consumed = match[1]
		cutoff = max(cutoff, consumed)
	}
	out.WriteString(s.pending[consumed:cutoff])
	s.pending = strings.Clone(s.pending[cutoff:])
	return out.String()
}

func (s *TextStream) Flush() string {
	text := s.filter.Filter(s.pending)
	s.pending = ""
	return text
}

// FilterFields copies JSON-shaped display data, filtering text fields while
// retaining identifiers, URLs, scores, and object keys verbatim.
func (f *TextFilter) FilterFields(value any) any {
	if !f.Enabled() {
		return value
	}
	switch value := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, item := range value {
			switch key {
			case "content", "content_with_weight", "answer", "thinking", "reasoning", "text", "snippet", "highlight", "title", "doc_name", "document_name", "docnm_kwd", "document_keyword", "important_keywords", "questions", "result", "description", "thoughts", "message", "error":
				out[key] = f.filterTextValue(item)
			default:
				out[key] = f.FilterFields(item)
			}
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, len(value))
		for i, item := range value {
			out[i] = f.FilterFields(item).(map[string]any)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = f.FilterFields(item)
		}
		return out
	}
	return value
}

func (f *TextFilter) filterTextValue(value any) any {
	switch value := value.(type) {
	case string:
		return f.Filter(value)
	case []string:
		out := make([]string, len(value))
		for i, item := range value {
			out[i] = f.Filter(item)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = f.filterTextValue(item)
		}
		return out
	default:
		return f.FilterFields(value)
	}
}

// FilterValues filters arbitrary component input/output values for display.
// Resource identities and links remain usable; the original values are not changed.
func (f *TextFilter) FilterValues(value any) any {
	if !f.Enabled() {
		return value
	}
	switch value := value.(type) {
	case string:
		return f.Filter(value)
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, item := range value {
			if key == "id" || key == "url" || strings.HasSuffix(key, "_id") || strings.HasSuffix(key, "_ids") || strings.HasSuffix(key, "_url") {
				out[key] = item
			} else {
				out[key] = f.FilterValues(item)
			}
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = f.FilterValues(item)
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, len(value))
		for i, item := range value {
			out[i] = f.FilterValues(item).(map[string]any)
		}
		return out
	case []string:
		return f.filterTextValue(value)
	}
	return value
}
