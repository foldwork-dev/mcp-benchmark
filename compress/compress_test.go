package compress

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type testCase struct {
	name      string
	extension string
	src       string
	wantFold  string
	wantStrip string
}

func TestCompress(t *testing.T) {
	cases := []testCase{
		{
			name:      "Go — function body folded, comment stripped",
			extension: ".go",
			src: `package main

// This is a doc comment
func Greet(name string) string {
	// inner comment
	msg := "Hello, " + name
	return msg
}
`,
			wantFold:  "[body folded:",
			wantStrip: "// This is a doc comment",
		},
		{
			name:      "Java — method body folded, Javadoc stripped",
			extension: ".java",
			src: `public class Greeter {
    /**
     * Javadoc comment
     */
    public String greet(String name) {
        // inner comment
        String msg = "Hello, " + name;
        return msg;
    }
}
`,
			wantFold:  "[body folded:",
			wantStrip: "Javadoc comment",
		},
		{
			name:      "Python — def body folded, # comment stripped",
			extension: ".py",
			src: `def greet(name):
    # inner comment
    return "Hello, " + name
`,
			wantFold:  "[body folded:",
			wantStrip: "inner comment",
		},
		{
			name:      "TypeScript — function body folded, // comment stripped",
			extension: ".ts",
			src: `// File-level comment
export class Greeter {
    // method comment
    greet(name: string): string {
        // inner comment
        return "Hello, " + name;
    }
}
`,
			wantFold:  "[body folded:",
			wantStrip: "method comment",
		},
		{
			name:      "Rust — fn body folded, // comment stripped",
			extension: ".rs",
			src: `// Rust module comment
pub fn greet(name: &str) -> String {
    // inner comment
    format!("Hello, {}", name)
}
`,
			wantFold:  "[body folded:",
			wantStrip: "Rust module comment",
		},
		{
			name:      "JavaScript — function body folded, comment stripped",
			extension: ".js",
			src: `// JS module comment
function greet(name) {
    // inner comment
    return "Hello, " + name;
}
`,
			wantFold:  "[body folded:",
			wantStrip: "JS module comment",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			tmpFile := filepath.Join(tmpDir, "test"+tc.extension)
			if err := os.WriteFile(tmpFile, []byte(tc.src), 0600); err != nil {
				t.Fatalf("setup: write temp file: %v", err)
			}

			src := []byte(tc.src)

			// 1. Test empty file does not panic
			_, err := Compress(tmpFile, []byte{}, 3)
			if err != nil {
				t.Errorf("empty file error: %v", err)
			}

			// 2. Test Tier 2 - body folded
			tier2, err := Compress(tmpFile, src, 2)
			if err != nil {
				t.Fatalf("tier2 error: %v", err)
			}
			if !strings.Contains(string(tier2), tc.wantFold) {
				t.Errorf("tier2: expected %q in output. Got:\n%s", tc.wantFold, string(tier2))
			}

			// 3. Test Tier 3 - comment stripped (but marker survives)
			tier3, err := Compress(tmpFile, src, 3)
			if err != nil {
				t.Fatalf("tier3 error: %v", err)
			}
			if strings.Contains(string(tier3), tc.wantStrip) {
				t.Errorf("tier3: comment %q should have been stripped. Got:\n%s", tc.wantStrip, string(tier3))
			}
			// Verify that the folded marker survived comment stripping
			if !strings.Contains(string(tier3), tc.wantFold) {
				t.Errorf("tier3: expected marker %q to survive comment stripping. Got:\n%s", tc.wantFold, string(tier3))
			}
		})
	}
}

