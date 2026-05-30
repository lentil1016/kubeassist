FROM golang:1.24 AS builder
WORKDIR /app
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o backend .

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /app/backend /backend
USER nonroot:nonroot
ENTRYPOINT ["/backend"]
