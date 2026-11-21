package levelup

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type webFixture struct {
	a               *AdminWeb
	e               *Engine
	api             *fakeAPI
	server          *httptest.Server
	client          *http.Client
	csrf, dir, path string
}

func webTest(t *testing.T) *webFixture {
	t.Helper()
	e, api, dir := fixture(t)
	path := filepath.Join(dir, "config.json")
	raw, _ := json.Marshal(e.Config)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	a, err := NewAdminWeb(e, path, dir)
	if err != nil {
		t.Fatal(err)
	}
	a.SetReady(true, "框架已连接")
	server := httptest.NewServer(a)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	t.Cleanup(server.Close)
	return &webFixture{a: a, e: e, api: api, server: server, client: client, dir: dir, path: path}
}
func (w *webFixture) request(t *testing.T, path string, data any, csrf, origin string) (int, Object) {
	t.Helper()
	method := "GET"
	var body io.Reader
	if data != nil {
		method = "POST"
		raw, _ := json.Marshal(data)
		body = bytes.NewReader(raw)
	}
	r, err := http.NewRequest(method, w.server.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if data != nil {
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", w.server.URL)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	r.Header.Set("X-CSRF-Token", csrf)
	response, err := w.client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	m, err := decode(raw)
	if err != nil {
		t.Fatal(string(raw))
	}
	return response.StatusCode, m
}
func (w *webFixture) setup(t *testing.T) {
	t.Helper()
	status, r := w.request(t, "/api/setup", Object{"code": w.a.InitialCode, "password": "fixture-admin-password"}, "", "")
	if status != 200 {
		t.Fatal(status, r)
	}
	w.csrf = Str(r["csrf"])
}
func TestWebRequiresSetupSessionAndCSRF(t *testing.T) {
	w := webTest(t)
	status, _ := w.request(t, "/api/status", nil, "", "")
	if status != 401 {
		t.Fatal("unauthenticated status")
	}
	w.setup(t)
	if _, err := os.Stat(filepath.Join(w.dir, "web-first-access.txt")); !os.IsNotExist(err) {
		t.Fatal("setup secret not revoked")
	}
	raw, _ := os.ReadFile(filepath.Join(w.dir, "web-auth.json"))
	if strings.Contains(string(raw), "fixture-admin-password") {
		t.Fatal("plaintext web password saved")
	}
	status, r := w.request(t, "/api/status", nil, "", "")
	if status != 200 || Int(Obj(r["config"])["bot_qq"]) != robot {
		t.Fatal("authenticated status failed")
	}
	if _, ok := r["devices_total"]; ok {
		t.Fatal("console status still returns device inventory")
	}
	if _, ok := r["devices_free"]; ok || count(w.api, "get_device_profile_list") != 0 {
		t.Fatal("console status should not query available devices")
	}
	status, _ = w.request(t, "/api/config", w.e.Config, "", "")
	if status != 403 {
		t.Fatal("missing CSRF accepted")
	}
	status, _ = w.request(t, "/api/config", w.e.Config, w.csrf, "https://foreign.example")
	if status != 403 {
		t.Fatal("foreign origin accepted")
	}
}
func TestWebSavesHotConfigAndPreservesDataDirectory(t *testing.T) {
	w := webTest(t)
	w.setup(t)
	next := w.e.CurrentConfig()
	next.QuickLogin = false
	next.SessionMinutes = 20
	next.AdminURL = "https://example.com/admin"
	next.DataDir = "/must-not-write"
	status, r := w.request(t, "/api/config", next, w.csrf, "")
	if status != 200 || !Bool(r["ok"]) {
		t.Fatal(status, r)
	}
	if w.e.CurrentConfig().QuickLogin || w.e.CurrentConfig().SessionMinutes != 20 || w.e.CurrentConfig().DataDir == "/must-not-write" {
		t.Fatal("runtime configuration not applied")
	}
	raw, _ := os.ReadFile(w.path)
	var persisted Config
	_ = json.Unmarshal(raw, &persisted)
	if persisted.QuickLogin || persisted.SessionMinutes != 20 {
		t.Fatal("config did not persist")
	}
	if count(w.api, "add_account")+count(w.api, "update_account")+count(w.api, "offline_account") != 0 {
		t.Fatal("web settings mutated native account")
	}
}
func TestWebRejectsPhoneOfflineAndCredentialURL(t *testing.T) {
	w := webTest(t)
	w.setup(t)
	w.api.accounts = append(w.api.accounts, account(other, 0, 2, 1, "phone"))
	next := w.e.CurrentConfig()
	next.BotQQ = other
	status, _ := w.request(t, "/api/config", next, w.csrf, "")
	if status != 400 || w.e.CurrentConfig().BotQQ != robot {
		t.Fatal("phone became robot")
	}
	next = w.e.CurrentConfig()
	next.AdminURL = "https://example.com/initialize?setup=secret"
	status, _ = w.request(t, "/api/config", next, w.csrf, "")
	if status != 400 {
		t.Fatal("credential URL saved")
	}
}
func TestWebBlocksRobotChangeDuringVerificationButAllowsPreferences(t *testing.T) {
	w := webTest(t)
	w.setup(t)
	w.api.accounts = append(w.api.accounts, account(other, 1, 2, 1, "pad"))
	w.e.setFlow(user, w.e.newSession("pad", "password", 1))
	next := w.e.CurrentConfig()
	next.BotQQ = other
	status, _ := w.request(t, "/api/config", next, w.csrf, "")
	if status != 400 {
		t.Fatal("robot changed during verification")
	}
	next = w.e.CurrentConfig()
	next.QuickLogin = false
	status, _ = w.request(t, "/api/config", next, w.csrf, "")
	if status != 200 {
		t.Fatal("preference change blocked")
	}
}
func TestWebSetupIsOneTimeAndPasswordChangeRevokesOtherSession(t *testing.T) {
	w := webTest(t)
	code := w.a.InitialCode
	w.setup(t)
	status, _ := w.request(t, "/api/setup", Object{"code": code, "password": "other-fixture-password"}, "", "")
	if status != 403 {
		t.Fatal("setup reused")
	}
	otherJar, _ := cookiejar.New(nil)
	otherClient := &http.Client{Jar: otherJar, Timeout: 10 * time.Second}
	oldClient := w.client
	w.client = otherClient
	status, _ = w.request(t, "/api/login", Object{"password": "fixture-admin-password"}, "", "")
	if status != 200 {
		t.Fatal("second login")
	}
	w.client = oldClient
	status, r := w.request(t, "/api/password", Object{"old_password": "fixture-admin-password", "new_password": "new-fixture-admin-password"}, w.csrf, "")
	if status != 200 {
		t.Fatal(status, r)
	}
	w.csrf = Str(r["csrf"])
	w.client = otherClient
	status, _ = w.request(t, "/api/status", nil, "", "")
	if status != 401 {
		t.Fatal("old session retained")
	}
	w.client = oldClient
	status, _ = w.request(t, "/api/status", nil, "", "")
	if status != 200 {
		t.Fatal("current session lost")
	}
}
func TestWebLoginThrottleAndUnknownAPINotProxied(t *testing.T) {
	w := webTest(t)
	for i := 0; i < 6; i++ {
		status, _ := w.request(t, "/api/login", Object{"password": "bad"}, "", "")
		if i == 5 && status != 429 {
			t.Fatal("login not throttled")
		}
	}
	response, err := w.client.Get(w.server.URL + "/api/get_bot_list")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatal("generic API proxy exposed")
	}
	if len(w.api.calls) != 0 {
		t.Fatal("unauthenticated API touched framework")
	}
}
func TestWebDefaultsCanStartUnconfiguredAndPublishNoSecrets(t *testing.T) {
	w := webTest(t)
	cfg := w.e.CurrentConfig()
	cfg.BotQQ = 0
	if err := w.e.SaveConfig(w.path, cfg); err != nil {
		t.Fatal(err)
	}
	w.setup(t)
	status, r := w.request(t, "/api/status", nil, "", "")
	if status != 200 || Str(r["bot_state"]) != "unconfigured" {
		t.Fatal("unconfigured web not available")
	}
	raw, _ := json.Marshal(r)
	for _, forbidden := range []string{"fixture-admin-password", "SetupHash", "PasswordHash", "state.key", "qimei"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("secret published")
		}
	}
	response, err := w.client.Get(w.server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatal("frame protection missing")
	}
}
func TestWebConfigAndQQReadsAreRaceSafe(t *testing.T) {
	w := webTest(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 4; j++ {
				if i%2 == 0 {
					c := w.e.CurrentConfig()
					c.QuickLogin = j%2 == 0
					if err := w.e.SaveConfig(w.path, c); err != nil {
						t.Error(err)
					}
				} else {
					w.e.Handle(event(user, int64(100+i*10+j), "等级菜单"))
					_ = w.e.CurrentConfig()
				}
			}
		}(i)
	}
	wg.Wait()
}
func TestAdminConfiguredProtocolZeroIsPreserved(t *testing.T) {
	w := webTest(t)
	zero := int64(0)
	b := account(other, 0, 3, 1, "pad")
	w.api.accounts = append(w.api.accounts, b)
	d, _ := w.e.dir()
	bot, err := findConfiguredBot(d, other, "pad", &zero)
	if err != nil || bot == nil || bot.PID != 0 {
		t.Fatal("protocol zero lost")
	}
}

