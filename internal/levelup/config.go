package levelup

import (
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
)

func ValidateConfig(c Config, allowUnset bool) error {
	if !(allowUnset && c.BotQQ == 0) && (c.BotQQ < 10000 || c.BotQQ > 999999999999) {
		return errors.New("请指定有效的机器人QQ号")
	}
	for _, id := range []*int64{c.BotProtocolID, c.PadProtocolID, c.LinuxProtocolID} {
		if id != nil && *id < 0 {
			return errors.New("协议ID必须为非负整数，0有效；自动选择使用null")
		}
	}
	if c.SessionMinutes < 1 || c.SessionMinutes > 30 {
		return errors.New("验证会话时长须在1至30分钟之间")
	}
	if c.AdminURL != "" {
		u, err := url.Parse(c.AdminURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("后台入口须为不含密码、访问码或查询参数的HTTPS地址")
		}
	}
	if err := validateTaskAliases(c.TaskAliases); err != nil {
		return err
	}
	return validateReplyTemplates(c.ReplyTemplates)
}
func findConfiguredBot(d Directory, qq int64, kind string, pid *int64) (*Bot, error) {
	if pid == nil {
		return findBot(d, qq, kind)
	}
	for i := range d.Bots {
		b := &d.Bots[i]
		if b.QQ == qq && b.Kind == kind && b.PID == *pid {
			return b, nil
		}
	}
	return nil, nil
}
func (e *Engine) CurrentConfig() Config {
	e.configGate.RLock()
	defer e.configGate.RUnlock()
	raw, _ := json.Marshal(e.Config)
	var c Config
	_ = json.Unmarshal(raw, &c)
	return c
}
func (e *Engine) SaveConfig(path string, c Config) error {
	if err := ValidateConfig(c, true); err != nil {
		return err
	}
	e.configGate.Lock()
	defer e.configGate.Unlock()
	old := e.Config
	c.DataDir = old.DataDir
	c.WebListen = old.WebListen
	if c.TaskAliases == nil {
		c.TaskAliases = old.TaskAliases
	}
	if c.ReplyTemplates == nil {
		c.ReplyTemplates = old.ReplyTemplates
	}
	changed := old.BotQQ != c.BotQQ || !reflect.DeepEqual(old.BotProtocolID, c.BotProtocolID) || !reflect.DeepEqual(old.PadProtocolID, c.PadProtocolID) || !reflect.DeepEqual(old.LinuxProtocolID, c.LinuxProtocolID)
	if changed {
		e.Sweep()
		e.mu.Lock()
		active := len(e.flows)
		e.mu.Unlock()
		if active > 0 {
			return errors.New("仍有进行中的验证会话，请先完成或取消，再切换机器人或协议")
		}
	}
	if c.BotQQ != 0 {
		d, err := e.dir()
		if err != nil {
			return errors.New("暂时无法查询框架，请等待IPC连接恢复后保存")
		}
		b, err := findConfiguredBot(d, c.BotQQ, "pad", c.BotProtocolID)
		if err != nil || b == nil || b.Status != 1 || b.NT != 1 {
			return errors.New("机器人必须是当前框架中已在线、唯一明确的NT Pad实例")
		}
		if _, err = protocol(d, "pad", c.PadProtocolID); err != nil {
			return errors.New("请选择唯一启用的Pad协议，不能使用Phone")
		}
		if _, err = protocol(d, "linux", c.LinuxProtocolID); err != nil {
			return errors.New("请选择唯一启用的Linux协议")
		}
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err = atomicFile(path, append(raw, '\n')); err != nil {
		return errors.New("配置文件保存失败，请检查插件目录写入权限")
	}
	e.Config = c
	return nil
}
