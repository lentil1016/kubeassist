FROM golang:1.25 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o backend .

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /app/backend /backend
USER nonroot:nonroot
ENTRYPOINT ["/backend"]
