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
	endOfFile = TokenKind(iota)
	Comment   = TokenKind(iota)
	Indent
	Outdent
	MapKey
	ListItem
	Value
	NoValue
	MultilineValue
	MultilineHint
	Error
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
	case Value:
		return "Value"
	case NoValue:
		return "NoValue"
	case MultilineValue:
		return "MultilineValue"
	case MultilineHint:
		return "MultilineHint"
	case Error:
		return "Error"
	case endOfFile:
		return "EndOfFile"
	default:
		panic("Unknown TokenKind")
	}
}

func (k TokenKind) GoString() string {
	return k.String()
}

type Token struct {
	Kind    TokenKind
	Content string
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
			return strings.TrimRight(before, " \t"), strings.TrimLeft(after, " \t")
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
	escapeRegex  = regexp.MustCompile(`\\(\{.*\}?|.)`)
)

func decodeLiteral(input string) (string, string) {
	if !utf8.ValidString(input) {
		return "", "invalid UTF-8"
	}
	if !strings.HasPrefix(input, `"`) {
		return input, ""
	}

	match := literalRegex.FindStringSubmatch(input)
	if match == nil {
		return "", "unclosed quotes"
	}
	if len(match[0]) != len(input) {
		return "", "characters after quotes"
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
		return "", fmt.Sprintf("invalid escape code: %s", badEscape)
	}
	return result, ""
}

func tokenize(input string) iter.Seq2[int, Token] {
	return func(yield func(int, Token) bool) {

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
						if !yield(multilineLno, Token{Kind: MultilineValue, Content: strings.TrimRight(multilineValue, " \t\r\n")}) {
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
				if !yield(lno, Token{Kind: Comment, Content: comment}) {
					return
				}
				continue
			}

			for !strings.HasPrefix(indent, stack[len(stack)-1]) {
				stack = stack[:len(stack)-1]
				if !yield(lno, Token{Kind: Outdent, Content: ""}) {
					return
				}
			}

			if indent != stack[len(stack)-1] {
				stack = append(stack, indent)
				if !yield(lno, Token{Kind: Indent, Content: indent}) {
					return
				}
			}

			if list, found := strings.CutPrefix(rest, "="); found {
				rest = strings.TrimLeft(list, " \t")
				if !yield(lno, Token{Kind: ListItem, Content: ""}) {
					return
				}
			} else {
				key, value := splitLiteral(rest, true)
				if !yield(lno, Token{Kind: MapKey, Content: key}) {
					return
				}
				rest = value
				rest = strings.TrimLeft(value, " \t")
				rest = strings.TrimPrefix(rest, "=")
				rest = strings.TrimLeft(rest, " \t")
			}

			if comment, found := strings.CutPrefix(rest, ";"); found {
				if !yield(lno, Token{Kind: Comment, Content: comment}) {
					return
				}
				continue
			}

			if indicator, found := strings.CutPrefix(rest, `"""`); found {
				indicator, rest := splitLiteral(indicator, false)
				multiline = true
				if !yield(lno, Token{Kind: MultilineHint, Content: indicator}) {
					return
				}

				if comment, found := strings.CutPrefix(rest, ";"); found {
					if !yield(lno, Token{Kind: Comment, Content: comment}) {
						return
					}
				}
				continue
			}

			value, rest := splitLiteral(rest, false)
			if value != "" {
				if !yield(lno, Token{Kind: Value, Content: value}) {
					return
				}
			}

			if comment, found := strings.CutPrefix(rest, ";"); found {
				if !yield(lno, Token{Kind: Comment, Content: comment}) {
					return
				}
			}
		}

		if multilineValue != "" {
			yield(multilineLno, Token{Kind: MultilineValue, Content: strings.TrimRight(multilineValue, " \t\r\n")})
		}
	}
}

type parseState int

const (
	stateUnknown = parseState(iota)
	stateListItem
	stateListValue
	stateListMultiline
	stateMapKey
	stateMapValue
	stateMapMultiline
)

type errorStop struct{}

var errStop = errorStop{}

