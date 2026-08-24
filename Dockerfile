FROM --platform=$BUILDPLATFORM golang:1.22.5-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/estate-service ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wget \
    && addgroup -S estate \
    && adduser -S -G estate -h /app estate
WORKDIR /app
COPY --from=build /out/estate-service /app/estate-service
RUN mkdir -p /app/data && chown -R estate:estate /app
USER estate
ENV HTTP_ADDR=:8080 DATABASE_PATH=/app/data/estate.db
EXPOSE 8080
HEALTHCHECK --interval=5s --timeout=2s --start-period=5s --retries=5 CMD wget -q -O - http://127.0.0.1:8080/readyz || exit 1
ENTRYPOINT ["/app/estate-service"]
