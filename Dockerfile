FROM golang:1.24-alpine AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o copilot-api ./cmd/copilot-api/

FROM alpine:3.21

RUN apk add --no-cache ca-certificates wget \
    && adduser -D -u 1000 copilot

COPY --from=builder /build/copilot-api /usr/local/bin/copilot-api

USER copilot

VOLUME ["/home/copilot/.local/share/copilot-api"]

EXPOSE 4141

HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
  CMD wget -qO- http://localhost:4141/health || exit 1

ENTRYPOINT ["copilot-api"]
CMD ["start", "--host", "0.0.0.0", "--port", "4141"]
