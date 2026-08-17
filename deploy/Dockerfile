FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/gateway ./cmd/gateway

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/gateway /gateway
EXPOSE 8080 9090
ENTRYPOINT ["/gateway"]
