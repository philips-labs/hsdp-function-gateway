FROM jpillora/chisel:1.7.7 as chisel

FROM golang:alpine3.13 as builder
WORKDIR /build
COPY go.mod .
COPY go.sum .
RUN go mod download

# Build
COPY . .
RUN go build -o app

## Build final image
FROM alpine:3.16.1
LABEL maintainer="andy.lo-a-foe@philips.com"
RUN apk add --no-cache ca-certificates supervisor jq curl && rm -rf /tmp/* /var/cache/apk/*
RUN apk add --no-cache yq --repository http://dl-cdn.alpinelinux.org/alpine/edge/community

RUN mkdir -p /sidecars/bin /sidecars/supervisor/conf.d sidecars/etc

COPY supervisord_configs/ /sidecars/supervisor/conf.d
COPY --from=builder /build/app /sidecars/bin
COPY --from=chisel /app/chisel /sidecars/bin

EXPOSE 8080

COPY supervisord.conf /etc/
CMD ["supervisord", "--nodaemon", "--configuration", "/etc/supervisord.conf"]
