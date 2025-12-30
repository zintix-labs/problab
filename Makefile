# Makefile for problab project

# -----------------------------------------------------------------------------
# 專案變數 (Project Variables)
# -----------------------------------------------------------------------------
PROFILING_DIR = build/profiling
BIN_DIR       = build/bin
BINARY_NAME   = run
BINARY_PATH   = $(BIN_DIR)/$(BINARY_NAME)

# PProf 輸出檔案定義 (請確保變數名稱一致)
CPU_PPROF    = $(PROFILING_DIR)/cpu.pprof
HEAP_PPROF   = $(PROFILING_DIR)/heap.pprof
ALLOCS_PPROF = $(PROFILING_DIR)/allocs.pprof

# flag簡寫
g  ?=
w  ?=
p  ?=
b  ?=
m  ?=
r  ?=
s  ?=

# 預設執行參數 (可由命令行覆蓋)
game     ?= 0
worker   ?= 1
players  ?= 1
bets     ?= 200
betmode  ?= 0
rounds   ?= 10000000
seed     ?= 2305843009213693951

# 依短參數覆蓋長參數，產生「實際要用的值」
GAME_E    := $(if $(g),$(g),$(game))
WORKER_E  := $(if $(w),$(w),$(worker))
PLAYERS_E := $(if $(p),$(p),$(players))
BETS_E    := $(if $(b),$(b),$(bets))
BETMODE_E := $(if $(m),$(m),$(betmode))
ROUNDS_E  := $(if $(r),$(r),$(rounds))
SEED_E    := $(if $(s),$(s),$(seed))


# 組合後的執行參數
RUN_ARGS = -game $(GAME_E) -worker $(WORKER_E) -player $(PLAYERS_E) -bets $(BETS_E) -mode $(BETMODE_E) -spins $(ROUNDS_E) -seed $(SEED_E)

# Profiling 專用參數 (對應 Go flag: var ProfileType = flag.String("p", ...))
PPROF_CPU_ARGS    = -p=cpu    $(RUN_ARGS)
PPROF_HEAP_ARGS   = -p=heap   $(RUN_ARGS)
PPROF_ALLOCS_ARGS = -p=allocs $(RUN_ARGS)

# Docker 設定
DOCKER_IMAGE ?= probsvr
DOCKER_TAG   ?= latest
DOCKER_PORT  ?= 5808

