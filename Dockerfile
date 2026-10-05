FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go openapi.yaml ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /api-teste .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /api-teste /api-teste
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/api-teste"]
