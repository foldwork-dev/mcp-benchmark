package main

import (
	"testing"
)

func TestMatchesGitignore(t *testing.T) {
	patterns := []string{
		"*.log",
		"temp/",
		"build",
		"ignored_file.txt",
	}

	tests := []struct {
		path string
		want bool
	}{
		{"test.log", true},
		{"src/test.log", true},
		{"temp/file.txt", true},
		{"build/output", true},
		{"ignored_file.txt", true},
		{"src/ignored_file.txt", true},
		{"main.go", false},
		{"src/main.go", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := matchesGitignore(patterns, tt.path)
			if got != tt.want {
				t.Errorf("matchesGitignore(%v, %q) = %v; want %v", patterns, tt.path, got, tt.want)
			}
		})
	}
}
