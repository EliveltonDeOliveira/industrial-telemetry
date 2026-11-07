# Builds any Go service of this module into a static binary on an empty image.
# Usage, from the repository root: docker build --build-arg SERVICE=simulator .
FROM golang:1.26.5-alpine AS build
ARG SERVICE
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
COPY services ./services
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/service ./services/${SERVICE}

FROM scratch
COPY --from=build /out/service /service
USER 65534:65534
ENTRYPOINT ["/service"]
