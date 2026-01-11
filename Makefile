# Makefile for problab project

# -----------------------------------------------------------------------------
# Project Variables
# -----------------------------------------------------------------------------
PROFILING_DIR = build/profiling
BIN_DIR       = build/bin
BINARY_NAME   = run
BINARY_PATH   = $(BIN_DIR)/$(BINARY_NAME)

# PProf
CPU_PPROF    = $(PROFILING_DIR)/cpu.pprof
HEAP_PPROF   = $(PROFILING_DIR)/heap.pprof
ALLOCS_PPROF = $(PROFILING_DIR)/allocs.pprof

# flag alias
g  ?=
w  ?=
p  ?=
b  ?=
m  ?=
r  ?=
s  ?=

# default flag value
game     ?= 0
worker   ?= 1
players  ?= 1
bets     ?= 200
betmode  ?= 0
rounds   ?= 10000000
seed     ?= 2305843009213693951

# alias
GAME_E    := $(if $(g),$(g),$(game))
WORKER_E  := $(if $(w),$(w),$(worker))
PLAYERS_E := $(if $(p),$(p),$(players))
BETS_E    := $(if $(b),$(b),$(bets))
BETMODE_E := $(if $(m),$(m),$(betmode))
ROUNDS_E  := $(if $(r),$(r),$(rounds))
SEED_E    := $(if $(s),$(s),$(seed))


# combine args
RUN_ARGS = -game $(GAME_E) -worker $(WORKER_E) -player $(PLAYERS_E) -bets $(BETS_E) -mode $(BETMODE_E) -spins $(ROUNDS_E) -seed $(SEED_E)

# pprof args: Go flag: var ProfileType = flag.String("p", ...))
PPROF_CPU_ARGS    = -p=cpu    $(RUN_ARGS)
PPROF_HEAP_ARGS   = -p=heap   $(RUN_ARGS)
PPROF_ALLOCS_ARGS = -p=allocs $(RUN_ARGS)

# docker
DOCKER_IMAGE ?= probsvr
DOCKER_TAG   ?= latest
DOCKER_PORT  ?= 5808

