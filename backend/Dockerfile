# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /build

# Optional Moses in-cluster package cache (Nexus). Empty = upstream default.
ARG GOPROXY=""
ENV GOPROXY=${GOPROXY:-https://proxy.golang.org,direct}

COPY go.mod go.sum* ./
RUN go mod download

COPY . .

RUN go mod verify
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o server ./cmd/server

# Runtime stage
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

COPY --from=builder /build/server .

EXPOSE 8080

USER nonroot:nonroot

ENTRYPOINT ["/app/server"]
