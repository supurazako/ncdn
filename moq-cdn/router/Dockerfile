FROM golang:1.24-alpine AS build

WORKDIR /src
COPY main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/moq-router ./main.go

FROM alpine:3.22
COPY --from=build /out/moq-router /usr/local/bin/moq-router
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/moq-router"]
