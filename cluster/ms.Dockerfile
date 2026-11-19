# syntax=docker/dockerfile:1
# Backend image. Build context: ../ms (see docker-compose.yml).

FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/pff ./cmd/pff

# Distroless: no shell, no package manager, runs as an unprivileged user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/pff /pff
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/pff"]
