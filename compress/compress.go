// Package parser provides a pure-Go structural source-code parser that
// extracts symbol boundaries from Go, Java, TypeScript, JavaScript, Python,
// C#, C++, Rust, and other languages without any CGO or WebAssembly dependencies.
//
// Compression tiers
//   - Tier 2: Method/function body folding — replaces inner blocks with a
//     canonical comment, preserving signatures and declarations verbatim.
//   - Tier 3: Comment pruning — strips all // /* */ # and triple-quoted
//     docstrings while protecting folded-body markers from removal.
//
// Primary entry point
//
//	Compressed, err := parser.Compress(path, tier)
//
// Internal helpers (FoldBodies, PruneComments, ParseFile) remain exported
// for callers that need fine-grained control.
package compress

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ─── Language Core ───────────────────────────────────────────────────────────

// Language identifies the source language strategy.
type Language int

const (
	// LangUnknown is used for unmapped types (which default to C-Style fallback).
	LangUnknown Language = iota
	// LangGo covers Go files with explicit parsing.
	LangGo
	// LangCStyle covers Java, TS, JS, C++, C#, Rust, and all unknown fallbacks.
	LangCStyle
	// LangPython covers Python with indentation-based parsing.
	LangPython
)

// DetectLanguage resolves the file extension dynamically.
func DetectLanguage(path string) Language {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return LangGo
	case ".py", ".pyw":
		return LangPython
	case ".java", ".ts", ".tsx", ".js", ".jsx", ".cpp", ".hpp", ".cc", ".c", ".h", ".cs", ".rs", ".vue", ".svelte", ".astro", ".proto":
		return LangCStyle
	default:
		// Default fallback strategy for all other files
		return LangCStyle
	}
}

// String implements fmt.Stringer.
func (l Language) String() string {
	switch l {
	case LangGo:
		return "go"
	case LangCStyle:
		return "c-style"
	case LangPython:
		return "python"
	default:
		return "unknown"
	}
}

// Symbol holds parsed structural metadata.
type Symbol struct {
	Name      string
	Kind      string
	StartLine int
	EndLine   int
	Signature string
}

// ─── Regex Patterns ──────────────────────────────────────────────────────────

// goFuncRe matches Go function and method declarations.
var goFuncRe = regexp.MustCompile(
	`^func\s+(?:\([^)]*\)\s+)?(?P<name>[A-Za-z_][A-Za-z0-9_]*)\s*\(`,
)

// goTypeRe matches Go type declarations.
var goTypeRe = regexp.MustCompile(
	`^type\s+(?P<name>[A-Za-z_][A-Za-z0-9_]*)\s+(?P<kind>struct|interface|\S)`,
)

// goVarRe matches Go var/const declarations.
var goVarRe = regexp.MustCompile(
	`^(?P<kind>var|const)\s+(?P<name>[A-Za-z_][A-Za-z0-9_]*)`,
)

// cStyleClassRe matches Java/C++/C#/TS class, interface, struct, and enum declarations.
var cStyleClassRe = regexp.MustCompile(
	`\b(?P<kind>class|interface|struct|enum|record|namespace)\s+(?P<name>[A-Za-z_][A-Za-z0-9_]*)`,
)

// cStyleMethodRe matches Java/C++/C#/TS methods, functions, and constructors.
var cStyleMethodRe = regexp.MustCompile(
	`^(?:public|protected|private|static|final|synchronized|abstract|default|async|export|\s)*\s*(?:[A-Za-z0-9_<>\[\]]+\s+)?(?P<name>[A-Za-z_][A-Za-z0-9_]*)\s*\(`,
)

// cStyleArrowFnRe matches JS/TS arrow functions, hooks, and callbacks.
var cStyleArrowFnRe = regexp.MustCompile(
	`\b(?P<name>[A-Za-z_][A-Za-z0-9_]*)\s*=\s*(?:useCallback|useMemo|useEvent|async|\s)*\s*(?:<[^>]+>)?\s*\([^)]*\)\s*(?::\s*[^{=]+)?\s*=>`,
)

// pySymbolRe matches Python class and def statements.
var pySymbolRe = regexp.MustCompile(
	`^\s*(?P<kind>def|class)\s+(?P<name>[A-Za-z_][A-Za-z0-9_]*)`,
)

// rustFnRe matches Rust function and method declarations.
//
//	pub fn foo(x: i32) -> String {
//	async fn handle(req: Request) -> Response {
var rustFnRe = regexp.MustCompile(
	`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:async\s+|const\s+|unsafe\s+|extern\s+)*(?P<kind>fn)\s+(?P<name>[A-Za-z_][A-Za-z0-9_]*)`,
)

