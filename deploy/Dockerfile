# build-kontext ist die repo-wurzel: docker build -f deploy/Dockerfile .
# digest per: docker buildx imagetools inspect golang:1.27-alpine
FROM golang:1.27-alpine@sha256:cf6fca6641884b8433441b2b0652976f975e1d0fdd26d177eaaf8596087f3125 AS build
ARG VERSION=dev
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/dmn-status ./cmd/dmn-status

# distroless hat keine shell, /data deshalb schon hier mit dem richtigen owner anlegen
RUN mkdir -p /out/data && chown 65532:65532 /out/data

# digest per: docker buildx imagetools inspect gcr.io/distroless/static-debian12:nonroot
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/dmn-status /dmn-status
COPY --from=build --chown=65532:65532 /out/data /data

USER nonroot
EXPOSE 8080
HEALTHCHECK CMD ["/dmn-status", "healthcheck"]
ENTRYPOINT ["/dmn-status"]
CMD ["serve"]
