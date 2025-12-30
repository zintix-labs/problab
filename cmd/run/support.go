package main

import (
	"crypto/rand"
	"flag"
	"fmt"
	"log"
	"math"
	"math/big"
	"strconv"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/spec"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

var cfg *config = new(config)

type config struct {
	name      string
	id        spec.GID
	worker    int
	player    int
	bets      int
	spins     int
	betMode   int
	seed      int64
	pprofmode string
}

type gidFlag struct{ p *spec.GID }

func (f gidFlag) String() string { return fmt.Sprint(uint(*f.p)) }
func (f gidFlag) Set(s string) error {
	u, err := strconv.ParseUint(s, 10, 0)
	if err != nil {
		return err
	}
	*f.p = spec.GID(uint(u))
	return nil
}

func bindVar() {
	// 綁定 Flag 到本地變數的指標 (&)
	flag.Var(gidFlag{&cfg.id}, "game", "target game id")
	flag.IntVar(&cfg.worker, "worker", 1, "number of workers")
	flag.IntVar(&cfg.player, "player", 1, "number of players")
	flag.IntVar(&cfg.bets, "bets", 200, "initial bets")
	flag.IntVar(&cfg.spins, "spins", 10000000, "spins per player")
	flag.IntVar(&cfg.betMode, "mode", 0, "bet mode index")
	flag.Int64Var(&cfg.seed, "seed", -1, "int64 seed for random number generator")
	flag.StringVar(&cfg.pprofmode, "p", "", "pprof: '', cpu, heap, allocs")

	flag.Parse()

	// given seed illeagel -> default seed
	if cfg.seed < 1 {
		seed, err := rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
		if err != nil {
			log.Fatal(err)
		}
		cfg.seed = seed.Int64()
	}
}

// 這裡解析並分支要執行的模擬器
func executeSimulator() { // 取得spin數
	cfg.valid() // 基本檢查

	lab, err := problab.NewAuto(
		core.NewDefault(),
		problab.Configs(demo_configs.FS),
		problab.Logics(demo_logic.Reg),
	)
	if err != nil {
		log.Fatal(err)
	}
	s, err := lab.NewSimulatorWithSeed(cfg.id, cfg.seed)
	if err != nil {
		log.Fatal(err)
	}
	ent, _ := lab.EntryById(cfg.id)
	cfg.name = ent.Name
	// 至此確保可執行
	green := "\033[1;32m"
	reset := "\033[0m"
	p := message.NewPrinter(language.English)

	if cfg.player == 1 { // 純機台模擬
		if cfg.worker == 1 { // 單線程
			p.Printf("%s[GAME:%s] [PLAYMODE:%d] [SPINS:%d]%s\n", green, cfg.name, cfg.betMode, cfg.spins, reset)
			st, used, _ := s.Sim(cfg.betMode, cfg.spins, true)
			st.StdOut(used)
		} else {
			p.Printf("%s[WORKERS:%d] [GAME:%s] [PLAYMODE:%d] [SPINS:%d]%s\n", green, cfg.worker, cfg.name, cfg.betMode, cfg.worker*cfg.spins, reset)
			st, used, _ := s.SimMP(cfg.betMode, cfg.spins, cfg.worker, true) // 併發
			st.StdOut(used)
		}
	} else { // 模擬多玩家體驗
		p.Printf("%s[WORKERS:%d] [GAME:%s] [PLAYERS:%d BALANCE:%d PLAYMODE:%d SPINS:%d]%s\n", green, cfg.worker, cfg.name, cfg.player, cfg.bets, cfg.betMode, cfg.spins, reset)
		st, est, used, _ := s.SimPlayers(cfg.worker, cfg.player, cfg.bets, cfg.betMode, cfg.spins, true)
		st.StdOut(used)
		est.Out()
	}
}

func (cfg *config) valid() {
	p := message.NewPrinter(language.English)

	// 工作協程檢查(併發數)
	if cfg.worker < 1 {
		log.Fatal("value err : workers must > 0")
	}

	// 玩家檢查
	// 玩家數量 > 0
	if cfg.player < 1 {
		log.Fatal("value err : player must > 0")
	}
	// 玩家數量太多 resize
	if cfg.player > 100000 {
		p.Printf("too much players: %d resized to 100k players\n", cfg.player)
		cfg.player = 100000
	}

	// 模擬玩家行為的時候，玩家帶入資金不能<1
	if cfg.player > 1 && cfg.bets < 1 {
		log.Fatal("value err : balance must >= 1")
	}

	// 轉數檢查
	if cfg.spins < 1 {
		log.Fatal("value err : spins must > 0")
	}

	// 模擬玩家的時候，每個玩家最高不超過15000轉(無意義)
	// 對一個玩家來說 1500轉約1hr 15000轉約10小時 體驗已經轉為長期，直接模擬長局數機台即可
	if cfg.player > 1 && cfg.spins > 15000 {
		p.Printf("too much spins for each players : %d resized to 15k spins for each player\n", cfg.spins)
		cfg.spins = 15000
	}
}