// ─── ParseFile Core API ──────────────────────────────────────────────────────

// ParseFile parses raw code content and extracts top-level symbols.
func ParseFile(path string, src []byte) ([]Symbol, error) {
	src = PreprocessPolyglot(path, src)
	src = bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n"))
	if strings.ToLower(filepath.Ext(path)) == ".proto" {
		lines, err := splitLines(src)
		if err != nil {
			return nil, err
		}
		return parseProto(lines)
	}
	lang := DetectLanguage(path)
	lines, err := splitLines(src)
	if err != nil {
		return nil, err
	}

	switch lang {
	case LangGo:
		return parseGo(lines)
	case LangPython:
		return parsePython(lines)
	case LangCStyle:
		return parseCStyle(lines)
	default:
		return parseCStyle(lines)
	}
}

// ─── FoldBodies (Tier 2 Compression) ─────────────────────────────────────────

// FoldBodies replaces structural function/method bodies with:
//
//	// [body folded: N lines] (or # [body folded: N lines])
//
// while keeping declarations, signatures, and boundaries intact.
//
// Returns (folded bytes, nil) on success.
func FoldBodies(path string, src []byte) ([]byte, error) {
	src = PreprocessPolyglot(path, src)
	src = bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n"))
	if IsMinified(src) || HasMergeConflictMarkers(src) {
		return src, nil
	}
	if strings.ToLower(filepath.Ext(path)) == ".proto" {
		lines, err := splitLines(src)
		if err != nil {
			return nil, err
		}
		out := foldProto(lines)
		return []byte(strings.Join(out, "\n")), nil
	}
	lang := DetectLanguage(path)
	lines, err := splitLines(src)
	if err != nil {
		return nil, err
	}
	var out []string

	switch lang {
	case LangGo:
		out = foldGo(lines)
	case LangPython:
		out = foldPython(lines)
	case LangCStyle:
		out = foldCStyle(lines)
	default:
		out = foldCStyle(lines)
	}

	return []byte(strings.Join(out, "\n")), nil
}

// ─── PruneComments (Tier 3 Compression) ──────────────────────────────────────

// PruneComments strips all C-style (//, /* */) and Python-style (#, triple-quotes) comments.
// It tracks string literals to prevent comment tokens inside strings from being stripped.
func PruneComments(path string, src []byte) ([]byte, error) {
	src = PreprocessPolyglot(path, src)
	src = bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n"))
	if IsMinified(src) || HasMergeConflictMarkers(src) {
		return src, nil
	}
	lang := DetectLanguage(path)
	lines, err := splitLines(src)
	if err != nil {
		return nil, err
	}
	var out []string

	inMultiComment := false
	inTripleQuoteDouble := false
	inTripleQuoteSingle := false

	for _, line := range lines {
		// If we are currently inside a multi-line C comment:
		if inMultiComment {
			if idx := strings.Index(line, "*/"); idx >= 0 {
				inMultiComment = false
				trailing := line[idx+2:]
				if strings.TrimSpace(trailing) != "" {
					out = append(out, trailing)
				}
			}
			continue
		}

		// If we are inside Python triple double-quotes:
		if inTripleQuoteDouble {
			if idx := strings.Index(line, `"""`); idx >= 0 {
				inTripleQuoteDouble = false
				trailing := line[idx+3:]
				if strings.TrimSpace(trailing) != "" {
					out = append(out, trailing)
				}
			}
			continue
		}

		// If we are inside Python triple single-quotes:
		if inTripleQuoteSingle {
			if idx := strings.Index(line, `'''`); idx >= 0 {
				inTripleQuoteSingle = false
				trailing := line[idx+3:]
				if strings.TrimSpace(trailing) != "" {
					out = append(out, trailing)
				}
			}
			continue
		}

		cleanLine, openedBlock, openedTripleDouble, openedTripleSingle := stripCommentsFromLine(line, lang)
		if openedBlock {
			inMultiComment = true
		} else if openedTripleDouble {
			inTripleQuoteDouble = true
		} else if openedTripleSingle {
			inTripleQuoteSingle = true
		}

		if strings.TrimSpace(cleanLine) != "" {
			out = append(out, cleanLine)
		}
	}

	return []byte(strings.Join(out, "\n")), nil
}

// IsMinified reports if the content has lines < 10 but bytes > 50KB.
func IsMinified(src []byte) bool {
	lineCount := bytes.Count(src, []byte("\n")) + 1
	return lineCount < 10 && len(src) > 50*1024
}

