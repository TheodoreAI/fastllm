package harness

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// TokenType identifies the lexical category of a token for syntax highlighting.
type TokenType int

const (
	TokenPlain TokenType = iota
	TokenKeyword
	TokenTypeIdent
	TokenString
	TokenNumber
	TokenComment
	TokenFunction
	TokenOperator
)

// Token represents one lexical unit.
type Token struct {
	Type  TokenType
	Value string
}

// Language keywords definition
var (
	goKeywords = map[string]bool{
		"break": true, "case": true, "chan": true, "const": true, "continue": true,
		"default": true, "defer": true, "else": true, "fallthrough": true, "for": true,
		"func": true, "go": true, "goto": true, "if": true, "import": true,
		"interface": true, "map": true, "package": true, "range": true, "return": true,
		"select": true, "struct": true, "switch": true, "type": true, "var": true,
		"nil": true, "true": true, "false": true, "iota": true,
	}

	goTypes = map[string]bool{
		"bool": true, "byte": true, "complex64": true, "complex128": true,
		"error": true, "float32": true, "float64": true, "int": true, "int8": true,
		"int16": true, "int32": true, "int64": true, "rune": true, "string": true,
		"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
		"uintptr": true, "any": true, "comparable": true,
	}

	jsTsKeywords = map[string]bool{
		"async": true, "await": true, "break": true, "case": true, "catch": true,
		"class": true, "const": true, "continue": true, "debugger": true, "default": true,
		"delete": true, "do": true, "else": true, "enum": true, "export": true,
		"extends": true, "false": true, "finally": true, "for": true, "from": true,
		"function": true, "get": true, "if": true, "implements": true, "import": true,
		"in": true, "instanceof": true, "interface": true, "let": true, "new": true,
		"null": true, "of": true, "package": true, "private": true, "protected": true,
		"public": true, "return": true, "set": true, "static": true, "super": true,
		"switch": true, "this": true, "throw": true, "true": true, "try": true,
		"typeof": true, "undefined": true, "var": true, "void": true, "while": true,
		"with": true, "yield": true, "type": true, "as": true,
	}

	jsTsTypes = map[string]bool{
		"string": true, "number": true, "boolean": true, "any": true, "void": true,
		"never": true, "unknown": true, "object": true, "symbol": true, "bigint": true,
		"Array": true, "Promise": true, "Record": true, "Partial": true, "Map": true, "Set": true,
	}

	pyKeywords = map[string]bool{
		"False": true, "None": true, "True": true, "and": true, "as": true, "assert": true,
		"async": true, "await": true, "break": true, "class": true, "continue": true,
		"def": true, "del": true, "elif": true, "else": true, "except": true,
		"finally": true, "for": true, "from": true, "global": true, "if": true,
		"import": true, "in": true, "is": true, "lambda": true, "nonlocal": true,
		"not": true, "or": true, "pass": true, "raise": true, "return": true,
		"try": true, "while": true, "with": true, "yield": true, "match": true, "case": true,
	}

	pyTypes = map[string]bool{
		"int": true, "float": true, "str": true, "bool": true, "list": true,
		"dict": true, "set": true, "tuple": true, "bytes": true, "object": true,
		"Optional": true, "Union": true, "Any": true, "List": true, "Dict": true, "Set": true, "Tuple": true,
	}

	rustKeywords = map[string]bool{
		"as": true, "break": true, "const": true, "continue": true, "crate": true,
		"else": true, "enum": true, "extern": true, "false": true, "fn": true,
		"for": true, "if": true, "impl": true, "in": true, "let": true, "loop": true,
		"match": true, "mod": true, "move": true, "mut": true, "pub": true,
		"ref": true, "return": true, "self": true, "Self": true, "static": true,
		"struct": true, "super": true, "trait": true, "true": true, "type": true,
		"unsafe": true, "use": true, "where": true, "while": true, "async": true, "await": true,
	}

	cCppKeywords = map[string]bool{
		"auto": true, "break": true, "case": true, "char": true, "const": true,
		"continue": true, "default": true, "do": true, "double": true, "else": true,
		"enum": true, "extern": true, "float": true, "for": true, "goto": true,
		"if": true, "int": true, "long": true, "register": true, "return": true,
		"short": true, "signed": true, "sizeof": true, "static": true, "struct": true,
		"switch": true, "typedef": true, "union": true, "unsigned": true, "void": true,
		"volatile": true, "while": true, "class": true, "namespace": true, "new": true,
		"delete": true, "public": true, "private": true, "protected": true, "template": true,
		"typename": true, "using": true, "nullptr": true, "true": true, "false": true,
	}

	shKeywords = map[string]bool{
		"if": true, "then": true, "else": true, "elif": true, "fi": true,
		"case": true, "esac": true, "for": true, "while": true, "until": true,
		"do": true, "done": true, "in": true, "function": true, "select": true,
		"time": true, "return": true, "exit": true, "local": true, "export": true,
	}

	sqlKeywords = map[string]bool{
		"SELECT": true, "FROM": true, "WHERE": true, "INSERT": true, "INTO": true,
		"UPDATE": true, "DELETE": true, "CREATE": true, "TABLE": true, "DROP": true,
		"ALTER": true, "JOIN": true, "LEFT": true, "RIGHT": true, "INNER": true,
		"OUTER": true, "ON": true, "GROUP": true, "BY": true, "ORDER": true,
		"HAVING": true, "LIMIT": true, "OFFSET": true, "AND": true, "OR": true,
		"NOT": true, "NULL": true, "AS": true, "UNION": true, "ALL": true,
		"INDEX": true, "PRIMARY": true, "KEY": true, "DEFAULT": true, "SET": true,
	}
)

