package levelup

import (
	"errors"
	"strings"
	"testing"
)

func TestProjectAliasUsedInChatOnly(t *testing.T) {
	e, api, _ := fixture(t)
	api.accounts = append(api.accounts, account(user, 1, 2, 1, "pad"))
	const official = "去日签卡打一次卡"
	const alias = "日签打卡"
	api.supported = []any{official, "电脑QQ在线"}
	api.taskRows = []any{Object{"title": official, "is_done": false}}
	e.Config.TaskAliases = map[string]string{official: alias, "其他账号的项目": "未开放项目"}
	e.Config.ReplyTemplates = map[string]string{
		"project_row":    "{{项目状态}} · {{项目名称}}",
		"project_toggle": "已{{项目状态}}「{{项目名称}}」",
	}
	e.Handle(event(user, 1, "项目开关"))
	if got := message(api); !strings.Contains(got, "关闭❌ · 日签打卡") ||
		!strings.Contains(got, "日签打卡 开启") || strings.Contains(got, official) {
		t.Fatalf("unexpected project list: %q", got)
	}
	for _, command := range []string{alias + " 开启", official + " 关闭", alias + " 开启"} {
		if err := e.handle(user, command, command); err != nil {
			t.Fatal(err)
		}
		got := message(api)
		if !strings.Contains(got, "「"+alias+"」") || strings.Contains(got, official) {
			t.Fatalf("unexpected toggle reply: %q", got)
		}
	}
	if tasks := stringsList(last(api, "level_task_management")["tasks"]); len(tasks) != 1 || tasks[0] != official {
		t.Fatalf("toggle must submit official name, got %v", tasks)
	}
	if err := e.repair(user); err != nil {
		t.Fatal(err)
	}
	if tasks := stringsList(last(api, "execute_level_tasks")["tasks"]); len(tasks) != 1 || tasks[0] != official {
		t.Fatalf("repair must submit official name, got %v", tasks)
	}
	if got := message(api); !strings.Contains(got, alias) || strings.Contains(got, official) {
		t.Fatalf("unexpected repair reply: %q", got)
	}
	statusCalls := count(api, "level_task_management")
	if err := e.handle(user, "未开放项目 开启", "未开放项目 开启"); err != nil {
		t.Fatal(err)
	}
	if count(api, "level_task_management") != statusCalls+1 || !strings.Contains(message(api), "不匹配") {
		t.Fatal("an alias enabled a task unsupported by this account")
	}
	api.supported = append(api.supported, alias)
	supported := stringsList(api.supported)
	if e.taskName(alias, supported) != alias || e.taskLabel(official, supported) != official {
		t.Fatal("a newly added official name should take precedence over an older alias")
	}
}

func TestReplyTemplatesValidateMenuAndRenderErrors(t *testing.T) {
	for _, entry := range replyCatalog {
		if err := validateReplyTemplates(map[string]string{entry.ID: entry.Default}); err != nil {
			t.Errorf("invalid built-in template %s: %v", entry.ID, err)
		}
		for name, sample := range entry.Tokens {
			if sample == "" {
				t.Errorf("%s has no sample for %s", entry.ID, name)
			}
		}
	}
	menu := repliesByID["menu"].Default
	for _, invalid := range []string{
		strings.Replace(menu, "{{开始登录}}", "开始登录", 1),
		menu + "{{未知}}",
		menu + "{{开始登录}}",
		menu + "{{",
	} {
		if err := validateReplyTemplates(map[string]string{"menu": invalid}); err == nil {
			t.Errorf("accepted invalid menu: %q", invalid)
		}
	}
	e, api, _ := fixture(t)
	const customMenu = "你好！\n{{取消操作}}、{{继续验证}}、{{项目开关}}\n{{开始登录}}、{{电脑登录}}、{{立即补挂}}"
	e.Config.ReplyTemplates = map[string]string{
		"menu":          customMenu,
		"error_level":   "请先升到 {{所需等级}} 级（当前 {{当前等级}} 级）。",
		"error_generic": "请稍后再试。",
	}
	if err := validateReplyTemplates(e.Config.ReplyTemplates); err != nil {
		t.Fatal(err)
	}
	e.Handle(event(user, 1, "等级菜单"))
	if got := message(api); got != "你好！\n取消操作、继续验证、项目开关\n开始登录、电脑登录、立即补挂" {
		t.Fatalf("unexpected menu: %q", got)
	}
	if err := e.replyError(user, &RemoteError{Code: "QQ_LEVEL_TOO_LOW", RequiredLevel: 32, CurrentLevel: 20}); err != nil {
		t.Fatal(err)
	}
	if got := message(api); got != "请先升到 32 级（当前 20 级）。" {
		t.Fatalf("unexpected level error: %q", got)
	}
	if err := e.replyError(user, errors.New("raw framework error with private details")); err != nil {
		t.Fatal(err)
	}
	if got := message(api); got != "请稍后再试。" {
		t.Fatalf("unexpected generic error: %q", got)
	}
	if got := renderReply(nil, "project_row", "项目名称", "{{开始登录}}", "项目状态", "开启"); got != "{{开始登录}}-开启" {
		t.Fatalf("values must not be interpreted as templates: %q", got)
	}
}

func TestValidateTaskAliasesRejectsAmbiguousInputs(t *testing.T) {
	for _, aliases := range []map[string]string{
		{"项目一": "项目", "项目二": "项目"},
		{"项目一": "项目二", "项目二": "项目"},
		{"项目一": "项目开关"},
		{"项目一": "多行\n项目"},
	} {
		if err := validateTaskAliases(aliases); err == nil {
			t.Errorf("accepted invalid aliases: %v", aliases)
		}
	}
}
