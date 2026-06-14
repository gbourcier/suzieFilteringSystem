FROM golang:1.26.4-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/digestd ./cmd/digestd \
    && mkdir -p /out/data \
    && chown -R 65532:65532 /out/data

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/digestd /digestd
COPY --from=build --chown=65532:65532 /out/data /data

VOLUME ["/data"]
ENTRYPOINT ["/digestd"]
