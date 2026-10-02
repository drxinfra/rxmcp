# Сборка статического бинарника и образ из scratch: внутри только rxmcp и корневые сертификаты.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /rxmcp .

FROM scratch
# Имя сервера в официальном MCP Registry: по этой метке реестр проверяет, что образ наш.
LABEL io.modelcontextprotocol.server.name="io.github.drxinfra/rxmcp"
LABEL org.opencontainers.image.source="https://github.com/drxinfra/rxmcp"
LABEL org.opencontainers.image.description="MCP server for Directum RX"
LABEL org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /rxmcp /rxmcp
# Настройки внутри контейнера: смонтируйте каталог сюда, если нужен профиль или cookie.
ENV RXMCP_HOME=/config
VOLUME /config
ENTRYPOINT ["/rxmcp"]