// HasMergeConflictMarkers reports if the content contains Git merge conflict markers.
func HasMergeConflictMarkers(src []byte) bool {
	return bytes.Contains(src, []byte("<<<<<<<")) &&
		bytes.Contains(src, []byte("=======")) &&
		bytes.Contains(src, []byte(">>>>>>>"))
}

// IsGenerated reports if the file content contains common auto-generated file headers.
func IsGenerated(src []byte) bool {
	lower := bytes.ToLower(src)
	limit := 2048
	if len(lower) < limit {
		limit = len(lower)
	}
	header := lower[:limit]
	markers := [][]byte{
		[]byte("code generated"),
		[]byte("do not edit"),
		[]byte("generated by"),
		[]byte("auto-generated"),
		[]byte("this file is generated"),
		[]byte("this file was generated"),
		[]byte("automatically generated"),
	}
	for _, m := range markers {
		if bytes.Contains(header, m) {
			return true
		}
	}
	return false
}

var scriptTagRe = regexp.MustCompile(`(?is)<script\b[^>]*>(.*?)</script>`)

func preprocessVueSvelte(src []byte) []byte {
	out := make([]byte, len(src))
	for i, b := range src {
		if b == '\n' || b == '\r' {
			out[i] = b
		} else {
			out[i] = ' '
		}
	}
	matches := scriptTagRe.FindAllSubmatchIndex(src, -1)
	for _, m := range matches {
		if len(m) >= 4 && m[2] >= 0 && m[3] >= 0 {
			copy(out[m[2]:m[3]], src[m[2]:m[3]])
		}
	}
	return out
}

func preprocessAstro(src []byte) []byte {
	out := make([]byte, len(src))
	for i, b := range src {
		if b == '\n' || b == '\r' {
			out[i] = b
		} else {
			out[i] = ' '
		}
	}
	str := string(src)
	firstIdx := strings.Index(str, "---")
	if firstIdx == -1 {
		return out
	}
	rest := str[firstIdx+3:]
	secondIdx := strings.Index(rest, "---")
	if secondIdx == -1 {
		return out
	}
	actualSecondIdx := firstIdx + 3 + secondIdx
	start := firstIdx + 3
	end := actualSecondIdx
	if start >= 0 && end <= len(src) && start < end {
		copy(out[start:end], src[start:end])
	}
	return out
}

func PreprocessPolyglot(path string, src []byte) []byte {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".vue", ".svelte":
		if !bytes.Contains(bytes.ToLower(src), []byte("<script")) {
			return src
		}
		return preprocessVueSvelte(src)
	case ".astro":
		trimmed := bytes.TrimSpace(src)
		if !bytes.HasPrefix(trimmed, []byte("---")) {
			return src
		}
		return preprocessAstro(src)
	default:
		return src
	}
}

func parseProto(lines []string) ([]Symbol, error) {
	var symbols []Symbol
	depth := 0

	protoBlockRe := regexp.MustCompile(`^\s*(?P<kind>message|service|enum)\s+(?P<name>[A-Za-z_][A-Za-z0-9_]*)`)
	protoRpcRe := regexp.MustCompile(`^\s*rpc\s+(?P<name>[A-Za-z_][A-Za-z0-9_]*)\s*\((?P<param>[^)]+)\)\s+returns\s*\((?P<ret>[^)]+)\)`)

	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		lineNum := i + 1

		if m := protoBlockRe.FindStringSubmatch(line); m != nil {
			kind := namedGroup(protoBlockRe, m, "kind")
			name := namedGroup(protoBlockRe, m, "name")
			end := findBlockEndCStyle(lines, i)
			symbols = append(symbols, Symbol{
				Name:      name,
				Kind:      kind,
				StartLine: lineNum,
				EndLine:   end,
				Signature: line,
			})
		} else if m := protoRpcRe.FindStringSubmatch(line); m != nil {
			name := namedGroup(protoRpcRe, m, "name")
			symbols = append(symbols, Symbol{
				Name:      name,
				Kind:      "rpc",
				StartLine: lineNum,
				EndLine:   lineNum,
				Signature: line,
			})
		}

		depth = updateCStyleDepth(line, depth)
	}
	return symbols, nil
}


// ─── Compress — Primary Public API ───────────────────────────────────────────

