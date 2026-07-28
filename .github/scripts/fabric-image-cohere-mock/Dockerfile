FROM golang:1.26.5-bookworm@sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651 AS build

WORKDIR /src
COPY main.go ./main.go
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "-s -w" -o /out/mock ./main.go

FROM scratch
COPY --from=build /out/mock /mock
ENTRYPOINT ["/mock"]
