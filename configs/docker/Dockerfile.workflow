FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/root/.cache/go-build --mount=type=cache,target=/go/pkg/mod true
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/workflow-engine ./cmd/workflow-engine

FROM alpine:3.19
WORKDIR /app
COPY --from=build /out/workflow-engine ./workflow-engine
EXPOSE 8082
ENTRYPOINT ["./workflow-engine"]
