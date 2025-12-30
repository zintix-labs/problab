package catalog

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/spec"
)

var (
	ErrDupID   = errs.NewFatal("duplicate game id")
	ErrDupName = errs.NewFatal("duplicate game name")
)

type Entry struct {
	GID        spec.GID
	Name       string
	ConfigName string
}

type Summary struct {
	GID      spec.GID      `json:"gid"`
	Name     string        `json:"name"`
	Logic    spec.LogicKey `json:"logic"`
	BetUnits []int         `json:"bet_units"`
}

type Catalog struct {
	byID   map[spec.GID]Entry
	byName map[string]Entry
	ids    []spec.GID          // 用來穩定排序
	unique map[string]struct{} // 一組遊戲，檔名需唯一
	config *multiFS
	frozen bool
}

func New(cfg ...fs.FS) (*Catalog, error) {
	multFS, err := newMultiFS(cfg...)
	if err != nil {
		return nil, errs.Wrap(err, "can not create catalog")
	}
	return &Catalog{
		byID:   map[spec.GID]Entry{},
		byName: map[string]Entry{},
		ids:    make([]spec.GID, 0, 100),
		unique: map[string]struct{}{},
		config: multFS,
		frozen: false,
	}, nil
}

func (c *Catalog) Register(metas ...Entry) error {
	if c.frozen {
		return errs.NewWarn("can not register when catalog already frozen")
	}
	seenID := map[spec.GID]struct{}{}
	seenName := map[string]struct{}{}
	seenCfg := map[string]struct{}{}
	for _, meta := range metas {
		meta.Name = strings.TrimSpace(meta.Name)
		meta.Name = strings.ToLower(meta.Name)
		if meta.Name == "" {
			return errs.NewFatal("game name required")
		}
		if err := validFileName(meta.ConfigName); err != nil {
			return err
		}
		if _, ok := c.config.index[meta.ConfigName]; !ok {
			return errs.NewFatal(fmt.Sprintf("config file not found: %s", meta.ConfigName))
		}
		if _, ok := c.byID[meta.GID]; ok {
			return ErrDupID
		}
		if _, ok := c.byName[meta.Name]; ok {
			return ErrDupName
		}
		if _, ok := c.unique[meta.ConfigName]; ok {
			return errs.NewFatal(fmt.Sprintf("duplicate config name: %s", meta.ConfigName))
		}
		if _, ok := seenID[meta.GID]; ok {
			return ErrDupID
		}
		if _, ok := seenName[meta.Name]; ok {
			return ErrDupName
		}
		if _, ok := seenCfg[meta.ConfigName]; ok {
			return errs.NewFatal(fmt.Sprintf("duplicate config name: %s", meta.ConfigName))
		}
		seenID[meta.GID] = struct{}{}
		seenName[meta.Name] = struct{}{}
		seenCfg[meta.ConfigName] = struct{}{}
	}
	for _, meta := range metas {
		c.unique[meta.ConfigName] = struct{}{}
		c.byID[meta.GID] = meta
		c.byName[meta.Name] = meta
		c.ids = append(c.ids, meta.GID)
	}
	sort.Slice(c.ids, func(i, j int) bool { return c.ids[i] < c.ids[j] })
	return nil
}

func (c *Catalog) GetByID(id spec.GID) (Entry, bool) {
	m, ok := c.byID[id]
	return m, ok
}

func (c *Catalog) GetByName(name string) (Entry, bool) {
	name = strings.TrimSpace(name)
	name = strings.ToLower(name)
	m, ok := c.byName[name]
	return m, ok
}

func (c *Catalog) IDs() []spec.GID {
	if len(c.ids) == 0 {
		return nil
	}
	return append([]spec.GID(nil), c.ids...)
}

func (c *Catalog) All() []Entry {
	order := c.IDs()
	m := make([]Entry, 0, len(c.ids))
	for _, id := range order {
		if meta, ok := c.GetByID(id); ok {
			m = append(m, meta)
		}
	}
	return m
}

func (c *Catalog) Cfg() *multiFS {
	return c.config
}

func (c *Catalog) Freeze() {
	c.frozen = true
}

func (c *Catalog) IsFrozen() bool {
	return c.frozen
}

