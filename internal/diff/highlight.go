package diff

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// greenStyle is a Chroma style that maps all token types to green-ish
// shades. Additions get syntax-highlighted but still read as "green" so
// the reviewer immediately knows they're new.
var greenStyle = styles.Register(chroma.MustNewStyle("rhodium-green", chroma.StyleEntries{
	chroma.Text:        "#5cb85c",
	chroma.Keyword:     "#73d673",
	chroma.KeywordConstant: "#73d673",
	chroma.KeywordDeclaration: "#73d673",
	chroma.KeywordNamespace: "#73d673",
	chroma.KeywordPseudo: "#73d673",
	chroma.KeywordReserved: "#73d673",
	chroma.KeywordType: "#73d673",
	chroma.Name:        "#5cb85c",
	chroma.NameAttribute: "#8ae68a",
	chroma.NameBuiltin: "#8ae68a",
	chroma.NameBuiltinPseudo: "#8ae68a",
	chroma.NameClass:   "#8ae68a",
	chroma.NameConstant: "#8ae68a",
	chroma.NameDecorator: "#8ae68a",
	chroma.NameEntity:  "#8ae68a",
	chroma.NameException: "#8ae68a",
	chroma.NameFunction: "#8ae68a",
	chroma.NameFunctionMagic: "#8ae68a",
	chroma.NameLabel:   "#8ae68a",
	chroma.NameNamespace: "#8ae68a",
	chroma.NameOther:   "#5cb85c",
	chroma.NameTag:     "#8ae68a",
	chroma.NameVariable: "#8ae68a",
	chroma.NameVariableClass: "#8ae68a",
	chroma.NameVariableGlobal: "#8ae68a",
	chroma.NameVariableInstance: "#8ae68a",
	chroma.NameVariableMagic: "#8ae68a",
	chroma.Literal:     "#5cb85c",
	chroma.LiteralDate: "#5cb85c",
	chroma.LiteralString: "#4a9e4a",
	chroma.LiteralStringBacktick: "#4a9e4a",
	chroma.LiteralStringChar: "#4a9e4a",
	chroma.LiteralStringDelimiter: "#4a9e4a",
	chroma.LiteralStringDoc: "#4a9e4a",
	chroma.LiteralStringDouble: "#4a9e4a",
	chroma.LiteralStringEscape: "#4a9e4a",
	chroma.LiteralStringHeredoc: "#4a9e4a",
	chroma.LiteralStringInterpol: "#4a9e4a",
	chroma.LiteralStringOther: "#4a9e4a",
	chroma.LiteralStringRegex: "#4a9e4a",
	chroma.LiteralStringSingle: "#4a9e4a",
	chroma.LiteralStringSymbol: "#4a9e4a",
	chroma.LiteralNumber: "#5cb85c",
	chroma.LiteralNumberBin: "#5cb85c",
	chroma.LiteralNumberFloat: "#5cb85c",
	chroma.LiteralNumberHex: "#5cb85c",
	chroma.LiteralNumberInteger: "#5cb85c",
	chroma.LiteralNumberIntegerLong: "#5cb85c",
	chroma.LiteralNumberOct: "#5cb85c",
	chroma.Operator:      "#5cb85c",
	chroma.OperatorWord:  "#73d673",
	chroma.Punctuation:   "#5cb85c",
	chroma.Comment:       "#3d8a3d",
	chroma.CommentHashbang: "#3d8a3d",
	chroma.CommentMultiline: "#3d8a3d",
	chroma.CommentPreproc: "#3d8a3d",
	chroma.CommentPreprocFile: "#3d8a3d",
	chroma.CommentSingle: "#3d8a3d",
	chroma.CommentSpecial: "#3d8a3d",
	chroma.Generic:       "#5cb85c",
	chroma.GenericDeleted: "#9e4a4a",
	chroma.GenericEmph:   "#5cb85c",
	chroma.GenericError:  "#9e4a4a",
	chroma.GenericHeading: "#73d673",
	chroma.GenericInserted: "#73d673",
	chroma.GenericOutput: "#5cb85c",
	chroma.GenericPrompt: "#5cb85c",
	chroma.GenericStrong: "#73d673",
	chroma.GenericSubheading: "#73d673",
	chroma.GenericTraceback: "#9e4a4a",
	chroma.GenericUnderline: "#5cb85c",
	chroma.Whitespace:      "#5cb85c",
}))

// Highlighter holds the pre-computed syntax-highlighted lines for a file.
type Highlighter struct {
	lines []string // 0-indexed, one entry per file line
}

// NewHighlighter tokenizes the full file content and returns a highlighter
// with pre-rendered terminal escape sequences for each line. Returns nil
// if no lexer matches the filename.
func NewHighlighter(fileContent, filename string) *Highlighter {
	lexer := lexers.Match(filename)
	if lexer == nil {
		return nil
	}
	lexer = chroma.Coalesce(lexer)

	iterator, err := lexer.Tokenise(nil, fileContent)
	if err != nil {
		return nil
	}

	// Format to terminal escape sequences.
	var buf strings.Builder
	formatter := formatters.TTY8
	style := greenStyle
	err = formatter.Format(&buf, style, iterator)
	if err != nil {
		return nil
	}

	// Split into lines, preserving escape sequences.
	rawLines := strings.Split(buf.String(), "\n")
	// Trim trailing empty element from final newline.
	if len(rawLines) > 0 && rawLines[len(rawLines)-1] == "" {
		rawLines = rawLines[:len(rawLines)-1]
	}

	return &Highlighter{lines: rawLines}
}

// Line returns the pre-rendered highlighted line at index i (0-based).
// Returns "" if out of range.
func (h *Highlighter) Line(i int) string {
	if i < 0 || i >= len(h.lines) {
		return ""
	}
	return h.lines[i]
}

// Len returns the number of highlighted lines.
func (h *Highlighter) Len() int {
	return len(h.lines)
}
