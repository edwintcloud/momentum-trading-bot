# Stage 1: Build React dashboard
FROM node:22-alpine AS web-builder
WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --production=false
COPY web/ ./
RUN npm run build

# Stage 2: Build Go binary
FROM golang:1.24-alpine AS go-builder
RUN apk add --no-cache git ca-certificates
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web-builder /app/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /momentum-bot .

# Stage 3: Final minimal image
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=go-builder /momentum-bot /app/momentum-bot
COPY --from=web-builder /app/web/dist /app/web/dist
COPY profiles/ /app/profiles/
COPY .env.example /app/.env.example

EXPOSE 8080
ENTRYPOINT ["/app/momentum-bot"]
CMD ["live"]