// Optional local UI preview: isolated fake APIs, never connects to Mengka or QQ.
func TestWebPreview(t *testing.T) {
	if os.Getenv("LAOLUO_UI_PREVIEW") != "1" {
		t.Skip("manual visual preview")
	}
	w := webTest(t)
	w.api.profiles = []any{}
	for id := int64(1); id <= 100; id++ {
		w.api.profiles = append(w.api.profiles, Object{"id": id})
	}
	w.api.accounts = append(w.api.accounts, account(user, 1, 2, 1, "pad"), account(user, 2, 3, 0, "linux"), account(other, 0, 4, 1, "phone"))
	info := Object{"url": w.server.URL, "code": w.a.InitialCode, "password": "preview-admin-password", "config_path": w.path}
	raw, _ := json.Marshal(info)
	if err := os.WriteFile("/tmp/laoluo-ui-preview-access.json", raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("isolated preview ready; access file /tmp/laoluo-ui-preview-access.json")
	select {}
}

func TestWebTemplateCatalogUsesOfficialStatusAndRequiresSession(t *testing.T) {
	w := webTest(t)
	input := Object{"qq": robot, "protocol_id": 1}
	for _, path := range []string{"/api/reply-templates", "/api/project-catalog"} {
		var data any
		if path == "/api/project-catalog" {
			data = input
		}
		if status, _ := w.request(t, path, data, "", ""); status != 401 {
			t.Fatalf("unauthenticated %s returned %d", path, status)
		}
	}
	w.setup(t)
	if status, r := w.request(t, "/api/reply-templates", nil, "", ""); status != 200 ||
		len(Arr(r["templates"])) != len(replyCatalog) {
		t.Fatal("unexpected reply catalog", status)
	}
	if status, _ := w.request(t, "/api/project-catalog", input, "", ""); status != 403 {
		t.Fatal("project catalog accepted missing CSRF", status)
	}
	w.api.accounts = append(w.api.accounts, account(user, 1, 2, 0, "pad"), account(other, 0, 3, 1, "phone"))
	for _, invalid := range []Object{{"qq": robot}, {"qq": user, "protocol_id": 1}, {"qq": other, "protocol_id": 0}} {
		if status, _ := w.request(t, "/api/project-catalog", invalid, w.csrf, ""); status != 400 {
			t.Fatal("invalid catalog account accepted", status)
		}
	}
	if count(w.api, "level_task_management") != 0 {
		t.Fatal("invalid catalog requests reached the task API")
	}
	before, err := os.ReadFile(w.path)
	if err != nil {
		t.Fatal(err)
	}
	status, r := w.request(t, "/api/project-catalog", input, w.csrf, "")
	call := last(w.api, "level_task_management")
	if status != 200 || !includes(stringsList(r["names"]), "电脑QQ在线") || count(w.api, "level_task_management") != 1 ||
		len(call) != 3 || Int(call["self_id"]) != robot || Int(call["protocol_id"]) != 1 || Str(call["operation"]) != "status" {
		t.Fatalf("unexpected catalog response %d or API call %v", status, call)
	}
	after, err := os.ReadFile(w.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("project catalog modified the config")
	}
}

func TestWebSavesTemplatesAndRetainsThemForOlderClients(t *testing.T) {
	w := webTest(t)
	w.setup(t)
	legacy := w.e.CurrentConfig()
	config := legacy
	config.TaskAliases = map[string]string{"去日签卡打一次卡": "日签打卡"}
	config.ReplyTemplates = make(map[string]string)
	// A fully edited catalog can exceed the default JSON body limit.
	for _, entry := range replyCatalog {
		config.ReplyTemplates[entry.ID] = entry.Default + "\n" + strings.Repeat("示例", 80)
	}
	if status, _ := w.request(t, "/api/config", config, w.csrf, ""); status != 200 {
		t.Fatal("the full catalog could not be saved", status)
	}
	legacy.QuickLogin = false
	if status, _ := w.request(t, "/api/config", legacy, w.csrf, ""); status != 200 {
		t.Fatal("older client could not save config", status)
	}
	current := w.e.CurrentConfig()
	if current.QuickLogin || current.TaskAliases["去日签卡打一次卡"] != "日签打卡" ||
		current.ReplyTemplates["menu"] != config.ReplyTemplates["menu"] {
		t.Fatal("saving an older config lost the installed templates")
	}
	w.e.Handle(event(user, 1, "等级菜单"))
	if got := message(w.api); !strings.Contains(got, "开始登录") || !strings.Contains(got, "示例示例") {
		t.Fatalf("saved template not used in running engine: %q", got)
	}
	config.ReplyTemplates = map[string]string{"menu": "没有指令的菜单"}
	if status, _ := w.request(t, "/api/config", config, w.csrf, ""); status != 400 {
		t.Fatal("invalid menu was saved", status)
	}
	current = w.e.CurrentConfig()
	if current.ReplyTemplates["menu"] == config.ReplyTemplates["menu"] {
		t.Fatal("invalid template replaced the saved menu")
	}
	clear := Object{"bot_qq": robot, "session_minutes": 15, "task_aliases": Object{}, "reply_templates": Object{}}
	if status, _ := w.request(t, "/api/config", clear, w.csrf, ""); status != 200 {
		t.Fatal("could not clear overrides", status)
	}
	raw, err := os.ReadFile(w.path)
	var persisted Config
	if err != nil || json.Unmarshal(raw, &persisted) != nil || len(persisted.ReplyTemplates) != 0 || len(persisted.TaskAliases) != 0 {
		t.Fatal("default templates were not restored in persistent config")
	}
}