// Compress is the primary entry point for consumers such as the benchmark CLI.
// It reads src and applies the requested compression tier:
//
//	tier <= 1  → returns src unchanged (raw)
//	tier == 2  → FoldBodies only (keeps signatures, strips inner logic)
//	tier >= 3  → FoldBodies then PruneComments (maximum token reduction)
//
// For file extensions the parser does not structurally support, Compress still
// applies the C-style brace folding fallback; it never returns an error solely
// because of an unknown extension.
//
// Returns (compressed bytes, nil) on success.
func Compress(path string, src []byte, tier int) ([]byte, error) {
	limit := 512
	if len(src) < limit {
		limit = len(src)
	}
	if isBinary(src[:limit]) {
		log.Printf("DEBUG: skipping binary file: %s", path)
		return src, nil
	}

	src = PreprocessPolyglot(path, src)
	src = bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n"))
	if IsMinified(src) || HasMergeConflictMarkers(src) {
		return src, nil
	}
	if tier <= 1 || !IsStructurallySupported(path) {
		return src, nil
	}

	folded, err := FoldBodies(path, src)
	if err != nil {
		return nil, fmt.Errorf("compress tier2 %q: %w", path, err)
	}

	if tier == 2 {
		return folded, nil
	}

	pruned, err := PruneComments(path, folded)
	if err != nil {
		return nil, fmt.Errorf("compress tier3 %q: %w", path, err)
	}
	return pruned, nil
}

// IsStructurallySupported reports whether the parser has an explicit structural
// folding strategy for the given file extension.  Files that return false will
// still be processed by the C-style fallback brace scanner; callers may want to
// label their results as "[estimated]" when this returns false.
func IsStructurallySupported(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".py", ".pyw",
		".java", ".ts", ".tsx", ".js", ".jsx",
		".cpp", ".hpp", ".cc", ".c", ".h", ".cs", ".rs",
		".vue", ".svelte", ".astro", ".proto":
		return true
	}
	return false
}

// ─── Go Parsing Strategy ─────────────────────────────────────────────────────

func parseGo(lines []string) ([]Symbol, error) {
	var symbols []Symbol
	depth := 0

	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		lineNum := i + 1

		if depth > 0 {
			for _, ch := range line {
				switch ch {
				case '{':
					depth++
				case '}':
					depth--
				}
			}
			continue
		}

		if m := goFuncRe.FindStringSubmatch(line); m != nil {
			name := namedGroup(goFuncRe, m, "name")
			kind := "function"
			if strings.Contains(line, "(") && strings.Count(line, "(") > 1 {
				kind = "method"
			}
			if strings.HasPrefix(line, "func (") || strings.HasPrefix(line, "func(") {
				kind = "method"
			}

			sig := signatureFromLine(line)
			end := findBlockEndCStyle(lines, i)

			symbols = append(symbols, Symbol{
				Name:      name,
				Kind:      kind,
				StartLine: lineNum,
				EndLine:   end,
				Signature: sig,
			})

			if strings.HasSuffix(strings.TrimSpace(line), "{") {
				depth++
			}
			continue
		}

		if m := goTypeRe.FindStringSubmatch(line); m != nil {
			name := namedGroup(goTypeRe, m, "name")
			kindStr := namedGroup(goTypeRe, m, "kind")

			kind := "type"
			switch kindStr {
			case "struct":
				kind = "class"
			case "interface":
				kind = "interface"
			}

			sig := strings.TrimSuffix(strings.TrimSpace(line), "{")
			end := findBlockEndCStyle(lines, i)

			symbols = append(symbols, Symbol{
				Name:      name,
				Kind:      kind,
				StartLine: lineNum,
				EndLine:   end,
				Signature: strings.TrimSpace(sig),
			})

			if strings.HasSuffix(strings.TrimSpace(line), "{") {
				depth++
			}
			continue
		}

		if m := goVarRe.FindStringSubmatch(line); m != nil {
			name := namedGroup(goVarRe, m, "name")
			kindStr := namedGroup(goVarRe, m, "kind")

			kind := "variable"
			if kindStr == "const" {
				kind = "constant"
			}

			symbols = append(symbols, Symbol{
				Name:      name,
				Kind:      kind,
				StartLine: lineNum,
				EndLine:   lineNum,
				Signature: strings.TrimSpace(line),
			})

			if strings.HasSuffix(strings.TrimSpace(line), "{") {
				depth++
			}
			continue
		}

		for _, ch := range line {
			switch ch {
			case '{':
				depth++
			case '}':
				depth--
			}
		}
	}

	return symbols, nil
}

