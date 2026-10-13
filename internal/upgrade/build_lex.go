package upgrade

import (
	"fmt"
	"strings"
	"unicode"
)

type buildToken struct {
	value, kind string
	start, end  int
}
type buildEdit struct {
	start, end    int
	value, reason string
}

// Tokenize the literal subset, preserving offsets and excluding comments.
// Strings are single-line; triple/slashy strings and escapes that could conceal
// declarations are refused, instead of approximating executable DSL semantics.
func lexBuild(data []byte, toml bool) ([]buildToken, error) {
	var tokens []buildToken
	for i := 0; i < len(data); {
		c := data[i]
		if unicode.IsSpace(rune(c)) {
			i++
			continue
		}
		if toml && c == '#' || !toml && i+1 < len(data) && string(data[i:i+2]) == "//" {
			for i < len(data) && data[i] != '\n' {
				i++
			}
			continue
		}
		if !toml && i+1 < len(data) && string(data[i:i+2]) == "/*" {
			j := strings.Index(string(data[i+2:]), "*/")
			if j < 0 {
				return nil, fmt.Errorf("unterminated build comment")
			}
			i += j + 4
			continue
		}
		start := i
		if c == '\'' || c == '"' || c == '`' {
			quote := c
			i++
			if i+1 < len(data) && data[i] == quote && data[i+1] == quote {
				return nil, fmt.Errorf("multiline build strings require a model-aware recipe")
			}
			for i < len(data) && data[i] != quote {
				if data[i] == '\n' || data[i] == '\r' {
					return nil, fmt.Errorf("multiline build strings require a model-aware recipe")
				}
				if data[i] == '\\' {
					return nil, fmt.Errorf("escaped build strings require a model-aware recipe")
				}
				i++
			}
			if i == len(data) {
				return nil, fmt.Errorf("unterminated build string")
			}
			kind := "string"
			if quote == '`' {
				kind = "identifier"
			}
			tokens = append(tokens, buildToken{string(data[start+1 : i]), kind, start, i + 1})
			i++
			continue
		}
		if c >= '0' && c <= '9' {
			i++
			for i < len(data) && (data[i] >= '0' && data[i] <= '9' || data[i] == '.') {
				i++
			}
			tokens = append(tokens, buildToken{string(data[start:i]), "number", start, i})
			continue
		}
		if c == '_' || unicode.IsLetter(rune(c)) {
			i++
			for i < len(data) && (unicode.IsLetter(rune(data[i])) || unicode.IsDigit(rune(data[i])) || data[i] == '_') {
				i++
			}
			tokens = append(tokens, buildToken{string(data[start:i]), "identifier", start, i})
			continue
		}
		tokens = append(tokens, buildToken{string(c), "symbol", i, i + 1})
		i++
	}
	return tokens, nil
}

func matchingTokens(tokens []buildToken) (map[int]int, error) {
	pairs := map[int]int{}
	var stack []int
	for i, t := range tokens {
		switch t.value {
		case "(", "{", "[":
			if t.kind == "symbol" {
				stack = append(stack, i)
			}
		case ")", "}", "]":
			if t.kind != "symbol" {
				continue
			}
			if len(stack) == 0 {
				return nil, fmt.Errorf("unbalanced build delimiter")
			}
			j := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if map[string]string{"(": ")", "{": "}", "[": "]"}[tokens[j].value] != t.value {
				return nil, fmt.Errorf("mismatched build delimiter")
			}
			pairs[j] = i
			pairs[i] = j
		}
	}
	if len(stack) != 0 {
		return nil, fmt.Errorf("unbalanced build delimiter")
	}
	return pairs, nil
}

func tokenEdit(t buildToken, value, reason string) buildEdit {
	if t.kind == "string" {
		return buildEdit{t.start + 1, t.end - 1, value, reason}
	}
	return buildEdit{t.start, t.end, value, reason}
}
