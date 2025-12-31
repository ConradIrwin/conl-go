package conl

import (
	"fmt"
	"iter"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// TokenKind represents the possible kinds of token in a CONL document.
type TokenKind int8

// These tokens are yielded from [Tokens].
const (
	Comment = TokenKind(iota)
	Indent
	Outdent
	MapKey
	ListItem
	Scalar
	NoValue
	MultilineScalar
	MultilineHint
)

func (k TokenKind) String() string {
	switch k {
	case Comment:
		return "Comment"
	case Indent:
		return "Indent"
	case Outdent:
		return "Outdent"
	case MapKey:
		return "MapKey"
	case ListItem:
		return "ListItem"
	case Scalar:
		return "Value"
	case NoValue:
		return "NoValue"
	case MultilineScalar:
		return "MultilineValue"
	case MultilineHint:
		return "MultilineHint"
	default:
		panic("Unknown TokenKind")
	}
}

func (k TokenKind) GoString() string {
	return k.String()
}

type Token struct {
	Lno     int
	Kind    TokenKind
	Content string
	Error   error
}

var lineRegexp = regexp.MustCompile("\r\n|\r|\n")

func lines(input string) iter.Seq2[int, string] {
	return func(yield func(int, string) bool) {
		lno := 1
		for match := lineRegexp.FindStringIndex(input); match != nil; match = lineRegexp.FindStringIndex(input) {
			if !yield(lno, input[:match[0]]) {
				return
			}
			input = input[match[1]:]
			lno++
		}
		yield(lno, input)
	}
}

func splitLiteral(input string, key bool) (before, after string) {
	if strings.HasPrefix(input, "\"") {
		wasEscape := false
		for i, c := range input[1:] {
			if c == '"' && !wasEscape {
				if before, after := splitUnquoted(input[i+1:], key); before != "" {
					return input[:i+1] + before, after
				} else {
					return input[:i+1], after
				}
			}
			wasEscape = c == '\\' && !wasEscape
		}
		return input, ""
	}

	return splitUnquoted(input, key)
}

func splitUnquoted(input string, key bool) (before, after string) {
	if key {
		before, after, found := strings.Cut(input, "=")
		if found {
			if i := strings.Index(before, ";"); i >= 0 {
				return strings.TrimRight(before[:i], " \t"), input[i:]
			}
			return strings.TrimRight(before, " \t"), after
		}
	}

	if i := strings.Index(input, ";"); i >= 0 {
		return strings.TrimRight(input[:i], " \t"), input[i:]
	}
	return strings.TrimRight(input, " \t"), ""
}

func decodeMultiline(input string) (string, string) {
	if !utf8.ValidString(input) {
		return "", "invalid UTF-8"
	}
	return input, ""
}

var (
	literalRegex = regexp.MustCompile(`^"((?:\\.|[^\\"])*)"`)
	escapeRegex  = regexp.MustCompile(`\\(\{[^}]*\}?|.)`)
)

func decodeLiteral(input string) (string, error) {
	if !utf8.ValidString(input) {
		return "", fmt.Errorf("invalid UTF-8")
	}
	if !strings.HasPrefix(input, `"`) {
		return input, nil
	}

	match := literalRegex.FindStringSubmatch(input)
	if match == nil {
		return "", fmt.Errorf("unclosed quotes")
	}
	if len(match[0]) != len(input) {
		return "", fmt.Errorf("characters after quotes")
	}

	var badEscape string
	result := escapeRegex.ReplaceAllStringFunc(match[1], func(escape string) string {
		switch escape[1] {
		case 'n':
			return "\n"
		case 'r':
			return "\r"
		case 't':
			return "\t"
		case '"', '\\':
			return string(escape[1])
		case '{':
			if escape[len(escape)-1] != '}' || len(escape) == 3 || len(escape) > 11 {
				break
			}
			codePoint, err := strconv.ParseInt(escape[2:len(escape)-1], 16, 32)
			if err != nil || !utf8.ValidRune(rune(codePoint)) {
				break
			}
			return string(rune(codePoint))
		}
		if badEscape == "" {
			badEscape = escape
		}
		return escape
	})
	if badEscape != "" {
		return "", fmt.Errorf("invalid escape code: %s", badEscape)
	}
	return result, nil
}

func checkUtf8(content string) error {
	if !utf8.ValidString(content) {
		return fmt.Errorf("invalid UTF-8")
	}
	return nil
}

func tokenize(input string) iter.Seq[Token] {
	return func(yield func(Token) bool) {

		stack := []string{""}
		multiline := false
		multilinePrefix := ""
		multilineValue := ""
		multilineLno := 0

		for lno, content := range lines(input) {
			rest := strings.TrimLeft(content, " \t")
			indent := content[0 : len(content)-len(rest)]

			if multiline {
				if multilinePrefix == "" {
					if strings.HasPrefix(indent, stack[len(stack)-1]) && indent != stack[len(stack)-1] {
						multilinePrefix = indent
						multilineValue = rest
						multilineLno = lno
						continue
					} else if rest == "" {
						continue
					} else {
						if !yield(Token{Lno: multilineLno, Kind: MultilineScalar, Error: fmt.Errorf("missing multiline value")}) {
							return
						}
						multiline = false
					}
				} else {
					if rest, found := strings.CutPrefix(content, multilinePrefix); found {
						multilineValue += "\n" + rest
						continue
					} else if rest == "" {
						multilineValue += "\n"
						continue
					} else {
						content := strings.TrimRight(multilineValue, " \t\r\n")
						err := checkUtf8(content)
						if !yield(Token{Lno: multilineLno, Kind: MultilineScalar, Content: content, Error: err}) {
							return
						}
						multiline = false
						multilinePrefix = ""
						multilineValue = ""
					}
				}
			}

			if rest == "" {
				continue
			}

			if comment, found := strings.CutPrefix(rest, ";"); found {
				if !yield(Token{Lno: lno, Kind: Comment, Content: comment, Error: checkUtf8(comment)}) {
					return
				}
				continue
			}

			for !strings.HasPrefix(indent, stack[len(stack)-1]) {
				stack = stack[:len(stack)-1]
				if !yield(Token{Lno: lno, Kind: Outdent, Content: ""}) {
					return
				}
			}

			if indent != stack[len(stack)-1] {
				stack = append(stack, indent)
				if !yield(Token{Lno: lno, Kind: Indent, Content: indent}) {
					return
				}
			}

			if list, found := strings.CutPrefix(rest, "="); found {
				rest = strings.TrimLeft(list, " \t")
				if !yield(Token{Lno: lno, Kind: ListItem, Content: ""}) {
					return
				}
			} else {
				key, value := splitLiteral(rest, true)
				content, err := decodeLiteral(key)
				if !yield(Token{Lno: lno, Kind: MapKey, Content: content, Error: err}) {
					return
				}
				rest = value
				rest = strings.TrimLeft(value, " \t")
				rest = strings.TrimPrefix(rest, "=")
				rest = strings.TrimLeft(rest, " \t")
			}

			if comment, found := strings.CutPrefix(rest, ";"); found {
				if !yield(Token{Lno: lno, Kind: Comment, Content: comment, Error: checkUtf8(comment)}) {
					return
				}
				continue
			}

			if indicator, found := strings.CutPrefix(rest, `"""`); found {
				indicator, rest := splitLiteral(indicator, false)
				multiline = true
				multilineLno = lno
				err := checkUtf8(indicator)
				if strings.HasPrefix(indicator, "\"") {
					err = fmt.Errorf("characters after quotes")
				}
				if !yield(Token{Lno: lno, Kind: MultilineHint, Content: indicator, Error: err}) {
					return
				}

				if comment, found := strings.CutPrefix(rest, ";"); found {
					if !yield(Token{Lno: lno, Kind: Comment, Content: comment, Error: checkUtf8(comment)}) {
						return
					}
				}
				continue
			}

			value, rest := splitLiteral(rest, false)
			if value != "" {
				content, err := decodeLiteral(value)
				if !yield(Token{Lno: lno, Kind: Scalar, Content: content, Error: err}) {
					return
				}
			}

			if comment, found := strings.CutPrefix(rest, ";"); found {
				if !yield(Token{Lno: lno, Kind: Comment, Content: comment, Error: checkUtf8(comment)}) {
					return
				}
			}
		}

		if multiline {
			if multilineValue != "" {
				content := strings.TrimRight(multilineValue, " \t\r\n")
				yield(Token{Lno: multilineLno, Kind: MultilineScalar, Content: content, Error: checkUtf8(content)})
			} else {
				yield(Token{Lno: multilineLno, Kind: MultilineScalar, Error: fmt.Errorf("missing multiline value")})
			}
		}
	}
}

type parseState struct {
	kind   TokenKind
	hasKey bool
}

// Tokens iterates over tokens in the input string.
//
// The raw tokens are post-processed to maintain the invariants that:
//   - [Indent] and [Outdent] are always paired correctly
//   - (ignoring [Comment]) after a [ListItem] or a [MapKey],
//     you will always get any of [Value], [MultilineHint], [NoValue] or [Indent]
//   - after a [MultilineHint] you will always get a [MultilineValue]
//   - within a given section you will only find [ListItem] or [MapKey], not a mix.
//
// Any parse errors are reported in Token.Error. The parser is tolerant to errors,
// though the resulting document may not be what the user intended, so you should
// handle errors appropriately.
func Tokens(input []byte) iter.Seq[Token] {
	states := []parseState{{}}
	lastLine := 0

	return func(yield func(Token) bool) {
		for token := range tokenize(string(input)) {
			state := &states[len(states)-1]
			switch token.Kind {
			case Indent:
				if state.hasKey {
					state.hasKey = false
				} else {
					kind := state.kind
					if kind == 0 {
						kind = MapKey
					}
					if !yield(Token{Lno: token.Lno, Kind: kind, Error: fmt.Errorf("unexpected indent"), Content: ""}) {
						return
					}
				}
				states = append(states, parseState{})
			case Outdent:
				states = states[:len(states)-1]
				if state.hasKey {
					if !yield(Token{Lno: token.Lno, Kind: NoValue}) {
						return
					}
				}
			case ListItem, MapKey:
				if state.kind == 0 {
					state.kind = token.Kind
				}
				if state.hasKey {
					if !yield(Token{Lno: token.Lno, Kind: NoValue}) {
						return
					}
				}
				state.hasKey = true
				if state.kind == MapKey && token.Kind == ListItem {
					if !yield(Token{Lno: token.Lno, Kind: MapKey, Error: fmt.Errorf("unexpected list item")}) {
						return
					}
					continue
				}
				if state.kind == ListItem && token.Kind == MapKey {
					if !yield(Token{Lno: token.Lno, Kind: ListItem, Error: fmt.Errorf("unexpected map key")}) {
						return
					}
					continue
				}
			case Scalar, MultilineScalar:
				state.hasKey = false

			case Comment, MultilineHint:
				// pass-through
			default:
				panic("Unknown token kind")
			}
			lastLine = token.Lno
			if !yield(token) {
				return
			}
		}

		for len(states) > 0 {
			state := states[len(states)-1]
			if state.hasKey {
				if !yield(Token{Lno: lastLine, Kind: NoValue, Content: ""}) {
					return
				}
			}
			if len(states) > 1 {
				if !yield(Token{Lno: lastLine, Kind: Outdent, Content: ""}) {
					return
				}
			}
			states = states[:len(states)-1]
		}
	}
}
