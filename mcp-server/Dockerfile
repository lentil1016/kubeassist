FROM golang:1.25 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o mcp-server .

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /app/mcp-server /mcp-server
USER nonroot:nonroot
ENTRYPOINT ["/mcp-server"]
