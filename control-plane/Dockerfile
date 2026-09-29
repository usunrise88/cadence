FROM golang:1.23 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/cadence ./cmd/cadence

FROM gcr.io/distroless/static
COPY --from=build /out/cadence /cadence
EXPOSE 8080
ENTRYPOINT ["/cadence"]