func foldGo(lines []string) []string {
	var out []string
	i := 0
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if !strings.HasSuffix(trimmed, "{") || !strings.Contains(trimmed, "func") {
			out = append(out, line)
			i++
			continue
		}

		isSig := goFuncRe.MatchString(trimmed)
		if isSig {
			out = append(out, line)
			i++

			depth := 1
			bodyStart := i
			for i < len(lines) && depth > 0 {
				for _, ch := range lines[i] {
					switch ch {
					case '{':
						depth++
					case '}':
						depth--
					}
				}
				i++
			}

			bodyLines := i - bodyStart - 1
			if bodyLines > 0 {
				indent := leadingWhitespace(line) + "\t"
				out = append(out, fmt.Sprintf("%s// [body folded: %d lines]", indent, bodyLines))
			}

			if i > 0 && i <= len(lines) {
				out = append(out, lines[i-1])
			}
			continue
		}

		out = append(out, line)
		i++
	}
	return out
}

// ─── Python Indentation-Based Strategy ───────────────────────────────────────

func parsePython(lines []string) ([]Symbol, error) {
	var symbols []Symbol
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		if m := pySymbolRe.FindStringSubmatch(line); m != nil {
			kindStr := namedGroup(pySymbolRe, m, "kind")
			name := namedGroup(pySymbolRe, m, "name")

			kind := "function"
			if kindStr == "class" {
				kind = "class"
			}

			startLine := i + 1
			sigIndent := countIndentation(line)

			// Find block boundary by scanning subsequent lines with deeper indentation.
			endLine := startLine
			for j := i + 1; j < len(lines); j++ {
				nextLine := lines[j]
				nextTrimmed := strings.TrimSpace(nextLine)
				if nextTrimmed == "" || strings.HasPrefix(nextTrimmed, "#") {
					continue
				}
				nextIndent := countIndentation(nextLine)
				if nextIndent <= sigIndent {
					break
				}
				endLine = j + 1
			}

			sig := capturePythonSignature(lines, i)
			symbols = append(symbols, Symbol{
				Name:      name,
				Kind:      kind,
				StartLine: startLine,
				EndLine:   endLine,
				Signature: sig,
			})
		}
	}
	return symbols, nil
}

func foldPython(lines []string) []string {
	var out []string
	i := 0
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "def ") || strings.HasPrefix(trimmed, "class ") {
			isClass := strings.HasPrefix(trimmed, "class ")
			out = append(out, line)
			sigIndent := countIndentation(line)
			i++

			var bodyLines []string
			for i < len(lines) {
				nextLine := lines[i]
				nextTrimmed := strings.TrimSpace(nextLine)
				if nextTrimmed != "" {
					nextIndent := countIndentation(nextLine)
					if nextIndent <= sigIndent {
						break
					}
				}
				bodyLines = append(bodyLines, nextLine)
				i++
			}

			nonEmptyCount := 0
			for _, l := range bodyLines {
				if strings.TrimSpace(l) != "" && !strings.HasPrefix(strings.TrimSpace(l), "#") {
					nonEmptyCount++
				}
			}

			if nonEmptyCount > 0 {
				indent := strings.Repeat(" ", sigIndent) + "    "
				if isClass {
					out = append(out, fmt.Sprintf("%spass  # [class body folded: %d lines]", indent, len(bodyLines)))
				} else {
					out = append(out, fmt.Sprintf("%spass  # [body folded: %d lines]", indent, len(bodyLines)))
				}
			}
			continue
		}

		out = append(out, line)
		i++
	}
	return out
}

// ─── Universal C-Style Strategy ──────────────────────────────────────────────

