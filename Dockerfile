# Local debug image. The mini-PC layout in the README is what runs on site.
FROM golang:1.25-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mediateca-station ./cmd/station

FROM debian:bookworm-slim

# policy-rc.d stops the dnsmasq package from starting during install.
# The entrypoint starts it after the television NIC is named eth0.
RUN printf '#!/bin/sh\nexit 101\n' > /usr/sbin/policy-rc.d \
    && chmod 0755 /usr/sbin/policy-rc.d \
    && apt-get update \
    && apt-get install --no-install-recommends -y ca-certificates adb iproute2 dnsmasq \
    && rm -rf /var/lib/apt/lists/* \
    && rm /usr/sbin/policy-rc.d

COPY --from=build /out/mediateca-station /usr/local/bin/mediateca-station
COPY docker/devnet.sh /usr/local/lib/devnet.sh
COPY docker/dnsmasq.conf /usr/local/lib/dnsmasq.conf
COPY docker/entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 0755 /usr/local/bin/docker-entrypoint.sh /usr/local/lib/devnet.sh \
    && install -d -m 0755 /var/lib/misc \
    && install -d -m 0700 /var/lib/mediateca-station /etc/mediateca-station

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
