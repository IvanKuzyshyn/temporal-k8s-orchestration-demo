# Test-only image: reuse the integration test binary as a transparent TCP relay.
FROM golang:1.27.3-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
COPY services ./services
COPY tests/integration ./tests/integration
RUN CGO_ENABLED=0 go test -c -tags=integration,resilience_integration -o /out/integration.test ./tests/integration

FROM debian:bookworm-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8
COPY --from=build /out/integration.test /integration.test
USER 65532:65532
ENTRYPOINT ["/integration.test", "-test.run=^TestTemporalRelayFixture$", "-test.timeout=30m", "-test.v"]
