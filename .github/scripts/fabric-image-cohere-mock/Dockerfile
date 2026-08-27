FROM golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36 AS build

WORKDIR /src
COPY main.go ./main.go
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "-s -w" -o /out/mock ./main.go

FROM scratch
COPY --from=build /out/mock /mock
ENTRYPOINT ["/mock"]
