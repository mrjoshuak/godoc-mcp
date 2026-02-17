package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	cacheTTL     = 5 * time.Minute
	maxCacheSize = 500
	cmdTimeout   = 30 * time.Second
)

// allowedFlags is the set of go doc flags permitted via cmd_flags.
var allowedFlags = map[string]bool{
	"-all":   true,
	"-src":   true,
	"-u":     true,
	"-short": true,
	"-c":     true,
}

const toolDescription = `Get Go documentation for a package, type, function, or method.
This is the preferred and most efficient way to understand Go packages, providing official package
documentation in a concise format. Use this before attempting to read source files directly. Results
are cached and optimized for AI consumption.

Best Practices:
1. ALWAYS try this tool first before reading package source code
2. Start with basic package documentation before looking at source code or specific symbols
3. Use -all flag when you need comprehensive package documentation
4. Only look up specific symbols after understanding the package overview

Common Usage Patterns:
- Standard library: Use just the package name (e.g., "io", "net/http")
- External packages: Use full import path (e.g., "github.com/user/repo")
- Local packages: Use relative path (e.g., "./pkg") or absolute path

The documentation is cached for 5 minutes to improve performance.`

type cachedDoc struct {
	content   string
	timestamp time.Time
}

type godocServer struct {
	mcpServer *server.MCPServer
	mu        sync.Mutex
	cache     map[string]cachedDoc
}

func newGodocServer() *godocServer {
	gs := &godocServer{
		cache: make(map[string]cachedDoc),
	}

	s := server.NewMCPServer(
		"godoc-mcp",
		version,
		server.WithToolCapabilities(true),
		server.WithLogging(),
		server.WithRecovery(),
	)
	gs.mcpServer = s

	tool := mcp.NewTool("get_doc",
		mcp.WithDescription(toolDescription),
		mcp.WithString("path",
			mcp.Required(),
			mcp.Description("Path to the Go package or file. Import path (e.g., 'io', 'github.com/user/repo') or local file path."),
		),
		mcp.WithString("target",
			mcp.Description("Specific symbol to document (function, type, interface). Leave empty for full package docs."),
		),
		mcp.WithArray("cmd_flags",
			mcp.Description("Additional go doc flags: -all (all docs), -src (source code), -u (unexported symbols), -short, -c."),
			mcp.WithStringItems(),
		),
		mcp.WithString("working_dir",
			mcp.Description("Working directory for module context. Required for relative paths (including '.')."),
		),
		mcp.WithNumber("page",
			mcp.Description("Page number (1-based) for paginated results."),
			mcp.Min(1),
			mcp.DefaultNumber(1),
		),
		mcp.WithNumber("page_size",
			mcp.Description("Lines per page."),
			mcp.Min(100),
			mcp.Max(5000),
			mcp.DefaultNumber(1000),
		),
	)
	s.AddTool(tool, gs.handleGetDoc)

	return gs
}

func (gs *godocServer) handleGetDoc(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	pkgPath, err := request.RequireString("path")
	if err != nil {
		return mcp.NewToolResultError("path argument is required"), nil
	}

	target := request.GetString("target", "")
	workingDir := request.GetString("working_dir", "")
	page := request.GetInt("page", 1)
	pageSize := request.GetInt("page_size", 1000)

	// Validate working_dir exists and is a directory.
	if workingDir != "" {
		info, err := os.Stat(workingDir)
		if err != nil || !info.IsDir() {
			return mcp.NewToolResultError(fmt.Sprintf("invalid working directory: %s", workingDir)), nil
		}
	}

	// Validate cmd_flags against allowlist.
	cmdFlags := request.GetStringSlice("cmd_flags", nil)
	for _, f := range cmdFlags {
		if !allowedFlags[f] {
			allowed := make([]string, 0, len(allowedFlags))
			for k := range allowedFlags {
				allowed = append(allowed, k)
			}
			return mcp.NewToolResultError(fmt.Sprintf("unsupported flag %q (allowed: %s)", f, strings.Join(allowed, ", "))), nil
		}
	}

	// Resolve the path to an import path.
	resolvedPath, subDirs, err := validatePath(pkgPath, workingDir)
	if err != nil {
		if subDirs != nil {
			msg := fmt.Sprintf("No Go files found in %s, but found Go packages in:\n%s", pkgPath, strings.Join(subDirs, "\n"))
			return mcp.NewToolResultText(msg), nil
		}
		return mcp.NewToolResultError(err.Error()), nil
	}
	pkgPath = resolvedPath

	// Create a temporary project if no working directory was provided.
	if workingDir == "" {
		tempDir, err := createTempProject(ctx, pkgPath)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to create temporary project: %v", err)), nil
		}
		defer os.RemoveAll(tempDir)
		workingDir = tempDir
	}

	// Build go doc arguments.
	var args []string
	args = append(args, cmdFlags...)
	args = append(args, pkgPath)
	if target != "" {
		args = append(args, target)
	}

	doc, err := gs.runGoDoc(ctx, workingDir, args...)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// Paginate the output.
	result, err := paginate(doc, page, pageSize)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	return mcp.NewToolResultText(result), nil
}

// paginate splits content into pages and returns the requested page with metadata.
func paginate(content string, page, pageSize int) (string, error) {
	if page < 1 {
		page = 1
	}

	lines := strings.Split(content, "\n")
	totalLines := len(lines)
	totalPages := (totalLines + pageSize - 1) / pageSize
	if totalPages < 1 {
		totalPages = 1
	}

	if page > totalPages {
		return "", fmt.Errorf("page %d exceeds total pages %d", page, totalPages)
	}

	start := (page - 1) * pageSize
	end := start + pageSize
	if end > totalLines {
		end = totalLines
	}

	pageContent := strings.Join(lines[start:end], "\n")
	metadata := fmt.Sprintf("Page %d of %d (showing lines %d-%d of %d)",
		page, totalPages, start+1, end, totalLines)

	return metadata + "\n\n" + pageContent, nil
}