func parseCStyle(lines []string) ([]Symbol, error) {
	var symbols []Symbol
	depth := 0

	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		lineNum := i + 1

		if depth == 0 {
			if m := cStyleClassRe.FindStringSubmatch(line); m != nil {
				name := namedGroup(cStyleClassRe, m, "name")
				kind := namedGroup(cStyleClassRe, m, "kind")

				sig := captureCStyleSignature(lines, i)
				end := findBlockEndCStyle(lines, i)

				symbols = append(symbols, Symbol{
					Name:      name,
					Kind:      kind,
					StartLine: lineNum,
					EndLine:   end,
					Signature: sig,
				})
			} else if m := cStyleMethodRe.FindStringSubmatch(line); m != nil {
				name := namedGroup(cStyleMethodRe, m, "name")
				if !isCStyleKeyword(name) && !strings.Contains(line, ";") && !strings.Contains(line, "}") {
					sig := captureCStyleSignature(lines, i)
					end := findBlockEndCStyle(lines, i)

					symbols = append(symbols, Symbol{
						Name:      name,
						Kind:      "function",
						StartLine: lineNum,
						EndLine:   end,
						Signature: sig,
					})
				}
			} else if m := cStyleArrowFnRe.FindStringSubmatch(line); m != nil {
				name := namedGroup(cStyleArrowFnRe, m, "name")
				if !isCStyleKeyword(name) && !strings.Contains(line, ";") && !strings.Contains(line, "}") {
					sig := captureCStyleSignature(lines, i)
					end := findBlockEndCStyle(lines, i)

					symbols = append(symbols, Symbol{
						Name:      name,
						Kind:      "function",
						StartLine: lineNum,
						EndLine:   end,
						Signature: sig,
					})
				}
			}
		} else if depth == 1 {
			if m := cStyleMethodRe.FindStringSubmatch(line); m != nil {
				name := namedGroup(cStyleMethodRe, m, "name")
				if !isCStyleKeyword(name) && !strings.Contains(line, ";") && !strings.Contains(line, "}") {
					sig := captureCStyleSignature(lines, i)
					end := findBlockEndCStyle(lines, i)

					symbols = append(symbols, Symbol{
						Name:      name,
						Kind:      "method",
						StartLine: lineNum,
						EndLine:   end,
						Signature: sig,
					})
				}
			} else if m := cStyleArrowFnRe.FindStringSubmatch(line); m != nil {
				name := namedGroup(cStyleArrowFnRe, m, "name")
				if !isCStyleKeyword(name) && !strings.Contains(line, ";") && !strings.Contains(line, "}") {
					sig := captureCStyleSignature(lines, i)
					end := findBlockEndCStyle(lines, i)

					symbols = append(symbols, Symbol{
						Name:      name,
						Kind:      "method",
						StartLine: lineNum,
						EndLine:   end,
						Signature: sig,
					})
				}
			}
		}

		depth = updateCStyleDepth(line, depth)
	}

	return symbols, nil
}

func findCStyleBodyBounds(lines []string, startIdx int) (startL, endL int, ok bool) {
	parenDepth := 0
	foundParenStart := false
	foundParenEnd := false

	inDouble := false
	inSingle := false
	inBacktick := false
	escaped := false

	var braceStartLine = -1
	braceDepth := 0

	for l := startIdx; l < len(lines); l++ {
		line := lines[l]
		runes := []rune(line)
		for col := 0; col < len(runes); col++ {
			ch := runes[col]
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}

			// Handle quotes/comments/literals
			switch ch {
			case '"':
				if !inSingle && !inBacktick {
					inDouble = !inDouble
				}
				continue
			case '\'':
				if !inDouble && !inBacktick {
					inSingle = !inSingle
				}
				continue
			case '`':
				if !inDouble && !inSingle {
					inBacktick = !inBacktick
				}
				continue
			}

			if inDouble || inSingle || inBacktick {
				continue
			}

			if !foundParenEnd {
				if !foundParenStart {
					if ch == '(' {
						foundParenStart = true
						parenDepth = 1
					}
				} else {
					if ch == '(' {
						parenDepth++
					} else if ch == ')' {
						parenDepth--
						if parenDepth == 0 {
							foundParenEnd = true
						}
					}
				}
			} else {
				if braceStartLine == -1 {
					if ch == ';' {
						return -1, -1, false
					}
					if ch == '{' {
						braceStartLine = l
						braceDepth = 1
					}
				} else {
					if ch == '{' {
						braceDepth++
					} else if ch == '}' {
						braceDepth--
						if braceDepth == 0 {
							return braceStartLine, l, true
						}
					}
				}
			}
		}

		if braceStartLine == -1 && l-startIdx > 10 {
			return -1, -1, false
		}

		inDouble = false
		inSingle = false
		escaped = false
	}
	return -1, -1, false
}

func foldCStyle(lines []string) []string {
	var out []string
	i := 0
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if strings.Contains(trimmed, ";") || !strings.Contains(trimmed, "(") {
			out = append(out, line)
			i++
			continue
		}

		isSig := false
		if cStyleMethodRe.MatchString(trimmed) {
			name := namedGroup(cStyleMethodRe, cStyleMethodRe.FindStringSubmatch(trimmed), "name")
			if !isCStyleKeyword(name) {
				isSig = true
			}
		}
		if !isSig && cStyleArrowFnRe.MatchString(trimmed) {
			isSig = true
		}
		if !isSig && rustFnRe.MatchString(trimmed) {
			isSig = true
		}

		if isSig {
			startL, endL, ok := findCStyleBodyBounds(lines, i)
			if ok && endL > startL {
				for idx := i; idx <= startL; idx++ {
					out = append(out, lines[idx])
				}
				bodyLines := endL - startL - 1
				if bodyLines > 0 {
					indent := leadingWhitespace(lines[startL]) + "\t"
					out = append(out, fmt.Sprintf("%s// [body folded: %d lines]", indent, bodyLines))
				}
				out = append(out, lines[endL])
				i = endL + 1
				continue
			}
		}

		out = append(out, line)
		i++
	}
	return out
}

