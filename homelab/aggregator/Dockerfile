# Build: a static binary (no C library needed at run time).
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /aggregator .

# Run: distroless has no shell or package manager, only what a static binary needs (CA certificates, tzdata,
# /etc/passwd with the nonroot user). Less inside means less to attack and nothing to patch but this image.
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /aggregator /aggregator
COPY certs/pve-root-ca.pem /etc/aggregator/pve-root-ca.pem
ENV PVE_CA_FILE=/etc/aggregator/pve-root-ca.pem LISTEN=:8099
# nonroot = uid 65532: if the process were ever taken over, it has no root inside the container.
USER nonroot:nonroot
EXPOSE 8099
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/aggregator", "-healthcheck"]
ENTRYPOINT ["/aggregator"]
