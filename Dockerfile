FROM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -o vaultsync .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /app/vaultsync /vaultsync
USER nonroot:nonroot
ENTRYPOINT ["/vaultsync"]
