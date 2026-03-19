FROM node:20-alpine AS web-builder
WORKDIR /build/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.24-alpine AS go-builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY internal ./internal
COPY profiles ./profiles
COPY --from=web-builder /build/web/dist ./web/dist
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64
RUN go build -o /out/momentum-bot .

FROM alpine:3.21
WORKDIR /app
RUN apk add --no-cache ca-certificates tzdata \
	&& addgroup -S app \
	&& adduser -S -G app app
COPY --from=go-builder /out/momentum-bot /app/momentum-bot
COPY --from=go-builder /build/web/dist /app/web/dist
COPY --from=go-builder /build/profiles /app/profiles
RUN chown -R app:app /app
USER app
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 CMD wget -q -O - http://127.0.0.1:8080/healthz >/dev/null || exit 1
CMD ["/app/momentum-bot"]
