# The README's images, rendered from the real app in demo mode: `make screenshots`.
FROM ubuntu:26.04
ARG GO_VERSION=1.26.3
RUN apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
      build-essential pkg-config libgtk-3-dev libgirepository1.0-dev librsvg2-common librsvg2-bin \
      xvfb x11-utils x11-apps xdotool imagemagick ffmpeg fonts-dejavu-core fonts-dejavu-extra \
      ca-certificates curl >/dev/null \
 && rm -rf /var/lib/apt/lists/* \
 && curl -fsSL https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz | tar -C /usr/local -xz
ENV PATH=/usr/local/go/bin:$PATH
