FROM --platform=$BUILDPLATFORM golang:1.26.8 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VALIDATOR_IMAGE=rancher/harvester-precheck:dev
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.defaultValidatorImage=${VALIDATOR_IMAGE}" -o /harvester-precheck .

FROM scratch
COPY --from=build /harvester-precheck /usr/local/bin/harvester-precheck
ENTRYPOINT ["/usr/local/bin/harvester-precheck"]
