package diff

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

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

	// Format to terminal escape sequences using the "swapoff" style
	// which provides realistic language-aware colors on dark terminals.
	var buf strings.Builder
	formatter := formatters.TTY8
	style := styles.Get("swapoff")
	if style == nil {
		style = styles.Fallback
	}
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
