# syntax=docker/dockerfile:1

# Build stage
FROM golang:1.23.3 AS build
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# produce a static binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o server .

# Final minimal image
FROM gcr.io/distroless/base-debian12
WORKDIR /app
COPY --from=build /app/server /app/server
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/app/server"]
