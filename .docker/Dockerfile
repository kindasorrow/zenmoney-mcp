ARG GO_VERSION=1.27

FROM golang:${GO_VERSION} AS build

WORKDIR /src

# Зависимости отдельным слоем: они меняются реже кода, и правка исходника не тянет модули заново.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# cgo выключен: modernc.org/sqlite на чистом Go, а в финальном образе нет libc для линковки.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/zenmoney-mcp ./cmd/zenmoney-mcp \
    && mkdir /out/data

# В distroless/static только CA-сертификаты, tzdata и nonroot, без шелла и пакетного менеджера.
FROM gcr.io/distroless/static-debian13:nonroot

COPY --from=build /out/zenmoney-mcp /usr/local/bin/zenmoney-mcp

# Каталог под базу: named volume берёт владельца и права из образа, иначе nonroot не создаст файл.
COPY --from=build --chown=65532:65532 /out/data /data

USER nonroot

ENTRYPOINT ["/usr/local/bin/zenmoney-mcp"]