// Tokens iterates over tokens in the input string with their associated
// (1-based) line number. The output is normalized to make it easier to consume.
// In particular [Indent] and [Outdent] are always paired correctly; and after a
// [ListItem] or a [MapKey], you are guaranteed to see a value or an [Error].
//
// If you only care about the meaning of the document, you can filter out
// [Comment] and [MultilineHint] tokens.
//
// An [Error] token is yielded for each error encountered during parsing.
// Parsers can choose to stop at the first error or keep going knowing that the
// resulting document may be invalid.
func Tokens(input string) iter.Seq2[int, Token] {
	states := []parseState{stateUnknown}
	lastLine := 0

	return func(yieldTo func(int, Token) bool) {
		defer func() {
			if r := recover(); r != nil {
				if r == errStop {
					return
				}
				panic(r)
			}
		}()

		yield := func(lno int, token Token) {
			if !yieldTo(lno, token) {
				panic(errStop)
			}
		}

		yieldDecoded := func(lno int, token Token) {
			value, err := decodeLiteral(token.Content)
			if err != "" {
				yield(lno, Token{Kind: Error, Content: err})
				return
			}

			token.Content = value
			yield(lno, token)
		}

		yieldChecked := func(lno int, token Token) {
			if !utf8.ValidString(token.Content) {
				yield(lno, Token{Kind: Error, Content: "invalid UTF-8"})
				return
			}
			yield(lno, token)
		}

		yieldMultiline := func(lno int, token Token) {
			value, err := decodeMultiline(token.Content)
			if err != "" {
				yield(lno, Token{Kind: Error, Content: err})
				return
			}

			token.Content = value
			yield(lno, token)
		}

		for lno, token := range tokenize(input) {
			lastLine = lno
			state := states[len(states)-1]
			switch token.Kind {
			case Comment:
				yieldChecked(lno, token)

			case Indent:
				states = append(states, stateUnknown)
				switch state {
				case stateListValue:
					states[len(states)-2] = stateListItem
					yield(lno, token)
				case stateMapValue:
					states[len(states)-2] = stateMapKey
					yield(lno, token)
				default:
					yield(lno, Token{Kind: Error, Content: "unexpected indent"})
				}

			case Outdent:
				states = states[:len(states)-1]
				switch state {
				case stateListItem, stateMapKey:
					yield(lno, token)
				case stateListValue, stateMapValue:
					yield(lno, Token{Kind: NoValue, Content: ""})
					yield(lno, token)
				default:
					yield(lno, Token{Kind: Error, Content: "unexpected outdent"})
				}

			case ListItem:
				switch state {
				case stateUnknown, stateListItem:
					states[len(states)-1] = stateListValue
					yield(lno, token)
				case stateListValue:
					yield(lno, Token{Kind: NoValue, Content: ""})
					yield(lno, token)
				default:
					yield(lno, Token{Kind: Error, Content: "unexpected list item"})
				}

			case MapKey:
				switch state {
				case stateUnknown, stateMapKey:
					states[len(states)-1] = stateMapValue
					yieldDecoded(lno, token)
				case stateMapValue:
					yield(lno, Token{Kind: NoValue, Content: ""})
					yieldDecoded(lno, token)
				default:
					yield(lno, Token{Kind: Error, Content: "unexpected map key"})
				}

			case Value:
				switch state {
				case stateListValue:
					states[len(states)-1] = stateListItem
					yieldDecoded(lno, token)
				case stateMapValue:
					states[len(states)-1] = stateMapKey
					yieldDecoded(lno, token)
				default:
					yield(lno, Token{Kind: Error, Content: "unexpected value"})
				}

			case MultilineHint:
				switch state {
				case stateListValue:
					states[len(states)-1] = stateListMultiline
					yieldChecked(lno, token)
				case stateMapValue:
					states[len(states)-1] = stateMapMultiline
					yieldChecked(lno, token)
				default:
					yield(lno, Token{Kind: Error, Content: "unexpected multiline hint"})
				}

			case MultilineValue:
				switch state {
				case stateListValue, stateListMultiline:
					states[len(states)-1] = stateListItem
					yieldMultiline(lno, token)
				case stateMapValue, stateMapMultiline:
					states[len(states)-1] = stateMapKey
					yieldMultiline(lno, token)

				default:
					yield(lno, Token{Kind: Error, Content: "unexpected value"})
				}

			default:
				panic("Unknown token kind")
			}
		}

		for len(states) > 1 {
			switch states[len(states)-1] {
			case stateListValue, stateMapValue:
				yield(lastLine, Token{Kind: NoValue, Content: ""})
				yield(lastLine, Token{Kind: Outdent, Content: ""})
			case stateListItem, stateMapKey:
				yield(lastLine, Token{Kind: Outdent, Content: ""})
			default:
				yield(lastLine, Token{Kind: Error, Content: "missing value"})
			}
			states = states[:len(states)-1]
		}
		switch states[len(states)-1] {
		case stateListValue, stateMapValue:
			yield(lastLine, Token{Kind: NoValue, Content: ""})
		case stateListItem, stateMapKey, stateUnknown:
			// do nothing
		default:
			yield(lastLine, Token{Kind: Error, Content: "missing value"})
		}
	}
}