# 上色
GREEN = \033[1;32m
BLUE = \033[36m
RED = \033[1;31m
RESET = \033[0m

# 1. 偵測作業系統來決定執行檔副檔名 (Windows 需要 .exe)
ifeq ($(OS),Windows_NT)
    EXT = .exe
else
    EXT =
endif

# 定義工具的路徑與原始碼路徑
OPS_TOOL = ./scripts/bin/scripts$(EXT)
OPS_SRC = $(wildcard scripts/*.go)

# -----------------------------------------------------------------------------
# .PHONY (定義所有非檔案的目標)
# -----------------------------------------------------------------------------
.PHONY: all build run bin clean help h svr dev
.PHONY: pprof read-pprof heap read-heap allocs read-allocs pgo
.PHONY: test test-all test-detail
.PHONY: docker-build docker-run docker-sh docker-clean docker-prune

# 預設目標 (Default Target)
all: help

# -----------------------------------------------------------------------------
# [核心操作] (讓makefile自動編譯 ./script檔案) 
# -----------------------------------------------------------------------------

# 2. 定義依賴關係：Binary 依賴於 Source Code
# 如果 bin/ops 不存在，或者 scripts/ops.go 比 bin/ops 新，就會執行這段
$(OPS_TOOL): $(OPS_SRC)
	@echo "$(BLUE)Compiling ops tool...$(RESET)"
	@go build -o $(OPS_TOOL) ./scripts

# 3. 提供一個手動強制重新編譯的指令
build-tool:
	@go build -o $(OPS_TOOL) ./scripts
	@echo "Ops tool rebuilt."


# -----------------------------------------------------------------------------
# [基礎操作] (Basic Operations)
# -----------------------------------------------------------------------------

build: ## 編譯專案 (產出標準二進位檔至 build/bin/run)
	@printf "$(GREEN)Building standard binary...$(RESET)\n"
	@mkdir -p $(BIN_DIR)
	@go build -o $(BINARY_PATH) ./cmd/run

run: ## 使用 go run 執行模擬 (接受 GAME, WORKERS 等參數)
	@go run ./cmd/run $(RUN_ARGS)

svr: ## 啟動 HTTP Server（go run）
	@printf "$(GREEN)Starting HTTP Server...$(RESET)\n"
	@go run ./cmd/svr

bin: ## 執行已編譯的二進位檔 (不重新編譯)
	@printf "$(GREEN)Running compiled binary...$(RESET)\n"
	@./$(BINARY_PATH) $(RUN_ARGS)

dev: ## 啟動 Dev Web Panel (/dev)
	@go run ./cmd/dev

clean: ## 清理暫存 (清除 go cache 與 build 目錄)
	@printf "$(GREEN)Cleaning cache and build artifacts...$(RESET)\n"
	@go clean -cache
	@rm -rf $(BIN_DIR) $(PROFILING_DIR)

# -----------------------------------------------------------------------------
# [性能分析] (Profiling & Optimization)
# -----------------------------------------------------------------------------

pprof: ## 執行 CPU 分析 (產出 cpu.pprof)
	@printf "$(GREEN)Generating CPU profile...$(RESET)\n"
	@mkdir -p $(PROFILING_DIR)
	@go run ./cmd/run $(PPROF_CPU_ARGS)

read-pprof: ## 讀取 CPU profile (開啟瀏覽器 :6060)
	@if [ ! -f "$(CPU_PPROF)" ]; then \
	  printf "❌ $(RED)找不到 $(CPU_PPROF)，請先執行 'make pprof'$(RESET)\n"; exit 1; \
	fi
	@printf "✅ $(GREEN)開啟 CPU pprof (:6060)... (Ctrl+C 退出)$(RESET)\n"
	@go tool pprof -http=localhost:6060 $(CPU_PPROF)

heap: ## 執行 Heap 分析 (產出 heap.pprof, 用於檢測內存洩漏)
	@printf "$(GREEN)Generating Heap profile...$(RESET)\n"
	@mkdir -p $(PROFILING_DIR)
	@go run ./cmd/run $(PPROF_HEAP_ARGS)

read-heap: ## 讀取 Heap profile (開啟瀏覽器 :6061)
	@if [ ! -f "$(HEAP_PPROF)" ]; then \
	  printf "❌ $(RED)找不到 $(HEAP_PPROF)，請先執行 'make heap'$(RESET)\n"; exit 1; \
	fi
	@printf "✅ $(GREEN)開啟 Heap pprof (:6061)... (Ctrl+C 退出)$(RESET)\n"
	@go tool pprof -http=localhost:6061 $(HEAP_PPROF)

allocs: ## 執行 Allocs 分析 (產出 allocs.pprof, 用於檢測總分配量)
	@printf "$(GREEN)Generating Allocs profile...$(RESET)\n"
	@mkdir -p $(PROFILING_DIR)
	@go run ./cmd/run $(PPROF_ALLOCS_ARGS)

read-allocs: ## 讀取 Allocs profile (開啟瀏覽器 :6062)
	@if [ ! -f "$(ALLOCS_PPROF)" ]; then \
	  printf "❌ $(RED)找不到 $(ALLOCS_PPROF)，請先執行 'make allocs'$(RESET)\n"; exit 1; \
	fi
	@printf "✅ $(GREEN)開啟 Allocs pprof (:6062)... (Ctrl+C 退出)$(RESET)\n"
	@go tool pprof -http=localhost:6062 $(ALLOCS_PPROF)

pgo: pprof ## 編譯 PGO 優化版 (依賴最新的 CPU profile)
	@printf "$(GREEN)Building PGO-optimized binary...$(RESET)\n"
	@go build -pgo=$(CPU_PPROF) -o $(BINARY_PATH) ./cmd/run

# -----------------------------------------------------------------------------
# [測試驗證] (Testing & Verification)
# -----------------------------------------------------------------------------

## 執行單元測試 (簡要模式，顯示 ok/FAIL)
test: $(OPS_TOOL)
	@$(OPS_TOOL) test

## 執行單元測試 (全套件覆蓋率模式)
test-all: 
	@$(OPS_TOOL) test-all

## 執行單元測試 (詳細模式，顯示 Log)
test-detail: 
	@$(OPS_TOOL) test-detail

# -----------------------------------------------------------------------------
# [Docker] (容器化服務)
# -----------------------------------------------------------------------------
docker-build: ## Build docker image
	docker build -t $(DOCKER_IMAGE):$(DOCKER_TAG) .
docker-run: ## Run docker container (foreground)
	docker run --rm -p $(DOCKER_PORT):5808 $(DOCKER_IMAGE):$(DOCKER_TAG)

docker-sh: ## 進容器看一下（debug 用）
	docker run --rm -it --entrypoint /bin/sh $(DOCKER_IMAGE):$(DOCKER_TAG)

docker-clean: ## 刪掉這個 image
	docker rmi $(DOCKER_IMAGE):$(DOCKER_TAG) || true

docker-prune: ## 全面清理沒用到的 image / build cache（小心用）
	docker system prune -f

# -----------------------------------------------------------------------------
# [開發模式參數] (允許 make dev <GameName> 形式不觸發錯誤)
# -----------------------------------------------------------------------------
ifeq ($(firstword $(MAKECMDGOALS)),dev)
ifneq ($(strip $(DEV_ARG)),)
ifeq ($(words $(MAKECMDGOALS)),2)
.PHONY: $(DEV_ARG)
$(DEV_ARG):
	@:
endif
endif
endif

# -----------------------------------------------------------------------------
# [說明文件] (Documentation)
# -----------------------------------------------------------------------------

help: ## 顯示此說明清單 (簡寫: h)
	@echo ""
	@echo "$(GREEN)Problab$(RESET)"
	@echo ""
	@echo "Usage:  make $(BLUE)<target>$(RESET) [ARGS...]"
	@echo ""
	@echo "Arguments (Long / Short):"
	@printf "  $(BLUE)%-13s$(RESET) = %-12s (%s)\n" "game    / g" "$(GAME_E)" "Target game name"
	@printf "  $(BLUE)%-13s$(RESET) = %-12s (%s)\n" "worker  / w" "$(WORKER_E)" "Number of parallel workers"
	@printf "  $(BLUE)%-13s$(RESET) = %-12s (%s)\n" "player  / p" "$(PLAYERS_E)" "Number of simulated players"
	@printf "  $(BLUE)%-13s$(RESET) = %-12s (%s)\n" "rounds  / r" "$(ROUNDS_E)" "Spins per worker/player"
	@printf "  $(BLUE)%-13s$(RESET) = %-12s (%s)\n" "bets    / b" "$(BETS_E)" "Initial balance in bets"
	@printf "  $(BLUE)%-13s$(RESET) = %-12s (%s)\n" "betmode / m" "$(BETMODE_E)" "Bet mode index"
	@echo ""
	@echo "Docker Arguments:"
	@printf "  $(BLUE)%-13s$(RESET) = %-12s (%s)\n" "DOCKER_IMAGE" "$(DOCKER_IMAGE)" "Docker image name"
	@printf "  $(BLUE)%-13s$(RESET) = %-12s (%s)\n" "DOCKER_TAG" "$(DOCKER_TAG)" "Docker image tag"
	@printf "  $(BLUE)%-13s$(RESET) = %-12s (%s)\n" "DOCKER_PORT" "$(DOCKER_PORT)" "Docker host port mapping"
	@echo ""
	@echo "Targets:"
	@echo "  $(GREEN)[Basic Operations]$(RESET)"
	@printf "    $(BLUE)%-12s$(RESET)  %s\n" "build" "Build standard binary to $(BINARY_PATH)"
	@printf "    $(BLUE)%-12s$(RESET)  %s\n" "run" "Run simulation using 'go run'"
	@printf "    $(BLUE)%-12s$(RESET)  %s\n" "dev" "Open Dev Web Panel at http://localhost:5808/dev"
	@printf "    $(BLUE)%-12s$(RESET)  %s\n" "svr" "Start HTTP server using 'go run ./cmd/svr'"
	@printf "    $(BLUE)%-12s$(RESET)  %s\n" "bin" "Run compiled binary (faster startup)"
	@printf "    $(BLUE)%-12s$(RESET)  %s\n" "clean" "Remove build artifacts and cache"
	@echo ""
	@echo "  $(GREEN)[Profiling & Optimization]$(RESET)"
	@printf "    $(BLUE)%-12s$(RESET)  %s\n" "pprof" "Run simulation with CPU profiling"
	@printf "    $(BLUE)%-12s$(RESET)  %s\n" "heap" "Run simulation with Heap profiling"
	@printf "    $(BLUE)%-12s$(RESET)  %s\n" "allocs" "Run simulation with Allocations profiling"
	@printf "    $(BLUE)%-12s$(RESET)  %s\n" "read-pprof" "Visualize CPU profile (:6060)"
	@printf "    $(BLUE)%-12s$(RESET)  %s\n" "read-heap" "Visualize Heap profile (:6061)"
	@printf "    $(BLUE)%-12s$(RESET)  %s\n" "read-allocs" "Visualize Allocs profile (:6062)"
	@printf "    $(BLUE)%-12s$(RESET)  %s\n" "pgo" "Build PGO-optimized binary"
	@echo ""
	@echo "  $(GREEN)[Testing & Verification]$(RESET)"
	@printf "    $(BLUE)%-12s$(RESET)  %s\n" "test" "Run unit tests (short summary)"
	@printf "    $(BLUE)%-12s$(RESET)  %s\n" "test-all" "Run all tests with coverage"
	@printf "    $(BLUE)%-12s$(RESET)  %s\n" "test-detail" "Run tests with verbose output"
	@echo ""
	@echo "  $(GREEN)[Docker]$(RESET)"
	@printf "    $(BLUE)%-12s$(RESET)  %s\n" "docker-build" "Build docker image"
	@printf "    $(BLUE)%-12s$(RESET)  %s\n" "docker-run" "Run docker container (foreground)"
	@printf "    $(BLUE)%-12s$(RESET)  %s\n" "docker-sh" "Run shell inside docker container"
	@printf "    $(BLUE)%-12s$(RESET)  %s\n" "docker-clean" "Remove docker image"
	@printf "    $(BLUE)%-12s$(RESET)  %s\n" "docker-prune" "Clean unused images and build cache"

h: help
