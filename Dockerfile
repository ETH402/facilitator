# syntax=docker/dockerfile:1.7
# Base images are pinned by digest, not tag: docs/DEPLOYMENT.md promises it, and a
# floating tag means the image that passed review is not necessarily the image that
# ships. Update deliberately.
FROM golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/eth402 ./cmd/eth402 \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

FROM gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35
WORKDIR /app
COPY --from=build /out/eth402 /usr/local/bin/eth402
COPY --from=build /out/migrate /usr/local/bin/migrate
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/eth402"]