func TestRustImplFoldingReduction(t *testing.T) {
	src := `impl Greeter {
    pub fn greet(&self, name: &str) -> String {
        // nested comment
        let greeting = format!("Hello, {}", name);
        println!("{}", greeting);
        greeting
    }
}`
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.rs")
	if err := os.WriteFile(tmpFile, []byte(src), 0600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	tier2, err := Compress(tmpFile, []byte(src), 2)
	if err != nil {
		t.Fatalf("Compress tier2 error: %v", err)
	}

	// Verify [body folded: exists
	if !strings.Contains(string(tier2), "[body folded:") {
		t.Errorf("Expected [body folded:] in tier2 output: %s", string(tier2))
	}

	// Verify reduction > 35%
	rawLen := len(src)
	foldedLen := len(tier2)
	reduction := (1.0 - float64(foldedLen)/float64(rawLen)) * 100
	if reduction < 35.0 {
		t.Errorf("Rust reduction was %.2f%%, expected > 35%%", reduction)
	}
}

func TestPythonClassFolding(t *testing.T) {
	src := `class Greeter:
    # class-level comment
    def __init__(self, prefix):
        self.prefix = prefix
    def greet(self, name):
        return f"{self.prefix}, {name}"
`
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.py")
	if err := os.WriteFile(tmpFile, []byte(src), 0600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	tier2, err := Compress(tmpFile, []byte(src), 2)
	if err != nil {
		t.Fatalf("Compress tier 2 error: %v", err)
	}

	// Verify class body is folded with [class body folded: N lines]
	if !strings.Contains(string(tier2), "[class body folded:") {
		t.Errorf("Expected class body folded marker. Got:\n%s", string(tier2))
	}

	// Verify class signature line is kept
	if !strings.Contains(string(tier2), "class Greeter:") {
		t.Errorf("Expected class signature to be preserved. Got:\n%s", string(tier2))
	}

	// Verify class body folded marker survives tier 3 comment stripping
	tier3, err := Compress(tmpFile, []byte(src), 3)
	if err != nil {
		t.Fatalf("Compress tier 3 error: %v", err)
	}
	if !strings.Contains(string(tier3), "[class body folded:") {
		t.Errorf("Expected class body folded marker to survive tier 3 comment stripping. Got:\n%s", string(tier3))
	}
}

func TestUnsupportedExtensionReturnsOriginalBytes(t *testing.T) {
	src := `This is a plain text file.
It should not be compressed or altered in any way.
`
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "readme.txt")
	if err := os.WriteFile(tmpFile, []byte(src), 0600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	tier3, err := Compress(tmpFile, []byte(src), 3)
	if err != nil {
		t.Fatalf("Compress tier 3 error: %v", err)
	}

	if string(tier3) != src {
		t.Errorf("Expected original bytes returned for unsupported extension. Got:\n%s", string(tier3))
	}
}

func TestPythonFoldedOutputIsValidSyntax(t *testing.T) {
	src := `def hello():
    x = 1
    y = 2
    return x + y

class MyClass:
    def method(self):
        pass
`
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.py")
	if err := os.WriteFile(tmpFile, []byte(src), 0600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	tier2, err := Compress(tmpFile, []byte(src), 2)
	if err != nil {
		t.Fatalf("Compress tier 2 error: %v", err)
	}

	// Let's write the folded bytes to a file
	foldedFile := filepath.Join(tmpDir, "folded.py")
	if err := os.WriteFile(foldedFile, tier2, 0600); err != nil {
		t.Fatalf("write folded file: %v", err)
	}

	// Verify syntax using python3 AST parser
	cmd := exec.Command("python3", "-c", "import ast; ast.parse(open('" + foldedFile + "', 'rb').read())")
	if err := cmd.Run(); err != nil {
		t.Errorf("Folded Python output is not valid syntax: %v. Output was:\n%s", err, string(tier2))
	}
}

func TestBinaryFileSkipping(t *testing.T) {
	src := []byte("hello\x00world\x00this\x00is\x00binary")
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "binary_file.go")
	if err := os.WriteFile(tmpFile, src, 0600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	res, err := Compress(tmpFile, src, 3)
	if err != nil {
		t.Fatalf("Compress binary file error: %v", err)
	}

	if string(res) != string(src) {
		t.Errorf("Expected original bytes returned for binary file. Got:\n%q", string(res))
	}
}
