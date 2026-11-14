# syntax=docker/dockerfile:1
# Build context: the repo root.

FROM golang:1.27.4-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY gen/go ./gen/go
COPY internal ./internal
COPY services/narrator ./services/narrator
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/narrator ./services/narrator/cmd/narrator

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/narrator /narrator
ENTRYPOINT ["/narrator"]
