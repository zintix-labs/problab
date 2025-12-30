package spec

// GameModeSetting 將單一遊戲模式（如主遊戲、免費遊戲等）所需設定統整在一起。
type GameModeSetting struct {
	ScreenSetting    ScreenSetting    `yaml:"screen_setting"     json:"screen_setting"`
	GenScreenSetting GenScreenSetting `yaml:"gen_screen_setting" json:"gen_screen_setting"`
	SymbolSetting    SymbolSetting    `yaml:"symbol_setting"     json:"symbol_setting"`
	HitSetting       HitSetting       `yaml:"hit_setting"        json:"hit_setting"`
}

func (gms *GameModeSetting) init() error {
	if err := gms.ScreenSetting.Init(); err != nil {
		return err
	}
	if err := gms.GenScreenSetting.Init(); err != nil {
		return err
	}
	if err := gms.SymbolSetting.Init(); err != nil {
		return err
	}
	if err := gms.HitSetting.Init(); err != nil {
		return err
	}
	return nil
}
