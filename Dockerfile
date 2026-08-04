# takoda99-server コンテナ。CI・ローカル検証用。
#
# 本番（GCP e2-micro）はコンテナを使わず、静的バイナリを systemd で常駐させる（docs/deploy.md）。
# RAM 1GB の VM で Docker デーモンを常駐させる余裕が無いため。
#
# go.work は使わず go.mod/go.sum で public な Takoda99-Proto を解決する（CIと同条件）。
# go は go.mod の go ディレクティブ（現状 1.25.0 / pgx v5 が要求）に合わせる。
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /server ./cmd/server

FROM alpine:3.20
RUN adduser -D -H app
COPY --from=build /server /server
USER app
# サーバーは $PORT があればそれを、無ければ 8080 を listen する。
EXPOSE 8080
ENTRYPOINT ["/server"]
CMD ["--mode", "match"]
