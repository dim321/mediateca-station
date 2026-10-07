# Local debug image. The mini-PC layout in the README is what runs on site.
FROM golang:1.25-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mediateca-station ./cmd/station

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install --no-install-recommends -y ca-certificates adb iproute2 \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/mediateca-station /usr/local/bin/mediateca-station
COPY docker/entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 0755 /usr/local/bin/docker-entrypoint.sh \
    && install -d -m 0700 /var/lib/mediateca-station /etc/mediateca-station

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