// DetectLanguage maps file path or extension to a canonical language identifier.
func DetectLanguage(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	base := strings.ToLower(filepath.Base(filePath))

	switch ext {
	case ".go":
		return "go"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".ts", ".mts", ".cts", ".tsx", ".jsx":
		return "typescript"
	case ".py", ".pyw":
		return "python"
	case ".rs":
		return "rust"
	case ".c", ".h":
		return "c"
	case ".cpp", ".cxx", ".cc", ".hpp", ".hxx":
		return "cpp"
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".toml":
		return "toml"
	case ".sh", ".bash", ".zsh":
		return "shell"
	case ".sql":
		return "sql"
	case ".md", ".markdown":
		return "markdown"
	case ".html", ".htm":
		return "html"
	case ".css", ".scss", ".sass":
		return "css"
	}

	switch base {
	case "dockerfile", "containerfile":
		return "dockerfile"
	case "makefile", "gnumakefile":
		return "makefile"
	}

	return ""
}

// LexLine tokenizes a single line of source code into syntax tokens.
func LexLine(line, lang string) []Token {
	if len(line) == 0 {
		return nil
	}

	lang = strings.ToLower(lang)
	switch lang {
	case "go":
		return lexCStyle(line, goKeywords, goTypes, true)
	case "javascript", "typescript":
		return lexCStyle(line, jsTsKeywords, jsTsTypes, true)
	case "rust":
		return lexCStyle(line, rustKeywords, nil, false)
	case "c", "cpp":
		return lexCStyle(line, cCppKeywords, nil, false)
	case "python":
		return lexPython(line)
	case "shell":
		return lexShell(line)
	case "sql":
		return lexSQL(line)
	case "json", "yaml", "toml":
		return lexConfig(line)
	default:
		return lexGeneric(line)
	}
}

