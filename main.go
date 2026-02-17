package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mark3labs/mcp-go/server"
)

const version = "1.0.1"

func main() {
	transport := flag.String("transport", "stdio", "Transport type: stdio, sse, or http")
	addr := flag.String("addr", ":8080", "Listen address for sse/http transport")
	flag.Parse()

	log.SetOutput(os.Stderr)
	log.Printf("Starting godoc-mcp server v%s (%s transport)...", version, *transport)

	gs := newGodocServer()
	defer gs.cleanup()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	switch *transport {
	case "stdio":
		go func() {
			<-sigCh
			gs.cleanup()
			os.Exit(0)
		}()
		if err := server.ServeStdio(gs.mcpServer); err != nil {
			log.Printf("Server error: %v", err)
			os.Exit(1)
		}

	case "sse":
		host := *addr
		if strings.HasPrefix(host, ":") {
			host = "localhost" + host
		}
		sseServer := server.NewSSEServer(gs.mcpServer,
			server.WithBaseURL("http://"+host),
			server.WithKeepAlive(true),
		)
		go func() {
			<-sigCh
			log.Printf("Shutting down...")
			gs.cleanup()
			sseServer.Shutdown(context.Background())
		}()
		log.Printf("SSE server listening on %s", *addr)
		if err := sseServer.Start(*addr); err != nil {
			log.Printf("Server stopped: %v", err)
		}

	case "http":
		httpServer := server.NewStreamableHTTPServer(gs.mcpServer)
		go func() {
			<-sigCh
			log.Printf("Shutting down...")
			gs.cleanup()
			httpServer.Shutdown(context.Background())
		}()
		log.Printf("HTTP server listening on %s", *addr)
		if err := httpServer.Start(*addr); err != nil {
			log.Printf("Server stopped: %v", err)
		}

	default:
		fmt.Fprintf(os.Stderr, "unknown transport: %s (use stdio, sse, or http)\n", *transport)
		os.Exit(1)
	}
}
