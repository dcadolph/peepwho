# Build whotoask and run it from a minimal image. The web UI and the Slack bot
# both run from this image; the default command serves the web UI. The serve
# bind is 0.0.0.0 so the published port works, which means the container
# refuses to start until WHOTOASK_SERVE_TOKEN is set; every request must then
# carry that token.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build \
    -ldflags "-s -w -X github.com/dcadolph/whotoask/cmd.version=${VERSION}" \
    -o /out/whotoask .

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/whotoask /whotoask
USER nonroot:nonroot
ENTRYPOINT ["/whotoask"]
CMD ["serve", "--addr", "0.0.0.0:8765"]
