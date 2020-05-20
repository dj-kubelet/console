FROM golang:latest
COPY . /src
WORKDIR /src
RUN CGO_ENABLED=0 GOOS=linux go build ./cmd/console

FROM scratch
COPY --from=0 /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=0 /src/console /
COPY --from=0 /src/static /static
CMD ["/console"]
