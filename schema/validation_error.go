package schema

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/ConradIrwin/conl-go"
)

// A ValidationError represents a single validation error.
// Use .Error() to get the message, and use .Lno to get the line number.
type ValidationError struct {
	msg string
	pos resultPos
}

func joinWithOr(items []string) string {
	if len(items) == 0 {
		return ""
	}
	if len(items) == 1 {
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
}

// Lno returns the 1-indexed line number on which the error occurred.
func (ve *ValidationError) Lno() int {
	if ve.pos == 0 {
		return 1
	}
	return ve.pos.Lno()
}

var quotedLiteral = regexp.MustCompile(`^"(?:[^\\"]|\\.)*"`)

// returns the range for the key or list item, value, and comment
func splitLine(line string) (int, int, int, int, int) {
	trimmed := strings.TrimLeft(line, " \t")
	startKey := len(line) - len(trimmed)
	trimmed =
		quotedLiteral.ReplaceAllStringFunc(trimmed, func(quoted string) string {
			return strings.Repeat("a", len(quoted))
		})

	endKey := len(line)
	startValue := len(line)
	if strings.HasPrefix(trimmed, "=") {
		endKey = startKey + 1
		startValue = endKey
	} else if found := strings.IndexAny(trimmed, "=;"); found > -1 {
		endKey = startKey + len(strings.TrimRight(trimmed[:found], " \t"))
		if trimmed[found] == '=' {
			startValue = startKey + found + 1
		} else {
			startValue = startKey + found
		}
	} else {
		endKey = startKey + len(strings.TrimRight(trimmed, " \t"))
	}
	valueHalf := line[startValue:]
	trimmed = strings.TrimLeft(valueHalf, " \t")
	startValue += len(valueHalf) - len(trimmed)

	trimmed =
		quotedLiteral.ReplaceAllStringFunc(trimmed, func(quoted string) string {
			return strings.Repeat("a", len(quoted))
		})

	endValue := len(line)
	startComment := len(line)
	if found := strings.Index(trimmed, ";"); found > -1 {
		endValue = startValue + len(strings.TrimRight(trimmed[:found], " \t"))
		startComment = startValue + found
	} else {
		endValue = startValue + len(strings.TrimRight(trimmed, " \t"))
	}

	return startKey, endKey, startValue, endValue, startComment
}

// RuneRange returns the 0-based utf-8 based range at which the error
// occurred (assuming that the provided line corresponds to Lno in the
// original document).
func (ve *ValidationError) RuneRange(line string) (int, int) {
	startKey, endKey, startValue, endValue, _ := splitLine(line)
	if ve.pos == 0 {
		return startKey, endValue
	}
	if ve.pos.isKey() || startValue == endValue {
		return startKey, endKey
	} else {
		return startValue, endValue
	}
}

// Msg returns a human-readable description of the problem suitable for
// showing to end-users.
func (ve *ValidationError) Msg() string {
	return ve.msg
}

// Error implements the error interface
func (ve *ValidationError) Error() string {
	return fmt.Sprintf("%d: %s", ve.Lno(), ve.Msg())
}

func buildError(pos resultPos, ms []*attempt) string {
	topP := 0
	msg := ""

	expected := []string{}
	missingKeys := []string{}

	addError := func(p int, newMsg string) {
		if p > topP {
			topP = p
			msg = newMsg
		}
	}

	for _, m := range ms {
		if m.ok {
			continue
		}

		if m.val.Scalar != nil && m.val.Scalar.Error != nil {
			addError(100, m.val.Scalar.Error.Error())
			continue
		}

		if m.matcher == nil {
			if m.val.Scalar.Kind == conl.ListItem {
				addError(90, "unexpected list item")
			} else if m.duplicate != nil {
				addError(90, "duplicate key "+m.duplicate.raw)
			} else {
				addError(90, "duplicate key "+m.val.Scalar.Content)
			}
			continue
		}

		if len(m.missingKeys) > 0 {
			for _, m := range m.missingKeys {
				missingKeys = append(missingKeys, suggestionsFromPattern(m.raw, true)...)
			}
			continue
		}

		if m.matcher.resolved == nil {
			if pos.isKey() {
				addError(80, "unexpected key "+m.val.Scalar.Content)
				continue
			}
			if m.val.Scalar == nil {
				expected = append(expected, "any scalar")
			} else {
				expected = append(expected, suggestionsFromPattern(m.matcher.raw, true)...)
			}
		} else {
			d := m.matcher.resolved
			if d.Keys != nil || d.RequiredKeys != nil {
				expected = append(expected, "a map")
			} else if d.Items != nil || d.RequiredItems != nil {
				if m.val.List != nil && len(m.val.List) < len(d.RequiredItems) {
					addError(40, fmt.Sprintf("missing required list item %d", len(d.RequiredItems)))
				} else {
					expected = append(expected, "a list")
				}
			} else if d.Scalar != nil || d.OneOf != nil {
				panic("unreachable")
			} else {
				expected = append(expected, "no value")
			}
		}
	}
	if topP > 50 {
		return msg
	}
	if len(expected) > 0 {
		slices.Sort(expected)
		expected = slices.Compact(expected)

		return "expected " + joinWithOr(expected)
	}
	if len(missingKeys) > 0 {
		slices.Sort(missingKeys)
		missingKeys = slices.Compact(missingKeys)

		return "missing required key " + joinWithOr(missingKeys)
	}

	return msg
}