func lexCStyle(line string, keywords, types map[string]bool, hasBacktickStrings bool) []Token {
	var tokens []Token
	runes := []rune(line)
	n := len(runes)
	i := 0

	for i < n {
		r := runes[i]

		// Whitespace
		if unicode.IsSpace(r) {
			start := i
			for i < n && unicode.IsSpace(runes[i]) {
				i++
			}
			tokens = append(tokens, Token{Type: TokenPlain, Value: string(runes[start:i])})
			continue
		}

		// Comments: // or /*
		if r == '/' && i+1 < n && (runes[i+1] == '/' || runes[i+1] == '*') {
			tokens = append(tokens, Token{Type: TokenComment, Value: string(runes[i:])})
			break
		}

		// Strings: "..." or '...' or `...`
		if r == '"' || r == '\'' || (hasBacktickStrings && r == '`') {
			quote := r
			start := i
			i++
			escaped := false
			for i < n {
				curr := runes[i]
				if quote != '`' && curr == '\\' && !escaped {
					escaped = true
					i++
					continue
				}
				if curr == quote && !escaped {
					i++
					break
				}
				escaped = false
				i++
			}
			tokens = append(tokens, Token{Type: TokenString, Value: string(runes[start:i])})
			continue
		}

		// Numbers: digit or .digit
		if unicode.IsDigit(r) || (r == '.' && i+1 < n && unicode.IsDigit(runes[i+1])) {
			start := i
			// Check for hex 0x...
			if r == '0' && i+1 < n && (runes[i+1] == 'x' || runes[i+1] == 'X') {
				i += 2
				for i < n && (unicode.IsDigit(runes[i]) || (runes[i] >= 'a' && runes[i] <= 'f') || (runes[i] >= 'A' && runes[i] <= 'F') || runes[i] == '_') {
					i++
				}
			} else {
				for i < n && (unicode.IsDigit(runes[i]) || runes[i] == '.' || runes[i] == '_' || runes[i] == 'e' || runes[i] == 'E') {
					i++
				}
			}
			tokens = append(tokens, Token{Type: TokenNumber, Value: string(runes[start:i])})
			continue
		}

		// Identifiers and Keywords
		if isIdentStart(r) {
			start := i
			for i < n && isIdentPart(runes[i]) {
				i++
			}
			val := string(runes[start:i])

			// Lookahead to see if followed by ( => function call/definition
			isFunc := false
			j := i
			for j < n && unicode.IsSpace(runes[j]) {
				j++
			}
			if j < n && runes[j] == '(' {
				isFunc = true
			}

			tokType := TokenPlain
			if keywords != nil && keywords[val] {
				tokType = TokenKeyword
			} else if types != nil && types[val] {
				tokType = TokenTypeIdent
			} else if isFunc {
				tokType = TokenFunction
			} else if unicode.IsUpper(runes[start]) && (types != nil || langLooksLikeType(val)) {
				// Capitalized identifiers in Go/TS are often types or exported structs
				tokType = TokenTypeIdent
			}
			tokens = append(tokens, Token{Type: tokType, Value: val})
			continue
		}

		// Operators & punctuation
		if isOperatorRune(r) {
			start := i
			for i < n && isOperatorRune(runes[i]) {
				i++
			}
			tokens = append(tokens, Token{Type: TokenOperator, Value: string(runes[start:i])})
			continue
		}

		// Single punctuation / other
		tokens = append(tokens, Token{Type: TokenPlain, Value: string(r)})
		i++
	}

	return tokens
}

