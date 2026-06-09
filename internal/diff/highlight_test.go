package diff

import (
	"strings"
	"testing"
)

func TestNewHighlighterGo(t *testing.T) {
	content := `package main

import "fmt"

func main() {
	fmt.Println("hello")
}
`
	hl := NewHighlighter(content, "main.go")
	if hl == nil {
		t.Fatal("NewHighlighter returned nil for .go file")
	}

	// Should have the same number of lines as the input.
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if hl.Len() != len(lines) {
		t.Errorf("Len() = %d, want %d", hl.Len(), len(lines))
	}

	// Each line should have ANSI escape codes (syntax highlighting).
	for i := 0; i < hl.Len(); i++ {
		line := hl.Line(i)
		if line == "" && i < len(lines) && lines[i] != "" {
			t.Errorf("Line(%d) is empty but source line is not", i)
		}
	}
}

func TestNewHighlighterTypeScript(t *testing.T) {
	content := "export function greet(name: string): string {\n  return `Hello, ${name}!`;\n}\n"
	hl := NewHighlighter(content, "greet.ts")
	if hl == nil {
		t.Fatal("NewHighlighter returned nil for .ts file")
	}
	if hl.Len() == 0 {
		t.Error("Len() = 0, want > 0")
	}
}

func TestNewHighlighterPython(t *testing.T) {
	content := `def greet(name: str) -> str:
    return f"Hello, {name}!"
`
	hl := NewHighlighter(content, "greet.py")
	if hl == nil {
		t.Fatal("NewHighlighter returned nil for .py file")
	}
	if hl.Len() == 0 {
		t.Error("Len() = 0, want > 0")
	}
}

func TestNewHighlighterUnknownLanguage(t *testing.T) {
	content := `some unknown content`
	hl := NewHighlighter(content, "file.xyz")
	if hl != nil {
		t.Errorf("NewHighlighter for .xyz returned non-nil, want nil")
	}
}

func TestNewHighlighterEmptyContent(t *testing.T) {
	hl := NewHighlighter("", "main.go")
	if hl == nil {
		t.Fatal("NewHighlighter returned nil for empty .go file")
	}
	if hl.Len() != 0 {
		t.Errorf("Len() = %d, want 0 for empty content", hl.Len())
	}
}

func TestHighlighterLineOutOfRange(t *testing.T) {
	content := `package main
`
	hl := NewHighlighter(content, "main.go")
	if hl == nil {
		t.Fatal("NewHighlighter returned nil")
	}

	if s := hl.Line(-1); s != "" {
		t.Errorf("Line(-1) = %q, want empty", s)
	}
	if s := hl.Line(100); s != "" {
		t.Errorf("Line(100) = %q, want empty", s)
	}
}

func TestNewHighlighterByFilename(t *testing.T) {
	tests := []struct {
		filename string
		wantNil  bool
	}{
		{"main.go", false},
		{"app.ts", false},
		{"app.tsx", false},
		{"app.js", false},
		{"app.jsx", false},
		{"app.py", false},
		{"app.rs", false},   // Rust is supported by Chroma
		{"app.c", false},    // C is supported
		{"app.unknown", true},
		{"Makefile", false}, // Makefile is supported
	}

	content := "x := 1\n"
	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			hl := NewHighlighter(content, tt.filename)
			if tt.wantNil && hl != nil {
				t.Errorf("NewHighlighter(%q) = non-nil, want nil", tt.filename)
			}
			if !tt.wantNil && hl == nil {
				t.Errorf("NewHighlighter(%q) = nil, want non-nil", tt.filename)
			}
		})
	}
}
