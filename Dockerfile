FROM node:22-alpine AS web
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci
COPY tsconfig.json tsconfig.app.json tsconfig.node.json vite.config.ts playwright.config.ts ./
COPY web ./web
RUN npm run build

FROM golang:1.26-alpine AS server
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /roomcade ./cmd/roomcade

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /roomcade-db ./cmd/roomcade-db
FROM alpine:3.22
RUN addgroup -S roomcade && adduser -S -G roomcade roomcade
WORKDIR /app
COPY --from=server /roomcade /app/roomcade
COPY --from=server /roomcade-db /app/roomcade-db
COPY --from=web /src/dist /app/dist
RUN mkdir -p /var/data && chown -R roomcade:roomcade /var/data /app
USER roomcade
ENV ADDR=:10000 DATABASE_PATH=/var/data/roomcade.db STATIC_DIR=/app/dist SECURE_COOKIES=true
EXPOSE 10000
ENTRYPOINT ["/app/roomcade"]