func lexPython(line string) []Token {
	var tokens []Token
	runes := []rune(line)
	n := len(runes)
	i := 0

	for i < n {
		r := runes[i]

		// Whitespace
		if unicode.IsSpace(r) {
			start := i
			for i < n && unicode.IsSpace(runes[i]) {
				i++
			}
			tokens = append(tokens, Token{Type: TokenPlain, Value: string(runes[start:i])})
			continue
		}

		// Comment: #
		if r == '#' {
			tokens = append(tokens, Token{Type: TokenComment, Value: string(runes[i:])})
			break
		}

		// Strings: "..." or '...' or triple quotes
		if r == '"' || r == '\'' {
			quote := r
			start := i
			// Check for triple quotes
			isTriple := i+2 < n && runes[i+1] == quote && runes[i+2] == quote
			if isTriple {
				i += 3
				for i < n {
					if i+2 < n && runes[i] == quote && runes[i+1] == quote && runes[i+2] == quote {
						i += 3
						break
					}
					i++
				}
			} else {
				i++
				escaped := false
				for i < n {
					curr := runes[i]
					if curr == '\\' && !escaped {
						escaped = true
						i++
						continue
					}
					if curr == quote && !escaped {
						i++
						break
					}
					escaped = false
					i++
				}
			}
			tokens = append(tokens, Token{Type: TokenString, Value: string(runes[start:i])})
			continue
		}

		// Numbers
		if unicode.IsDigit(r) {
			start := i
			for i < n && (unicode.IsDigit(runes[i]) || runes[i] == '.' || runes[i] == '_' || runes[i] == 'e' || runes[i] == 'E' || runes[i] == 'x' || runes[i] == 'o' || runes[i] == 'b') {
				i++
			}
			tokens = append(tokens, Token{Type: TokenNumber, Value: string(runes[start:i])})
			continue
		}

		// Identifiers
		if isIdentStart(r) {
			start := i
			for i < n && isIdentPart(runes[i]) {
				i++
			}
			val := string(runes[start:i])

			isFunc := false
			j := i
			for j < n && unicode.IsSpace(runes[j]) {
				j++
			}
			if j < n && runes[j] == '(' {
				isFunc = true
			}

			tokType := TokenPlain
			if pyKeywords[val] {
				tokType = TokenKeyword
			} else if pyTypes[val] {
				tokType = TokenTypeIdent
			} else if isFunc {
				tokType = TokenFunction
			}
			tokens = append(tokens, Token{Type: tokType, Value: val})
			continue
		}

		if isOperatorRune(r) {
			start := i
			for i < n && isOperatorRune(runes[i]) {
				i++
			}
			tokens = append(tokens, Token{Type: TokenOperator, Value: string(runes[start:i])})
			continue
		}

		tokens = append(tokens, Token{Type: TokenPlain, Value: string(r)})
		i++
	}

	return tokens
}

func lexShell(line string) []Token {
	var tokens []Token
	runes := []rune(line)
	n := len(runes)
	i := 0

	for i < n {
		r := runes[i]

		if unicode.IsSpace(r) {
			start := i
			for i < n && unicode.IsSpace(runes[i]) {
				i++
			}
			tokens = append(tokens, Token{Type: TokenPlain, Value: string(runes[start:i])})
			continue
		}

		// Comment: #
		if r == '#' {
			tokens = append(tokens, Token{Type: TokenComment, Value: string(runes[i:])})
			break
		}

		// Strings
		if r == '"' || r == '\'' {
			quote := r
			start := i
			i++
			for i < n && runes[i] != quote {
				if runes[i] == '\\' && i+1 < n {
					i++
				}
				i++
			}
			if i < n {
				i++
			}
			tokens = append(tokens, Token{Type: TokenString, Value: string(runes[start:i])})
			continue
		}

		// Words / Identifiers
		if isIdentStart(r) || r == '-' || r == '$' {
			start := i
			for i < n && !unicode.IsSpace(runes[i]) && runes[i] != '#' && runes[i] != ';' && runes[i] != '|' && runes[i] != '&' && runes[i] != '"' && runes[i] != '\'' {
				i++
			}
			val := string(runes[start:i])
			tokType := TokenPlain
			if shKeywords[val] {
				tokType = TokenKeyword
			} else if strings.HasPrefix(val, "$") {
				tokType = TokenTypeIdent
			} else if strings.HasPrefix(val, "-") {
				tokType = TokenOperator
			}
			tokens = append(tokens, Token{Type: tokType, Value: val})
			continue
		}

		tokens = append(tokens, Token{Type: TokenPlain, Value: string(r)})
		i++
	}

	return tokens
}

