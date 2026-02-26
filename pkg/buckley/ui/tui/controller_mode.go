package tui

import (
	"fmt"
	"strings"
)

func (c *Controller) handleSettingsUpdate(settings UISettings) {
	if c == nil || c.store == nil {
		return
	}
	settings = settings.Normalized()
	_ = c.store.SetSetting(uiThemeSettingKey, settings.ThemeName)
	_ = c.store.SetSetting(uiStylesheetSettingKey, settings.StylesheetPath)
	_ = c.store.SetSetting(uiMetadataSettingKey, settings.MessageMetadata)
	_ = c.store.SetSetting(uiHighContrastSettingKey, boolSettingValue(settings.HighContrast))
	_ = c.store.SetSetting(uiReduceMotionSettingKey, boolSettingValue(settings.ReduceMotion))
	_ = c.store.SetSetting(uiEffectsSettingKey, boolSettingValue(settings.EffectsEnabled))
}

func (c *Controller) handleModeCommand(mode string) error {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return fmt.Errorf("mode required")
	}
	if c.strategyFactory == nil {
		return fmt.Errorf("execution strategy factory unavailable")
	}
	strategy, err := c.strategyFactory.Create(mode)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.execStrategy = strategy
	c.mu.Unlock()
	if strategy != nil {
		c.app.SetExecutionMode(strategy.Name())
		if c.cfg != nil {
			c.cfg.Execution.Mode = strategy.Name()
		}
	}
	attachStrategyUIHooks(strategy, c.progressMgr, c.toastMgr)
	c.app.AddMessage(fmt.Sprintf("Switched to %s mode", strategy.Name()), "system")
	return nil
}
