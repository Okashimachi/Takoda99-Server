# textro99-server コンテナ。Render(docker runtime) 等でそのままデプロイできる。
# go.work は使わず go.mod/go.sum で public な Textro99-Proto を解決する（CIと同条件）。
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /server ./cmd/server

FROM alpine:3.20
RUN adduser -D -H app
COPY --from=build /server /server
USER app
# Render 等は $PORT を注入する。サーバーは $PORT があればそれを listen する。
EXPOSE 8080
ENTRYPOINT ["/server"]
CMD ["--mode", "match"]