func lexSQL(line string) []Token {
	var tokens []Token
	runes := []rune(line)
	n := len(runes)
	i := 0

	for i < n {
		r := runes[i]

		if unicode.IsSpace(r) {
			start := i
			for i < n && unicode.IsSpace(runes[i]) {
				i++
			}
			tokens = append(tokens, Token{Type: TokenPlain, Value: string(runes[start:i])})
			continue
		}

		// Comments: -- or /*
		if (r == '-' && i+1 < n && runes[i+1] == '-') || (r == '/' && i+1 < n && runes[i+1] == '*') {
			tokens = append(tokens, Token{Type: TokenComment, Value: string(runes[i:])})
			break
		}

		// Strings: '...'
		if r == '\'' || r == '"' {
			quote := r
			start := i
			i++
			for i < n && runes[i] != quote {
				i++
			}
			if i < n {
				i++
			}
			tokens = append(tokens, Token{Type: TokenString, Value: string(runes[start:i])})
			continue
		}

		if unicode.IsDigit(r) {
			start := i
			for i < n && (unicode.IsDigit(runes[i]) || runes[i] == '.') {
				i++
			}
			tokens = append(tokens, Token{Type: TokenNumber, Value: string(runes[start:i])})
			continue
		}

		if isIdentStart(r) {
			start := i
			for i < n && isIdentPart(runes[i]) {
				i++
			}
			val := string(runes[start:i])
			tokType := TokenPlain
			if sqlKeywords[strings.ToUpper(val)] {
				tokType = TokenKeyword
			}
			tokens = append(tokens, Token{Type: tokType, Value: val})
			continue
		}

		tokens = append(tokens, Token{Type: TokenPlain, Value: string(r)})
		i++
	}

	return tokens
}

var jsonKeyRegexp = regexp.MustCompile(`^"([^"\\]|\\.)*"\s*:`)

func lexConfig(line string) []Token {
	var tokens []Token
	runes := []rune(line)
	n := len(runes)
	i := 0

	for i < n {
		r := runes[i]

		if unicode.IsSpace(r) {
			start := i
			for i < n && unicode.IsSpace(runes[i]) {
				i++
			}
			tokens = append(tokens, Token{Type: TokenPlain, Value: string(runes[start:i])})
			continue
		}

		// Comments: # or //
		if r == '#' || (r == '/' && i+1 < n && runes[i+1] == '/') {
			tokens = append(tokens, Token{Type: TokenComment, Value: string(runes[i:])})
			break
		}

		// Strings: "..." or '...'
		if r == '"' || r == '\'' {
			quote := r
			start := i
			i++
			escaped := false
			for i < n {
				curr := runes[i]
				if curr == '\\' && !escaped {
					escaped = true
					i++
					continue
				}
				if curr == quote && !escaped {
					i++
					break
				}
				escaped = false
				i++
			}

			// In JSON/YAML, if followed by :, it's a key/identifier
			j := i
			for j < n && unicode.IsSpace(runes[j]) {
				j++
			}
			tokType := TokenString
			if j < n && runes[j] == ':' {
				tokType = TokenTypeIdent
			}
			tokens = append(tokens, Token{Type: tokType, Value: string(runes[start:i])})
			continue
		}

		// Numbers, booleans, null
		if unicode.IsDigit(r) || r == '-' || r == '+' {
			start := i
			i++
			for i < n && (unicode.IsDigit(runes[i]) || runes[i] == '.' || runes[i] == 'e' || runes[i] == 'E') {
				i++
			}
			tokens = append(tokens, Token{Type: TokenNumber, Value: string(runes[start:i])})
			continue
		}

		if isIdentStart(r) {
			start := i
			for i < n && isIdentPart(runes[i]) {
				i++
			}
			val := string(runes[start:i])
			tokType := TokenPlain
			if val == "true" || val == "false" || val == "null" {
				tokType = TokenKeyword
			}
			tokens = append(tokens, Token{Type: tokType, Value: val})
			continue
		}

		tokens = append(tokens, Token{Type: TokenPlain, Value: string(r)})
		i++
	}

	return tokens
}