func findProtoBodyBounds(lines []string, startIdx int) (startL, endL int, ok bool) {
	braceStartLine := -1
	braceDepth := 0
	inDouble := false
	inSingle := false
	escaped := false

	for l := startIdx; l < len(lines); l++ {
		line := lines[l]
		runes := []rune(line)
		for col := 0; col < len(runes); col++ {
			ch := runes[col]
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				if !inSingle {
					inDouble = !inDouble
				}
				continue
			}
			if ch == '\'' {
				if !inDouble {
					inSingle = !inSingle
				}
				continue
			}
			if inDouble || inSingle {
				continue
			}

			if braceStartLine == -1 {
				if ch == '{' {
					braceStartLine = l
					braceDepth = 1
				}
			} else {
				if ch == '{' {
					braceDepth++
				} else if ch == '}' {
					braceDepth--
					if braceDepth == 0 {
						return braceStartLine, l, true
					}
				}
			}
		}
		inDouble = false
		inSingle = false
	}
	return -1, -1, false
}

func foldProto(lines []string) []string {
	var out []string
	protoBlockRe := regexp.MustCompile(`^\s*(?:message|service|enum|rpc)\s+[A-Za-z_][A-Za-z0-9_]*`)
	i := 0
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if protoBlockRe.MatchString(trimmed) {
			startL, endL, ok := findProtoBodyBounds(lines, i)
			if ok && endL > startL {
				for idx := i; idx <= startL; idx++ {
					out = append(out, lines[idx])
				}
				bodyLines := endL - startL - 1
				if bodyLines > 0 {
					indent := leadingWhitespace(lines[startL]) + "\t"
					out = append(out, fmt.Sprintf("%s// [body folded: %d lines]", indent, bodyLines))
				}
				out = append(out, lines[endL])
				i = endL + 1
				continue
			}
		}

		out = append(out, line)
		i++
	}
	return out
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func splitLines(src []byte) ([]string, error) {
	src = bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n"))
	const maxLineSize = 1024 * 1024  // 1MB per line max
	sc := bufio.NewScanner(bytes.NewReader(src))
	buf := make([]byte, maxLineSize)
	sc.Buffer(buf, maxLineSize)
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scanning file: %w", err)
	}
	return lines, nil
}

func findBlockEndCStyle(lines []string, startIdx int) int {
	depth := 0
	hasOpened := false

	for i := startIdx; i < len(lines); i++ {
		line := lines[i]
		inDoubleQuote := false
		inSingleQuote := false
		inBacktick := false
		escaped := false

		runes := []rune(line)
		for j := 0; j < len(runes); j++ {
			ch := runes[j]
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}

			switch ch {
			case '"':
				if !inSingleQuote && !inBacktick {
					inDoubleQuote = !inDoubleQuote
				}
			case '\'':
				if !inDoubleQuote && !inBacktick {
					inSingleQuote = !inSingleQuote
				}
			case '`':
				if !inDoubleQuote && !inSingleQuote {
					inBacktick = !inBacktick
				}
			case '{':
				if !inDoubleQuote && !inSingleQuote && !inBacktick {
					depth++
					hasOpened = true
				}
			case '}':
				if !inDoubleQuote && !inSingleQuote && !inBacktick {
					depth--
				}
			}
		}

		if hasOpened && depth == 0 {
			return i + 1
		}
	}
	return startIdx + 1
}

func captureCStyleSignature(lines []string, startIdx int) string {
	var sigLines []string
	for i := startIdx; i < len(lines); i++ {
		line := lines[i]
		if idx := strings.Index(line, "{"); idx >= 0 {
			sigLines = append(sigLines, line[:idx])
			break
		}
		sigLines = append(sigLines, line)
	}
	return strings.TrimSpace(strings.Join(sigLines, " "))
}

func updateCStyleDepth(line string, currentDepth int) int {
	inDoubleQuote := false
	inSingleQuote := false
	inBacktick := false
	escaped := false

	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}

		switch ch {
		case '"':
			if !inSingleQuote && !inBacktick {
				inDoubleQuote = !inDoubleQuote
			}
		case '\'':
			if !inDoubleQuote && !inBacktick {
				inSingleQuote = !inSingleQuote
			}
		case '`':
			if !inDoubleQuote && !inSingleQuote {
				inBacktick = !inBacktick
			}
		case '{':
			if !inDoubleQuote && !inSingleQuote && !inBacktick {
				currentDepth++
			}
		case '}':
			if !inDoubleQuote && !inSingleQuote && !inBacktick {
				currentDepth--
			}
		}
	}
	return currentDepth
}

