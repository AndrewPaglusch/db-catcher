FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /db-catcher .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /db-catcher /db-catcher
WORKDIR /config
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=10s CMD ["/db-catcher", "healthcheck"]
ENTRYPOINT ["/db-catcher"]
