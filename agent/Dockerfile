# Build from the repository root: docker build -f agent/Dockerfile -t argus-agent .
FROM golang:1.26 AS build
WORKDIR /src
COPY sdk/go ./sdk/go
COPY agent ./agent
WORKDIR /src/agent
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /argus-agent .

FROM gcr.io/distroless/static-debian12
COPY --from=build /argus-agent /argus-agent
ENTRYPOINT ["/argus-agent"]
