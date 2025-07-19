#!/bin/bash

# Check Go installation
which go >/dev/null 2>&1 || { echo "Error: Go not found"; exit 1; }

# Install godoc-mcp if not found
which godoc-mcp >/dev/null 2>&1 || {
    echo "Installing godoc-mcp..."
    go install github.com/mrjoshuak/godoc-mcp@latest
}

# Verify installation
GODOC_MCP_BINARY=$(which godoc-mcp 2>/dev/null)
[ -z "$GODOC_MCP_BINARY" ] && { echo "Error: godoc-mcp installation failed"; exit 1; }

# Echo the command for the user to run
echo "Run the following command to add the MCP server:"
echo
echo "claude mcp add godoc -e GOPATH=\"$(go env GOPATH)\" -e GOMODCACHE=\"$(go env GOMODCACHE)\" -- \"$GODOC_MCP_BINARY\""
echo