// validatePath resolves a user-provided path to a Go import path.
func validatePath(pkgPath, workingDir string) (string, []string, error) {
	// Relative paths require a working directory to resolve module context.
	if strings.HasPrefix(pkgPath, ".") {
		if workingDir == "" {
			return "", nil, fmt.Errorf("working_dir is required for relative paths (including '.')")
		}

		moduleName, err := readModuleName(filepath.Join(workingDir, "go.mod"))
		if err != nil {
			return "", nil, fmt.Errorf("failed to read go.mod in working directory: %w", err)
		}

		if pkgPath == "." {
			return moduleName, nil, nil
		}
		relPath := strings.TrimPrefix(pkgPath, "./")
		return path.Join(moduleName, relPath), nil, nil
	}

	// Absolute paths: read go.mod from the given path.
	if strings.HasPrefix(pkgPath, "/") || filepath.IsAbs(pkgPath) {
		if workingDir != "" && pkgPath != workingDir {
			return "", nil, fmt.Errorf("absolute path must match working directory when provided")
		}

		moduleName, err := readModuleName(filepath.Join(pkgPath, "go.mod"))
		if err != nil {
			return "", nil, fmt.Errorf("failed to read go.mod: %w", err)
		}
		return moduleName, nil, nil
	}

	// Treat everything else as an import path.
	return pkgPath, nil, nil
}

// readModuleName extracts the module name from a go.mod file.
func readModuleName(goModPath string) (string, error) {
	content, err := os.ReadFile(goModPath)
	if err != nil {
		return "", fmt.Errorf("failed to read %s: %w", goModPath, err)
	}

	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "module ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			return fields[1], nil
		}
	}

	return "", fmt.Errorf("no module declaration found in %s", goModPath)
}

// createTempProject creates a temporary Go module for fetching documentation.
// The caller must clean up the returned directory with os.RemoveAll.
func createTempProject(ctx context.Context, importPath string) (string, error) {
	tempDir, err := os.MkdirTemp("", "godoc-mcp-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}

	initCtx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(initCtx, "go", "mod", "init", "godoc-temp")
	cmd.Dir = tempDir
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(tempDir)
		return "", fmt.Errorf("failed to initialize go.mod: %w\noutput: %s", err, out)
	}

	// For non-stdlib packages, download the dependency.
	if !isStdLib(importPath) {
		getCtx, cancel := context.WithTimeout(ctx, cmdTimeout)
		defer cancel()

		cmd = exec.CommandContext(getCtx, "go", "get", importPath)
		cmd.Dir = tempDir
		if out, err := cmd.CombinedOutput(); err != nil {
			os.RemoveAll(tempDir)
			return "", fmt.Errorf("failed to get package %s: %w\noutput: %s", importPath, err, out)
		}
	}

	return tempDir, nil
}

// runGoDoc executes go doc with caching.
func (gs *godocServer) runGoDoc(ctx context.Context, workingDir string, args ...string) (string, error) {
	cacheKey := workingDir + "|" + strings.Join(args, "|")

	gs.mu.Lock()
	if doc, ok := gs.cache[cacheKey]; ok {
		if time.Since(doc.timestamp) < cacheTTL {
			gs.mu.Unlock()
			log.Printf("Cache hit for %s", cacheKey)
			return doc.content, nil
		}
		delete(gs.cache, cacheKey)
	}
	gs.mu.Unlock()

	execCtx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "go", append([]string{"doc"}, args...)...)
	if workingDir != "" {
		cmd.Dir = workingDir
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", formatGoDocError(string(out), err)
	}

	content := string(out)

	gs.mu.Lock()
	// Evict oldest entry if cache is full.
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
	gs.cache[cacheKey] = cachedDoc{content: content, timestamp: time.Now()}
	gs.mu.Unlock()

	log.Printf("Cache miss for %s (%d bytes)", cacheKey, len(content))
	return content, nil
}

// formatGoDocError returns an enhanced error message with suggestions.
func formatGoDocError(output string, err error) error {
	switch {
	case strings.Contains(output, "no such package") || strings.Contains(output, "is not in std"):
		return fmt.Errorf("package not found:\n"+
			"1. For standard library packages, use just the package name (e.g., 'io', 'net/http')\n"+
			"2. For external packages, ensure they are imported in the module\n"+
			"3. For local packages, provide a relative path (e.g., './pkg') or absolute path\n"+
			"4. Check for typos in the package name\n"+
			"Detail: %s", output)

	case strings.Contains(output, "no such symbol"):
		return fmt.Errorf("symbol not found:\n"+
			"1. Check if the symbol name is correct (case-sensitive)\n"+
			"2. Use -u flag to see unexported symbols\n"+
			"3. Use -all flag to see all package documentation\n"+
			"Detail: %w", err)

	case strings.Contains(output, "build constraints exclude all Go files"):
		return fmt.Errorf("no Go files for current platform; try -all flag or set GOOS/GOARCH: %w", err)
	}

	return fmt.Errorf("go doc error: %w\noutput: %s", err, output)
}

// isStdLib returns true if the package path looks like a standard library package.
// Standard library packages do not contain a dot in the first path element.
func isStdLib(pkg string) bool {
	first, _, _ := strings.Cut(pkg, "/")
	return !strings.Contains(first, ".")
}
