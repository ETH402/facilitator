# syntax=docker/dockerfile:1.7
FROM golang:1.26.5-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/eth402 ./cmd/eth402 \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/eth402 /usr/local/bin/eth402
COPY --from=build /out/migrate /usr/local/bin/migrate
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/eth402"]
