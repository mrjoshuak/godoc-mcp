FROM golang:1.23-alpine AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o godoc-mcp .

FROM golang:1.23-alpine

RUN apk add --no-cache git ca-certificates

COPY --from=builder /build/godoc-mcp /usr/local/bin/godoc-mcp

ENTRYPOINT ["godoc-mcp"]