# colorful
GREEN = \033[1;32m
BLUE = \033[36m
RED = \033[1;31m
RESET = \033[0m

# os define
ifeq ($(OS),Windows_NT)
    EXT = .exe
else
    EXT =
endif

# scripts tool path
OPS_TOOL = ./scripts/bin/scripts$(EXT)
OPS_SRC = $(wildcard scripts/*.go)

# -----------------------------------------------------------------------------
# .PHONY
# -----------------------------------------------------------------------------
.PHONY: all build run bin clean help h svr dev
.PHONY: pprof read-pprof heap read-heap allocs read-allocs pgo
.PHONY: test test-all test-detail
.PHONY: docker-build docker-run docker-sh docker-clean docker-prune

# default: help
all: help

# if bin/ops not exist or scripts/ops.go newer than bin/ops
$(OPS_TOOL): $(OPS_SRC)
	@echo "$(BLUE)Compiling ops tool...$(RESET)"
	@go build -o $(OPS_TOOL) ./scripts

# helper
build-tool:
	@go build -o $(OPS_TOOL) ./scripts
	@echo "Ops tool rebuilt."


# -----------------------------------------------------------------------------
# Basic Operations
# -----------------------------------------------------------------------------

## go build : build/bin/run
build: 
	@printf "$(GREEN)Building standard binary...$(RESET)\n"
	@mkdir -p $(BIN_DIR)
	@go build -o $(BINARY_PATH) ./cmd/run


## go run:args (see help)
run: 
	@go run ./cmd/run $(RUN_ARGS)


## boost HTTP Server（go run）
svr: 
	@printf "$(GREEN)Starting HTTP Server...$(RESET)\n"
	@go run ./cmd/svr

## execute binary file
bin:
	@printf "$(GREEN)Running compiled binary...$(RESET)\n"
	@./$(BINARY_PATH) $(RUN_ARGS)


## Dev Web Panel (/dev)
dev: 
	@go run ./cmd/dev

## Optimizer
opt:
	@go run ./cmd/opt -game $(GAME_E) -mode $(BETMODE_E)

## clean go cache & build
clean: 
	@printf "$(GREEN)Cleaning cache and build artifacts...$(RESET)\n"
	@go clean -cache
	@rm -rf $(BIN_DIR) $(PROFILING_DIR)

# -----------------------------------------------------------------------------
# Profiling & Optimization
# -----------------------------------------------------------------------------

## cpu.pprof
pprof: 
	@printf "$(GREEN)Generating CPU profile...$(RESET)\n"
	@mkdir -p $(PROFILING_DIR)
	@go run ./cmd/run $(PPROF_CPU_ARGS)

## read CPU profile (:6060)
read-pprof: 
	@if [ ! -f "$(CPU_PPROF)" ]; then \
	  printf "❌ $(RED)$(CPU_PPROF) not found. Please run 'make pprof' first.$(RESET)\n"; exit 1; \
	fi
	@printf "✅ $(GREEN)Opening CPU pprof at :6060... (Ctrl+C to quit)$(RESET)\n"
	@go tool pprof -http=localhost:6060 $(CPU_PPROF)

## Run Heap analysis (generate heap.pprof, used for memory leak detection)
heap: 
	@printf "$(GREEN)Generating Heap profile...$(RESET)\n"
	@mkdir -p $(PROFILING_DIR)
	@go run ./cmd/run $(PPROF_HEAP_ARGS)

## Read Heap profile (open browser :6061)
read-heap: 
	@if [ ! -f "$(HEAP_PPROF)" ]; then \
	  printf "❌ $(RED)$(HEAP_PPROF) not found. Please run 'make heap' first.$(RESET)\n"; exit 1; \
	fi
	@printf "✅ $(GREEN)Opening Heap pprof at :6061... (Ctrl+C to quit)$(RESET)\n"
	@go tool pprof -http=localhost:6061 $(HEAP_PPROF)

## Run Allocs analysis (generate allocs.pprof, used for total allocation detection)
allocs: 
	@printf "$(GREEN)Generating Allocs profile...$(RESET)\n"
	@mkdir -p $(PROFILING_DIR)
	@go run ./cmd/run $(PPROF_ALLOCS_ARGS)

## Read Allocs profile (open browser :6062)
read-allocs: 
	@if [ ! -f "$(ALLOCS_PPROF)" ]; then \
	  printf "❌ $(RED)$(ALLOCS_PPROF) not found. Please run 'make allocs' first.$(RESET)\n"; exit 1; \
	fi
	@printf "✅ $(GREEN)Opening Allocs pprof at :6062... (Ctrl+C to quit)$(RESET)\n"
	@go tool pprof -http=localhost:6062 $(ALLOCS_PPROF)

## Build PGO optimized binary (dependent on latest CPU profile)
pgo: pprof 
	@printf "$(GREEN)Building PGO-optimized binary...$(RESET)\n"
	@go build -pgo=$(CPU_PPROF) -o $(BINARY_PATH) ./cmd/run

# -----------------------------------------------------------------------------
# [Testing & Verification]
# -----------------------------------------------------------------------------

## Run unit tests (short summary: ok/FAIL)
test: $(OPS_TOOL)
	@$(OPS_TOOL) test

## Run unit tests (full suite with coverage)
test-all: 
	@$(OPS_TOOL) test-all

## Run unit tests (verbose output)
test-detail: 
	@$(OPS_TOOL) test-detail

# -----------------------------------------------------------------------------
# [Docker] (Containerization)
# -----------------------------------------------------------------------------

## Build docker image
docker-build: 
	docker build -f deploy/docker/Dockerfile -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

## Run docker container (foreground)
docker-run: 
	docker run --rm -p $(DOCKER_PORT):5808 $(DOCKER_IMAGE):$(DOCKER_TAG)

## Enter container (debug only)
docker-sh: 
	docker run --rm -it --entrypoint /bin/sh $(DOCKER_IMAGE):$(DOCKER_TAG)

## Remove docker image
docker-clean: 
	docker rmi $(DOCKER_IMAGE):$(DOCKER_TAG) || true

## Prune unused images / build cache (use with care)
docker-prune: 
	docker system prune -f

# -----------------------------------------------------------------------------
# [Documentation]
# -----------------------------------------------------------------------------

## Show this help message (alias: h)
help: 
	@echo ""
	@echo "$(GREEN)Problab$(RESET)"
	@echo ""
	@echo "Usage:  make $(BLUE)<target>$(RESET) [ARGS...]"
	@echo ""
	@echo "Arguments (Long / Short):"
	@printf "  $(BLUE)%-13s$(RESET) = %-20s (%s)\n" "game    / g" "$(GAME_E)" "Target game name"
	@printf "  $(BLUE)%-13s$(RESET) = %-20s (%s)\n" "worker  / w" "$(WORKER_E)" "Number of parallel workers"
	@printf "  $(BLUE)%-13s$(RESET) = %-20s (%s)\n" "player  / p" "$(PLAYERS_E)" "Number of simulated players"
	@printf "  $(BLUE)%-13s$(RESET) = %-20s (%s)\n" "rounds  / r" "$(ROUNDS_E)" "Spins per worker/player"
	@printf "  $(BLUE)%-13s$(RESET) = %-20s (%s)\n" "bets    / b" "$(BETS_E)" "Initial balance in bets"
	@printf "  $(BLUE)%-13s$(RESET) = %-20s (%s)\n" "betmode / m" "$(BETMODE_E)" "Bet mode index"
	@printf "  $(BLUE)%-13s$(RESET) = %-20s (%s)\n" "seed    / s" "$(SEED_E)" "int64 seed for RNG init"
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