func validFileName(file string) error {
	if file == "" {
		return errs.NewFatal("empty config filename")
	}
	// 1) 不能包含路徑或類似字元
	if strings.ContainsAny(file, `/\:`) {
		return errs.NewFatal(fmt.Sprintf("invalid config filename: %q (must be a basename; no / \\\\ :) ", file))
	}
	// 2) 必須以 .yaml/.yml/.json 結尾（大小寫不敏感）
	lower := strings.ToLower(file)
	if !(strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".json")) {
		return errs.NewFatal(fmt.Sprintf("invalid config filename: %q (must end with .yaml, .yml, or .json)", file))
	}
	// 3) 不能以 . 開頭（防止直接 .yaml / .yml）
	if strings.HasPrefix(file, ".") {
		return errs.NewFatal(fmt.Sprintf("invalid config filename: %q (cannot start with '.')", file))
	}
	return nil
}

func parseGameSettingByExt(filename string, raw []byte) (*spec.GameSetting, error) {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".yaml", ".yml":
		return spec.GetGameSettingByYAML(raw)
	case ".json":
		return spec.GetGameSettingByJSON(raw)
	default:
		return nil, errs.NewFatal(fmt.Sprintf("unsupported config format: %q", filename))
	}
}

// GameSettingById
//
// 會讀取 fs.FS 中的 YAML/JSON 設定、初始化各子設定並執行基本檢查後回傳
func (c *Catalog) GameSettingById(id spec.GID) (*spec.GameSetting, error) {
	e, ok := c.GetByID(id)
	if !ok {
		return nil, errs.NewWarn("id dose not exist in catalog")
	}
	src, ok := c.config.GetFS(e.ConfigName)
	if !ok {
		return nil, errs.NewWarn("file name dose not exist in catalog")
	}
	raw, err := fs.ReadFile(src, e.ConfigName)
	if err != nil {
		return nil, errs.Wrap(err, "catalog parse file error")
	}
	return parseGameSettingByExt(e.ConfigName, raw)
}

// GameSettingByName
//
// 會讀取fs中的 YAML/JSON 設定、初始化各子設定並執行基本檢查後回傳
func (c *Catalog) GameSettingByName(name string) (*spec.GameSetting, error) {
	e, ok := c.GetByName(name)
	if !ok {
		return nil, errs.NewWarn("name dose not exist in catalog")
	}
	src, ok := c.config.GetFS(e.ConfigName)
	if !ok {
		return nil, errs.NewWarn("file name dose not exist in catalog")
	}
	raw, err := fs.ReadFile(src, e.ConfigName)
	if err != nil {
		return nil, errs.Wrap(err, "catalog parse file error")
	}
	return parseGameSettingByExt(e.ConfigName, raw)
}

type multiFS struct {
	src   []fs.FS
	index map[string]int // name -> src index
}

func newMultiFS(src ...fs.FS) (*multiFS, error) {
	if len(src) == 0 {
		return nil, errs.NewFatal("no fs provided")
	}
	for i, s := range src {
		if s == nil {
			return nil, errs.NewFatal(fmt.Sprintf("fs[%d] is nil", i))
		}
	}

	m := &multiFS{
		src:   src,
		index: make(map[string]int, 256),
	}

	// eager validate: build index and detect duplicates
	for i := 0; i < len(src); i++ {
		err := fs.WalkDir(src[i], ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// Problab configs are intentionally required to be a *flat* directory.
				// Only the root "." is allowed. Any subdirectory presence is a contract violation,
				// even if it contains no yaml/json files.
				if path == "." {
					return nil
				}
				return errs.NewFatal(fmt.Sprintf("config FS must be flat (no subdirectories): %q", path))
			}

			// WalkDir may return paths like "demo/game_0.yaml" when subdirectories exist.
			// Even though we already reject subdirectories above, keep this check as a defensive guard.
			if strings.Contains(path, "/") {
				return errs.NewFatal(fmt.Sprintf("config FS must be flat (no subdirectories): %q", path))
			}

			// Only index yaml/json configs; ignore any other assets that may exist in the FS.
			lower := strings.ToLower(path)
			if !(strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".json")) {
				return nil
			}

			name := path // flat FS guarantees path is a basename

			if prev, ok := m.index[name]; ok {
				// duplicate across FS: fail fast
				return errs.NewFatal(fmt.Sprintf("duplicate config %q in fs[%d] and fs[%d]", name, prev, i))
			}
			m.index[name] = i
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *multiFS) GetFS(name string) (fs.FS, bool) {
	if id, ok := m.index[name]; ok {
		return m.src[id], ok
	}
	return nil, false
}

// Sources exposes config FS sources for read-only iteration.
func (m *multiFS) Sources() []fs.FS {
	if m == nil || len(m.src) == 0 {
		return nil
	}
	return append([]fs.FS(nil), m.src...)
}