func lexGeneric(line string) []Token {
	var tokens []Token
	runes := []rune(line)
	n := len(runes)
	i := 0

	for i < n {
		r := runes[i]

		if unicode.IsSpace(r) {
			start := i
			for i < n && unicode.IsSpace(runes[i]) {
				i++
			}
			tokens = append(tokens, Token{Type: TokenPlain, Value: string(runes[start:i])})
			continue
		}

		// Comments
		if (r == '/' && i+1 < n && (runes[i+1] == '/' || runes[i+1] == '*')) || r == '#' {
			tokens = append(tokens, Token{Type: TokenComment, Value: string(runes[i:])})
			break
		}

		// Strings
		if r == '"' || r == '\'' || r == '`' {
			quote := r
			start := i
			i++
			for i < n && runes[i] != quote {
				if runes[i] == '\\' && i+1 < n {
					i++
				}
				i++
			}
			if i < n {
				i++
			}
			tokens = append(tokens, Token{Type: TokenString, Value: string(runes[start:i])})
			continue
		}

		// Numbers
		if unicode.IsDigit(r) {
			start := i
			for i < n && (unicode.IsDigit(runes[i]) || runes[i] == '.') {
				i++
			}
			tokens = append(tokens, Token{Type: TokenNumber, Value: string(runes[start:i])})
			continue
		}

		// Identifiers
		if isIdentStart(r) {
			start := i
			for i < n && isIdentPart(runes[i]) {
				i++
			}
			tokens = append(tokens, Token{Type: TokenPlain, Value: string(runes[start:i])})
			continue
		}

		tokens = append(tokens, Token{Type: TokenPlain, Value: string(r)})
		i++
	}

	return tokens
}

func isIdentStart(r rune) bool {
	return unicode.IsLetter(r) || r == '_'
}

func isIdentPart(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

func isOperatorRune(r rune) bool {
	switch r {
	case '+', '-', '*', '/', '%', '=', '!', '<', '>', '&', '|', '^', '~', ':', '?', ';', ',', '.':
		return true
	default:
		return false
	}
}

func langLooksLikeType(val string) bool {
	// Common convention in Go/TS/Rust/C++: PascalCase identifiers are types
	if len(val) < 2 {
		return false
	}
	r := []rune(val)
	return unicode.IsUpper(r[0]) && !unicode.IsUpper(r[1])
}

// TokenColorCode returns the ANSI sequence corresponding to the token type in the active theme.
func TokenColorCode(tokType TokenType) string {
	switch tokType {
	case TokenKeyword:
		return themeSeqs[rolePurple]
	case TokenTypeIdent:
		return themeSeqs[roleAccent]
	case TokenFunction:
		return themeSeqs[roleAccent2]
	case TokenString:
		return themeSeqs[roleWarn]
	case TokenNumber:
		return themeSeqs[roleAccent]
	case TokenComment:
		return themeSeqs[roleMuted]
	case TokenOperator:
		return themeSeqs[roleText]
	default:
		return themeSeqs[roleText]
	}
}

// HighlightCodeLine applies syntax highlighting to a single line of code in the given language.
func HighlightCodeLine(line, lang string) string {
	return HighlightCodeLineBg(line, lang, "")
}

// HighlightCodeLineBg highlights line like HighlightCodeLine, restoring the
// background sequence bg after each token's reset so a tinted line (a diff
// addition or deletion) keeps its tint across the whole line.
func HighlightCodeLineBg(line, lang, bg string) string {
	if !ColorsEnabled() || line == "" {
		return line
	}
	return HighlightTokens(LexLine(line, lang), bg)
}

// HighlightTokens renders tokens with the active theme's colours. bg, when
// non-empty, is re-emitted after every reset; see HighlightCodeLineBg.
func HighlightTokens(tokens []Token, bg string) string {
	if !ColorsEnabled() {
		var sb strings.Builder
		for _, tok := range tokens {
			sb.WriteString(tok.Value)
		}
		return sb.String()
	}
	var sb strings.Builder
	for _, tok := range tokens {
		color := TokenColorCode(tok.Type)
		if color != "" && tok.Type != TokenPlain {
			sb.WriteString(color)
			sb.WriteString(tok.Value)
			sb.WriteString(ansiReset)
			sb.WriteString(bg)
		} else {
			sb.WriteString(tok.Value)
		}
	}
	return sb.String()
}
