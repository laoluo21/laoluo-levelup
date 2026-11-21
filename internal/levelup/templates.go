package levelup

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

type replyDefinition struct {
	id, group, name, text string
}

type replyTemplate struct {
	ID      string            `json:"id"`
	Group   string            `json:"group"`
	Name    string            `json:"name"`
	Default string            `json:"default"`
	Tokens  map[string]string `json:"tokens"`
}

var fixedCommands = map[string]bool{
	"等级菜单": true, "开始登录": true, "立即补挂": true, "项目开关": true,
	"电脑登录": true, "继续验证": true, "取消操作": true, "身份手机号": true,
	"接收短信": true, "发送短信": true, "安全扫码": true, "已发送": true, "查询二维码": true,
}

var tokenPattern = regexp.MustCompile(`\{\{([^{}\n]+)\}\}`)

var previewTokenValues = map[string]string{
	"项目列表": "日签打卡-开启✅\n电脑在线-关闭❌", "项目示例": "日签打卡",
	"项目名称": "日签打卡", "项目状态": "开启", "提交数量": "2", "补挂项目": "日签打卡\n会员签到",
	"电脑上线提示": "电脑上线请发送“电脑登录”。", "账号类型": "Pad",
	"可用验证指令": "接收短信 | 安全扫码", "后台地址": "\n后台地址：https://mengka.example.com",
	"账号状态": "当前没有插件验证会话。\nPad已在线。", "短信接收号码": "10690000",
	"短信内容": "示例短信内容", "当前等级": "20", "所需等级": "32",
}

var replyCatalog, repliesByID = buildReplyCatalog()

func buildReplyCatalog() ([]replyTemplate, map[string]replyTemplate) {
	catalog := make([]replyTemplate, 0, len(replyDefinitions))
	byID := make(map[string]replyTemplate, len(replyDefinitions))
	for _, def := range replyDefinitions {
		entry := replyTemplate{ID: def.id, Group: def.group, Name: def.name, Default: def.text, Tokens: map[string]string{}}
		for _, token := range tokenPattern.FindAllStringSubmatch(def.text, -1) {
			name := token[1]
			if fixedCommands[name] {
				entry.Tokens[name] = name
			} else {
				entry.Tokens[name] = previewTokenValues[name]
			}
		}
		catalog = append(catalog, entry)
		byID[def.id] = entry
	}
	return catalog, byID
}

func validateReplyTemplates(templates map[string]string) error {
	for id, text := range templates {
		entry, ok := repliesByID[id]
		if !ok {
			return fmt.Errorf("未知指令模板：%s", id)
		}
		if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > 800 {
			return fmt.Errorf("「%s」须填写 1–800 字，或恢复默认", entry.Name)
		}
		seen := map[string]int{}
		for _, token := range tokenPattern.FindAllStringSubmatch(text, -1) {
			if _, ok := entry.Tokens[token[1]]; !ok {
				return fmt.Errorf("「%s」有未知占位符 %s", entry.Name, token[0])
			}
			seen[token[1]]++
		}
		rest := tokenPattern.ReplaceAllString(text, "")
		if strings.Contains(rest, "{{") || strings.Contains(rest, "}}") {
			return fmt.Errorf("「%s」有不完整的占位符", entry.Name)
		}
		for name := range entry.Tokens {
			if seen[name] == 0 || id == "menu" && seen[name] != 1 {
				return fmt.Errorf("「%s」需要保留 {{%s}}", entry.Name, name)
			}
		}
	}
	return nil
}

func renderReply(overrides map[string]string, id string, values ...string) string {
	entry := repliesByID[id]
	text := entry.Default
	if value := overrides[id]; value != "" {
		text = value
	}
	dynamic := make(map[string]string, len(values)/2)
	for i := 0; i+1 < len(values); i += 2 {
		dynamic[values[i]] = values[i+1]
	}
	return tokenPattern.ReplaceAllStringFunc(text, func(marker string) string {
		name := marker[2 : len(marker)-2]
		if fixedCommands[name] {
			return name
		}
		return dynamic[name]
	})
}

func (e *Engine) replyText(id string, values ...string) string {
	return renderReply(e.Config.ReplyTemplates, id, values...)
}

func (e *Engine) reply(qq int64, id string, values ...string) error {
	return e.send(qq, e.replyText(id, values...))
}

func (e *Engine) replyError(qq int64, err error) error {
	id, values := errorReply(err)
	return e.reply(qq, id, values...)
}

func validateTaskAliases(aliases map[string]string) error {
	if len(aliases) > 200 {
		return errors.New("最多可保存 200 条项目简称")
	}
	used := make(map[string]bool, len(aliases))
	for name, alias := range aliases {
		if name != strings.TrimSpace(name) || name == "" || utf8.RuneCountInString(name) > 120 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
			return errors.New("官方项目名须填写完整，最多 120 字")
		}
		if alias != strings.TrimSpace(alias) || alias == "" || utf8.RuneCountInString(alias) > 24 ||
			strings.IndexFunc(alias, unicode.IsControl) >= 0 || known(alias) {
			return fmt.Errorf("「%s」的简称须为 1–24 字，且不能使用固定指令", name)
		}
		_, isOfficial := aliases[alias]
		if used[alias] || isOfficial && alias != name {
			return fmt.Errorf("项目简称「%s」重复或与其他项目原名冲突", alias)
		}
		used[alias] = true
	}
	return nil
}

func (e *Engine) taskLabel(name string, supported []string) string {
	alias := e.Config.TaskAliases[name]
	if alias == "" || alias != name && includes(supported, alias) {
		return name
	}
	return alias
}

func (e *Engine) taskName(label string, supported []string) string {
	if includes(supported, label) {
		return label
	}
	for _, name := range supported {
		if e.taskLabel(name, supported) == label {
			return name
		}
	}
	return ""
}
