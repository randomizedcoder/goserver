#
# github.com/network-quality/goserver/Dockerfile
#
# trunk-ignore-all(trivy/DS026)
# trunk-ignore-all(checkov/CKV_DOCKER_2)
#
# Build with:
# docker build --build-arg GIT_COMMIT=$(git rev-parse HEAD)--build-arg DATE=$(date -u +%Y_%m_%d_%H_%M_%S) -t rpmserver .

# https://docs.docker.com/reference/dockerfile/#arg
ARG GO_VERSION=1.22.4

FROM golang:${GO_VERSION} AS BUILD

WORKDIR /go/src

COPY . .

ARG GIT_COMMIT=NotSet
ARG DATE=NotSet
RUN echo "GIT_COMMIT=${GIT_COMMIT} DATE=${DATE}"

# RUN go mod download \
#     && \
#     CGO_ENABLED=0 go build -ldflags "-s -w -X main.commit=${GIT_COMMIT} -X main.date=${DATE}" -o /networkqualityd
# https://words.filippo.io/shrink-your-go-binaries-with-this-one-weird-trick/
RUN make

# https://github.com/GoogleContainerTools/distroless
# Distroless images are very small. The smallest distroless image, gcr.io/distroless/static-debian11,
# is around 2 MiB. That's about 50% of the size of alpine (~5 MiB), and less than 2% of the size of debian (124 MiB).
# https://github.com/GoogleContainerTools/distroless?tab=readme-ov-file#docker
FROM gcr.io/distroless/static-debian12:nonroot AS FINAL

ARG U=nonroot
USER ${U}:${U}

# Configure default values that a user can override.
ENV cert_file=/live/fullchain.pem
ENV key_file=/live/privkey.pem
ENV public_name=networkquality.example.com
ENV config_name=networkquality.example.com
ENV public_port=4043
ENV listen_addr=0.0.0.0

COPY --from=BUILD --chown=${U}:${U} --chmod=555 /networkqualityd /networkqualityd

# By default, this is what the container will run when `docker run`
# is issued by a user.
# trunk-ignore(hadolint/DL3025)
ENTRYPOINT ["/networkqualityd", \
    "--cert-file", "${cert_file}", \
    "--key-file", "${key_file}", \
    "--public-name", "${public_name}", \
    "--public-port", "${public_port}", \
    "--config-name", "${config_name}", \
    "--listen-addr", "${listen_addr}", \
    "--enable-prom", \
    "${debug}" ]

# end