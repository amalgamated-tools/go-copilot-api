# Stage 1: Build Go binary
FROM golang:1.26-alpine AS backend
ARG VERSION=dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -ldflags "-X main.version=${VERSION}" -o copilot-api-go ./cmd/copilot-api

# Stage 2: Final image
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app

COPY --from=backend /app/copilot-api-go .

# Create the token directory
RUN mkdir -p /root/.local/share/copilot-api

EXPOSE 4141

# Add a health check or wrapper script
COPY check_token.sh /app/
RUN chmod +x /app/check_token.sh

ENTRYPOINT ["/app/check_token.sh"]
CMD ["start", "--port", "4141", "--host", "0.0.0.0"]
