# syntax=docker/dockerfile:1.7
# Base images are pinned by digest, not tag: docs/DEPLOYMENT.md promises it, and a
# floating tag means the image that passed review is not necessarily the image that
# ships. Update deliberately.
FROM golang:1.26.6-alpine@sha256:3889b425f035be855a72fb4755265311293b6d414521f0a519d819df32222d83 AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/eth402 ./cmd/eth402 \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/policysigner ./cmd/policysigner

# The signing boundary ships as its own image, built with --target policysigner.
# Sharing one image with the facilitator would work, but the boundary exists to be a
# separate workload with a separate identity, and an image containing both invites a
# deployment that runs the wrong binary under the wrong service account — which
# silently collapses the boundary it is meant to create.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35 AS policysigner
WORKDIR /app
COPY --from=build /out/policysigner /usr/local/bin/policysigner
USER nonroot:nonroot
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/policysigner"]

FROM gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35
WORKDIR /app
COPY --from=build /out/eth402 /usr/local/bin/eth402
COPY --from=build /out/migrate /usr/local/bin/migrate
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/eth402"]
