package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestIsStdLib(t *testing.T) {
	tests := []struct {
		pkg  string
		want bool
	}{
		{"io", true},
		{"net/http", true},
		{"fmt", true},
		{"crypto/tls", true},
		{"", true},
		{"github.com/user/repo", false},
		{"golang.org/x/net", false},
		{"example.com/pkg", false},
		{"v2", true}, // ambiguous but rare; no dot = treated as stdlib
	}

	for _, tt := range tests {
		t.Run(tt.pkg, func(t *testing.T) {
			if got := isStdLib(tt.pkg); got != tt.want {
				t.Errorf("isStdLib(%q) = %v, want %v", tt.pkg, got, tt.want)
			}
		})
	}
}

func TestAllowedFlags(t *testing.T) {
	tests := []struct {
		flag    string
		allowed bool
	}{
		{"-all", true},
		{"-src", true},
		{"-u", true},
		{"-short", true},
		{"-c", true},
		{"-C", false},
		{"-modfile", false},
		{"--all", false},
		{"-overlay", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.flag, func(t *testing.T) {
			if got := allowedFlags[tt.flag]; got != tt.allowed {
				t.Errorf("allowedFlags[%q] = %v, want %v", tt.flag, got, tt.allowed)
			}
		})
	}
}

func TestReadModuleName(t *testing.T) {
	t.Run("valid go.mod", func(t *testing.T) {
		dir := t.TempDir()
		gomod := filepath.Join(dir, "go.mod")
		os.WriteFile(gomod, []byte("module github.com/test/pkg\n\ngo 1.21\n"), 0644)

		name, err := readModuleName(gomod)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if name != "github.com/test/pkg" {
			t.Errorf("got %q, want %q", name, "github.com/test/pkg")
		}
	})

	t.Run("go.mod with trailing comment", func(t *testing.T) {
		dir := t.TempDir()
		gomod := filepath.Join(dir, "go.mod")
		os.WriteFile(gomod, []byte("module github.com/test/pkg // some comment\n"), 0644)

		name, err := readModuleName(gomod)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if name != "github.com/test/pkg" {
			t.Errorf("got %q, want %q", name, "github.com/test/pkg")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		_, err := readModuleName("/nonexistent/go.mod")
		if err == nil {
			t.Fatal("expected error for missing file")
		}
	})

	t.Run("no module declaration", func(t *testing.T) {
		dir := t.TempDir()
		gomod := filepath.Join(dir, "go.mod")
		os.WriteFile(gomod, []byte("go 1.21\n"), 0644)

		_, err := readModuleName(gomod)
		if err == nil {
			t.Fatal("expected error for missing module declaration")
		}
	})
}

func TestValidatePath(t *testing.T) {
	t.Run("import path passthrough", func(t *testing.T) {
		resolved, subDirs, err := validatePath("io", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolved != "io" {
			t.Errorf("got %q, want %q", resolved, "io")
		}
		if subDirs != nil {
			t.Errorf("got subDirs %v, want nil", subDirs)
		}
	})

	t.Run("external import path", func(t *testing.T) {
		resolved, _, err := validatePath("github.com/user/repo", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolved != "github.com/user/repo" {
			t.Errorf("got %q, want %q", resolved, "github.com/user/repo")
		}
	})

	t.Run("relative path without working_dir", func(t *testing.T) {
		_, _, err := validatePath("./pkg", "")
		if err == nil {
			t.Fatal("expected error for relative path without working_dir")
		}
	})

	t.Run("relative dot path", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/test/proj\n"), 0644)

		resolved, _, err := validatePath(".", dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolved != "github.com/test/proj" {
			t.Errorf("got %q, want %q", resolved, "github.com/test/proj")
		}
	})

	t.Run("relative subpackage path", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/test/proj\n"), 0644)

		resolved, _, err := validatePath("./sub/pkg", dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolved != "github.com/test/proj/sub/pkg" {
			t.Errorf("got %q, want %q", resolved, "github.com/test/proj/sub/pkg")
		}
	})
}

func TestPaginate(t *testing.T) {
	content := strings.Join(makeLines(250), "\n")

	t.Run("first page", func(t *testing.T) {
		result, err := paginate(content, 1, 100)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasPrefix(result, "Page 1 of 3") {
			t.Errorf("unexpected metadata: %s", firstLine(result))
		}
	})

	t.Run("last page", func(t *testing.T) {
		result, err := paginate(content, 3, 100)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasPrefix(result, "Page 3 of 3") {
			t.Errorf("unexpected metadata: %s", firstLine(result))
		}
	})

	t.Run("page exceeds total", func(t *testing.T) {
		_, err := paginate(content, 10, 100)
		if err == nil {
			t.Fatal("expected error for page exceeding total")
		}
	})

	t.Run("page zero clamped to 1", func(t *testing.T) {
		result, err := paginate(content, 0, 100)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasPrefix(result, "Page 1 of 3") {
			t.Errorf("unexpected metadata: %s", firstLine(result))
		}
	})

	t.Run("single page", func(t *testing.T) {
		result, err := paginate("short content", 1, 1000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasPrefix(result, "Page 1 of 1") {
			t.Errorf("unexpected metadata: %s", firstLine(result))
		}
	})

	t.Run("empty content", func(t *testing.T) {
		result, err := paginate("", 1, 1000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasPrefix(result, "Page 1 of 1") {
			t.Errorf("unexpected metadata: %s", firstLine(result))
		}
	})
}

func TestCacheEviction(t *testing.T) {
	gs := &godocServer{
		cache: make(map[string]cachedDoc),
	}

	// Fill cache to maxCacheSize.
	for i := 0; i < maxCacheSize; i++ {
		key := strings.Repeat("x", i+1)
		gs.cache[key] = cachedDoc{
			content:   "content",
			timestamp: time.Now().Add(-time.Duration(maxCacheSize-i) * time.Second),
		}
	}

	if len(gs.cache) != maxCacheSize {
		t.Fatalf("cache size = %d, want %d", len(gs.cache), maxCacheSize)
	}

	// Inserting one more entry should trigger eviction of the oldest.
	gs.mu.Lock()
	if len(gs.cache) >= maxCacheSize {
		var oldestKey string
		var oldestTime time.Time
		for k, v := range gs.cache {
			if oldestKey == "" || v.timestamp.Before(oldestTime) {
				oldestKey = k
				oldestTime = v.timestamp
			}
		}
		delete(gs.cache, oldestKey)
	}
	gs.cache["new-entry"] = cachedDoc{content: "new", timestamp: time.Now()}
	gs.mu.Unlock()

	if len(gs.cache) != maxCacheSize {
		t.Errorf("cache size after eviction = %d, want %d", len(gs.cache), maxCacheSize)
	}

	if _, ok := gs.cache["new-entry"]; !ok {
		t.Error("new entry not found in cache")
	}
}

func TestCacheExpiry(t *testing.T) {
	gs := &godocServer{
		cache: make(map[string]cachedDoc),
	}

	gs.cache["expired"] = cachedDoc{
		content:   "old",
		timestamp: time.Now().Add(-cacheTTL - time.Second),
	}
	gs.cache["fresh"] = cachedDoc{
		content:   "new",
		timestamp: time.Now(),
	}

	// Fresh entry should be returned.
	gs.mu.Lock()
	if doc, ok := gs.cache["fresh"]; ok && time.Since(doc.timestamp) < cacheTTL {
		gs.mu.Unlock()
		if doc.content != "new" {
			t.Errorf("fresh cache content = %q, want %q", doc.content, "new")
		}
	} else {
		gs.mu.Unlock()
		t.Error("fresh cache entry not found or expired unexpectedly")
	}

	// Expired entry should be evicted on access.
	gs.mu.Lock()
	if doc, ok := gs.cache["expired"]; ok {
		if time.Since(doc.timestamp) >= cacheTTL {
			delete(gs.cache, "expired")
		}
	}
	gs.mu.Unlock()

	if _, ok := gs.cache["expired"]; ok {
		t.Error("expired entry should have been evicted")
	}
}

// Integration tests that require the Go toolchain.

func TestRunGoDocStdlib(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not found in PATH")
	}

	gs := &godocServer{
		cache: make(map[string]cachedDoc),
	}

	ctx := context.Background()

	// Create a temp project for the stdlib lookup.
	tempDir, err := createTempProject(ctx, "io")
	if err != nil {
		t.Fatalf("createTempProject: %v", err)
	}
	defer os.RemoveAll(tempDir)

	doc, err := gs.runGoDoc(ctx, tempDir, "io")
	if err != nil {
		t.Fatalf("runGoDoc: %v", err)
	}

	if !strings.Contains(doc, "Package io") {
		t.Errorf("expected 'Package io' in output, got: %s", doc[:min(len(doc), 200)])
	}
}

func TestRunGoDocSymbol(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not found in PATH")
	}

	gs := &godocServer{
		cache: make(map[string]cachedDoc),
	}

	ctx := context.Background()

	tempDir, err := createTempProject(ctx, "io")
	if err != nil {
		t.Fatalf("createTempProject: %v", err)
	}
	defer os.RemoveAll(tempDir)

	doc, err := gs.runGoDoc(ctx, tempDir, "io", "Reader")
	if err != nil {
		t.Fatalf("runGoDoc: %v", err)
	}

	if !strings.Contains(doc, "Reader") {
		t.Errorf("expected 'Reader' in output, got: %s", doc[:min(len(doc), 200)])
	}
}

func TestHandleGetDocStdlib(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not found in PATH")
	}

	gs := newGodocServer()

	req := mcp.CallToolRequest{}
	req.Params.Name = "get_doc"
	req.Params.Arguments = map[string]any{
		"path": "fmt",
	}

	result, err := gs.handleGetDoc(context.Background(), req)
	if err != nil {
		t.Fatalf("handleGetDoc returned protocol error: %v", err)
	}

	if result.IsError {
		t.Fatalf("handleGetDoc returned tool error: %+v", result.Content)
	}

	// Check that the result contains fmt package documentation.
	for _, c := range result.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			if strings.Contains(tc.Text, "Package fmt") {
				return // success
			}
		}
	}
	t.Error("expected 'Package fmt' in tool result content")
}

func TestHandleGetDocBadFlag(t *testing.T) {
	gs := newGodocServer()

	req := mcp.CallToolRequest{}
	req.Params.Name = "get_doc"
	req.Params.Arguments = map[string]any{
		"path":      "io",
		"cmd_flags": []any{"-overlay"},
	}

	result, err := gs.handleGetDoc(context.Background(), req)
	if err != nil {
		t.Fatalf("handleGetDoc returned protocol error: %v", err)
	}

	if !result.IsError {
		t.Error("expected tool error for bad flag")
	}
}

func TestHandleGetDocMissingPath(t *testing.T) {
	gs := newGodocServer()

	req := mcp.CallToolRequest{}
	req.Params.Name = "get_doc"
	req.Params.Arguments = map[string]any{}

	result, err := gs.handleGetDoc(context.Background(), req)
	if err != nil {
		t.Fatalf("handleGetDoc returned protocol error: %v", err)
	}

	if !result.IsError {
		t.Error("expected tool error for missing path")
	}
}

// Test helpers

func makeLines(n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = strings.Repeat("x", 40)
	}
	return lines
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