func signatureFromLine(line string) string {
	sig := strings.TrimSuffix(strings.TrimSpace(line), "{")
	return strings.TrimSpace(sig)
}

func namedGroup(re *regexp.Regexp, match []string, name string) string {
	for i, n := range re.SubexpNames() {
		if n == name && i < len(match) {
			return match[i]
		}
	}
	return ""
}

func leadingWhitespace(s string) string {
	for i, ch := range s {
		if ch != ' ' && ch != '\t' {
			return s[:i]
		}
	}
	return s
}

func stripCommentsFromLine(line string, lang Language) (cleanLine string, openedBlock bool, openedTripleDouble bool, openedTripleSingle bool) {
	inDouble := false
	inSingle := false
	inBacktick := false
	escaped := false

	runes := []rune(line)
	n := len(runes)
	var clean []rune

	for i := 0; i < n; i++ {
		ch := runes[i]
		if escaped {
			clean = append(clean, ch)
			escaped = false
			continue
		}
		if ch == '\\' {
			clean = append(clean, ch)
			escaped = true
			continue
		}

		switch ch {
		case '"':
			if !inSingle && !inBacktick {
				if lang == LangPython && i+2 < n && runes[i+1] == '"' && runes[i+2] == '"' {
					closedOnSame := false
					for j := i + 3; j < n-2; j++ {
						if runes[j] == '"' && runes[j+1] == '"' && runes[j+2] == '"' {
							closedOnSame = true
							i = j + 2
							break
						}
					}
					if closedOnSame {
						continue
					}
					openedTripleDouble = true
					return string(clean), false, true, false
				}
				inDouble = !inDouble
			}
		case '\'':
			if !inDouble && !inBacktick {
				if lang == LangPython && i+2 < n && runes[i+1] == '\'' && runes[i+2] == '\'' {
					closedOnSame := false
					for j := i + 3; j < n-2; j++ {
						if runes[j] == '\'' && runes[j+1] == '\'' && runes[j+2] == '\'' {
							closedOnSame = true
							i = j + 2
							break
						}
					}
					if closedOnSame {
						continue
					}
					openedTripleSingle = true
					return string(clean), false, false, true
				}
				inSingle = !inSingle
			}
		case '`':
			if !inDouble && !inSingle {
				inBacktick = !inBacktick
			}
		}

		if !inDouble && !inSingle && !inBacktick {
			if lang == LangPython {
				if ch == '#' {
					// Preserve folded body markers
					if strings.Contains(string(runes[i:]), "body folded:") {
						// Keep it, do not break
					} else {
						break
					}
				}
			} else {
				if ch == '/' && i+1 < n {
					next := runes[i+1]
					if next == '/' {
						// Preserve folded body markers
						if strings.Contains(string(runes[i:]), "body folded:") {
							// Keep it, do not break
						} else {
							break
						}
					}
					if next == '*' {

						openedBlock = true
						closedOnSame := false
						for j := i + 2; j < n-1; j++ {
							if runes[j] == '*' && runes[j+1] == '/' {
								closedOnSame = true
								i = j + 1
								break
							}
						}
						if closedOnSame {
							openedBlock = false
							continue
						}
						return string(clean), true, false, false
					}
				}
			}
		}

		clean = append(clean, ch)
	}

	return string(clean), false, false, false
}

func countIndentation(line string) int {
	count := 0
	for _, ch := range line {
		if ch == ' ' {
			count++
		} else if ch == '\t' {
			count += 4
		} else {
			break
		}
	}
	return count
}

func capturePythonSignature(lines []string, startIdx int) string {
	var sigLines []string
	for i := startIdx; i < len(lines); i++ {
		line := lines[i]
		if idx := strings.Index(line, ":"); idx >= 0 {
			sigLines = append(sigLines, line[:idx])
			break
		}
		sigLines = append(sigLines, line)
	}
	return strings.TrimSpace(strings.Join(sigLines, " ")) + ":"
}

func isCStyleKeyword(name string) bool {
	switch name {
	case "if", "for", "while", "switch", "catch", "synchronized", "new", "return", "throw", "else", "try", "finally", "typeof", "instanceof", "sizeof":
		return true
	default:
		return false
	}
}

func isBinary(data []byte) bool {
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return !utf8.Valid(data)
}
