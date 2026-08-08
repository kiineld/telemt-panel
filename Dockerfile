FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/panel ./cmd/panel

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/panel /usr/local/bin/panel
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/panel"]
