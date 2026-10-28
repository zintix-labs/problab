// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
	"github.com/zintix-labs/problab/demo"
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
	choose    string
	mode      problab.ChooseMode // 由 choose 解析
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
	flag.StringVar(&cfg.choose, "choose", "", "choose mode for choice games: first, last")

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

	lab, err := demo.NewProbLab()
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = lab.Close() }()
	s, err := lab.NewSimulator(cfg.id, problab.MachineConfig{Seed: problab.SeedInt64(cfg.seed)})
	if err != nil {
		log.Fatal(err)
	}
	if err := s.SetCfg(problab.BetConfig{Choose: cfg.mode}); err != nil {
		log.Fatal(err)
	}
	ent, _ := lab.EntryById(cfg.id)
	cfg.name = ent.Name
	// 至此確保可執行
	green := "\033[1;32m"
	reset := "\033[0m"
	p := message.NewPrinter(language.English)
	choose := ""
	if cfg.choose != "" {
		choose = " [CHOOSE:" + cfg.choose + "]"
	}

	if cfg.player == 1 { // 純機台模擬
		if cfg.worker == 1 { // 單線程
			_, _ = p.Printf("%s[GAME:%s] [PLAYMODE:%d] [SPINS:%d]%s%s\n", green, cfg.name, cfg.betMode, cfg.spins, choose, reset)
			st, used, err := s.Sim(cfg.betMode, cfg.spins, true)
			if err != nil {
				log.Fatal(err)
			}
			st.StdOut(used)
		} else {
			_, _ = p.Printf("%s[WORKERS:%d] [GAME:%s] [PLAYMODE:%d] [SPINS:%d]%s%s\n", green, cfg.worker, cfg.name, cfg.betMode, cfg.worker*cfg.spins, choose, reset)
			st, used, err := s.SimMP(cfg.betMode, cfg.spins, cfg.worker, true) // 併發
			if err != nil {
				log.Fatal(err)
			}
			st.StdOut(used)
		}
	} else { // 模擬多玩家體驗
		_, _ = p.Printf("%s[WORKERS:%d] [GAME:%s] [PLAYERS:%d BALANCE:%d PLAYMODE:%d SPINS:%d]%s%s\n", green, cfg.worker, cfg.name, cfg.player, cfg.bets, cfg.betMode, cfg.spins, choose, reset)
		st, est, used, err := s.SimPlayers(cfg.worker, cfg.player, cfg.bets, cfg.betMode, cfg.spins, true)
		if err != nil {
			log.Fatal(err)
		}
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
		_, _ = p.Printf("too much players: %d resized to 100k players\n", cfg.player)
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

	// 選擇方式：省略時不指定，只接受 first、last
	mode, err := problab.ParseChooseMode(cfg.choose)
	if err != nil {
		log.Fatal("value err : choose must be first or last")
	}
	cfg.mode = mode

	// 模擬玩家的時候，每個玩家最高不超過15000轉(無意義)
	// 對一個玩家來說 15000轉 應該超過10HRs 體驗已經轉為長期，直接模擬長局數機台即可
	if cfg.player > 1 && cfg.spins > 15000 {
		_, _ = p.Printf("too much spins for each players : %d resized to 15k spins for each player\n", cfg.spins)
		cfg.spins = 15000
	}
}
