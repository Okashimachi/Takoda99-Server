# Takoda99 サーバーの開発・デプロイ用ショートカット。
#
# 手順の正典は docs/deploy.md、当日の対処は docs/runbook.md、
# 本戦前の残タスクは docs/pre-event-checklist.md。
# ここは「毎回打つ長いコマンド」を短くするだけで、新しい手順は増やさない。
#
# ⚠ CD（自動デプロイ）は**あえて組んでいない**。デプロイは systemctl restart を伴い、
#    試合状態は in-memory なので**進行中の試合が消える**。「今は試合中でない」と
#    人が確認してから打てることが、この規模では利点になる。

SHELL     := /bin/bash
HASH      := $(shell git rev-parse --short HEAD)
OUT_DIR   := $(HOME)/takoda99-backup
BIN       := $(OUT_DIR)/takoda99-server-$(HASH)
VM        := takoda99-server
ZONE      := us-west1-b
REMOTE    := /opt/takoda99/server
BASE      := https://takoda99.mooo.com

# go.work がローカルの Proto を束ねているため、検証は必ず GOWORK=off（CI と同条件）。
GO := GOWORK=off

.PHONY: help
help: ## このヘルプを出す
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk 'BEGIN{FS=":.*?## "}{printf "  \033[1m%-16s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "  よく使う流れ:  make check → make build → make deploy → make verify"

# ── 検証 ───────────────────────────────────────────────

.PHONY: check
check: ## CI と同じ検査を回す（build/vet/test/lint）
	$(GO) go build ./...
	$(GO) go vet ./...
	$(GO) go test ./...
	$(GO) go test -race ./internal/game/...
	$(GO) golangci-lint run
	@echo "✅ CI 相当の検査がすべて通った"

.PHONY: sim
sim: ## バランス確認（決着時間・分離度・負スコア）
	$(GO) go run ./cmd/matchsim --runs 5 --profile normal

.PHONY: wirelog
wirelog: ## クライアント連携用の生ログを出す（本番同等：99店・本番スケジュール）
	@mkdir -p $(OUT_DIR)
	WIRE_LOG_PRODUCTION=1 WIRE_LOG_OUT=$(OUT_DIR)/wire-log-production.jsonl \
	  $(GO) go test ./internal/app/ -run TestWireContract_ProductionLog -v -timeout 300s
	@echo "→ $(OUT_DIR)/wire-log-production.jsonl"

# ── ビルド ─────────────────────────────────────────────

.PHONY: build
build: ## 本番用バイナリを ~/takoda99-backup/ にビルド（Finder から見える場所）
	@mkdir -p $(OUT_DIR)
	$(GO) CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	  go build -trimpath -ldflags="-s -w" -o $(BIN) ./cmd/server
	@ls -lh $(BIN)
	@echo ""
	@echo "次にやること:"
	@echo "  gcloud があるなら → make deploy"
	@echo "  無いなら         → GCPコンソール SSH の ⚙ でアップロードして下記を実行"
	@echo "    sudo install -o takoda99 -g takoda99 -m 755 ~/takoda99-server-$(HASH) $(REMOTE) && sudo systemctl restart takoda99"

# ── デプロイ ───────────────────────────────────────────

.PHONY: deploy
deploy: build ## ビルドして本番へ転送・再起動（gcloud 必須・試合の合間に）
	@command -v gcloud >/dev/null || { \
	  echo "🔴 gcloud が無い。'make deploy-help' を見るか、コンソールから手動で入れる"; exit 1; }
	@echo "⚠ 進行中の試合が消える。試合中でないことを確認してから続行してください。"
	@read -p "続行する? [y/N] " yn; [ "$$yn" = "y" ] || { echo "中止"; exit 1; }
	gcloud compute scp $(BIN) $(VM):~/ --zone $(ZONE)
	gcloud compute ssh $(VM) --zone $(ZONE) --command \
	  'sudo install -o takoda99 -g takoda99 -m 755 ~/takoda99-server-$(HASH) $(REMOTE) && sudo systemctl restart takoda99'
	@echo "✅ デプロイ完了。'make verify' で実値を確認すること"

.PHONY: verify
verify: ## 本番の実値を確認（コードの既定値は本番に効かない。DB値が優先）
	@echo "── 設定（DBから読めているか）──"
	@curl -s --max-time 10 $(BASE)/api/params \
	  | jq '{configHash, score, odai, \
	         cull: {targetAliveCount: [.cull.stages[].targetAliveCount], warnMaxIds: .cull.warnMaxIds}, \
	         publish, sanity, heat}' \
	  || echo "🔴 JSON が返らない。config の Validate に落ちている可能性（journalctl を見る）"
	@echo "── 疎通 ──"
	@curl -s -o /dev/null -w "  /healthz : %{http_code}\n" --max-time 10 $(BASE)/healthz
	@curl -s -o /dev/null -w "  /admin/  : %{http_code}\n" --max-time 10 $(BASE)/admin/
	@echo ""
	@echo "⚠ ここに出ない項目は「確認できていない」。パラメータを足したらこの jq にも足すこと。"
	@echo "  configHash が変われば設定が変わった証拠（config-front の表示と突き合わせる）。"

.PHONY: logs
logs: ## 本番のログを見る（gcloud 必須）
	gcloud compute ssh $(VM) --zone $(ZONE) --command \
	  'sudo journalctl -u takoda99 -n 50 --no-pager'

.PHONY: deploy-help
deploy-help: ## gcloud の入れ方
	@echo "macOS:  brew install --cask google-cloud-sdk"
	@echo "その後:  gcloud auth login && gcloud config set project <プロジェクトID>"
	@echo "確認  :  gcloud compute instances list   # $(VM) が見えればOK"
	@echo ""
	@echo "入れなくてもデプロイはできる（GCPコンソールの SSH → ⚙ ファイルをアップロード）。"
	@echo "詳細は docs/deploy.md"
