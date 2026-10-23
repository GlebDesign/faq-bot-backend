FROM golang:1.24-alpine AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
COPY backend/ ./
RUN mkdir -p migrations && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /api ./cmd/api && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /migrate ./cmd/migrate

FROM scratch
COPY --from=build /api /api
COPY --from=build /migrate /migrate
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /src/migrations /migrations
ENV MIGRATIONS_PATH=/migrations
USER 65532:65532
ENV APP_ENV=production HTTP_LISTEN_ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["/api"]
