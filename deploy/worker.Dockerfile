FROM golang:1.27.3-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
COPY services ./services
COPY cmd ./cmd
RUN CGO_ENABLED=0 go build -trimpath -o /out/app ./services/worker/cmd/worker && \
    CGO_ENABLED=0 go build -trimpath -o /out/healthcheck ./cmd/healthcheck

FROM debian:bookworm-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8
WORKDIR /app
COPY --from=build /out/ /app/
USER 65532:65532
EXPOSE 8081
ENTRYPOINT ["/app/app"]
