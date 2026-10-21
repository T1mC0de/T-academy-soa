FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/catalog-service ./cmd/catalog-service

FROM alpine:3.22
COPY --from=build /out/catalog-service /app/catalog-service
USER nobody
EXPOSE 8080
ENTRYPOINT ["/app/catalog-service"]
