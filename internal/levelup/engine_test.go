package levelup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const robot int64 = 10000001
const user int64 = 20000002
const other int64 = 30000003

type invocation struct {
	action string
	params Object
}
type fakeAPI struct {
	mu                     sync.Mutex
	calls                  []invocation
	accounts               []any
	profiles               []any
	settings               Object
	supported              []any
	taskRows               []any
	membershipRows         []any
	addUnknown             bool
	createErr              error
	createMalformed        bool
	contextFailAfterCreate bool
	stealCreated           bool
	addErr                 error
	loginErr               error
	queryErr               error
	scanReply              Object
	restrict               bool
	qr                     bool
}

func account(qq, pid, device, status int64, kind string) Object {
	platform, typ := "Android", "Pad"
	if kind == "linux" {
		platform, typ = "PC", "Linux"
	}
	if kind == "phone" {
		typ = "Phone"
	}
	return Object{"self_id": qq, "protocol": Object{"id": pid, "platform": platform, "type": typ}, "device": Object{"id": device}, "status": status, "nt": int64(1), "level": int64(100)}
}
func copyObject(m Object) Object { raw, _ := json.Marshal(m); v, _ := decode(raw); return v }
func (f *fakeAPI) Call(ctx context.Context, action string, p Object) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, invocation{action, copyObject(p)})
	switch action {
	case "get_account_management_context":
		if f.contextFailAfterCreate && f.hasCreated() {
			return nil, errors.New("CONTEXT_UNAVAILABLE")
		}
		return Object{"api_version": int64(2), "scope": "framework", "accounts": f.accounts, "protocols": []any{Object{"id": int64(0), "platform": "Android", "type": "Phone", "state": true}, Object{"id": int64(1), "platform": "Android", "type": "Pad", "state": true}, Object{"id": int64(2), "platform": "PC", "type": "Linux", "state": true}}}, nil
	case "create_device_profile":
		if f.createErr != nil {
			return nil, f.createErr
		}
		if f.createMalformed {
			return Object{}, nil
		}
		id := int64(100)
		for _, p := range f.profiles {
			if n := Int(Obj(p)["id"]); n >= id {
				id = n + 1
			}
		}
		f.profiles = append(f.profiles, Object{"id": id, "name": "fixture-created"})
		if f.stealCreated {
			f.accounts = append(f.accounts, account(other, 0, id, 0, "phone"))
		}
		return Object{"id": id, "name": "fixture-created"}, nil
	case "get_device_profile_list":
		return f.profiles, nil
	case "send_friend_msg":
		return Object{"success": true, "message_id": int64(42)}, nil
	case "add_account":
		if f.addErr != nil {
			return nil, f.addErr
		}
		if f.addUnknown {
			return nil, &RemoteError{Code: "TIMEOUT", Uncertain: true}
		}
		kind := "pad"
		if Int(p["protocol_id"]) == 2 {
			kind = "linux"
		}
		f.accounts = append(f.accounts, account(Int(p["self_id"]), Int(p["protocol_id"]), Int(p["device_profile_id"]), 0, kind))
		return nil, nil
	case "update_account":
		for _, v := range f.accounts {
			b := Obj(v)
			if Int(b["self_id"]) == Int(p["self_id"]) && Int(Obj(b["protocol"])["id"]) == Int(p["protocol_id"]) {
				b["status"] = int64(0)
			}
		}
		return nil, nil
	case "login_account":
		if f.loginErr != nil {
			return nil, f.loginErr
		}
		for _, v := range f.accounts {
			b := Obj(v)
			if Int(b["self_id"]) == Int(p["self_id"]) && Int(Obj(b["protocol"])["id"]) == Int(p["protocol_id"]) {
				b["status"] = int64(2)
			}
		}
		if Int(p["protocol_id"]) == 2 {
			return Object{"code": int64(0), "next": "qr", "url": "https://ssl.ptlogin2.qq.com/qr?k=fixture"}, nil
		}
		return Object{"code": int64(140022008), "next": "slider", "slider_url": "https://ti.qq.com/fixture"}, nil
	case "offline_account":
		for _, v := range f.accounts {
			b := Obj(v)
			if Int(b["self_id"]) == Int(p["self_id"]) && Int(Obj(b["protocol"])["id"]) == Int(p["protocol_id"]) {
				b["status"] = int64(0)
			}
		}
		return nil, nil
	case "get_security_verify_methods":
		return Object{"next": "security", "methods": Object{"verify_list": []any{int64(3), int64(4), int64(10)}, "sign": "initial-private-sign"}}, nil
	case "get_sms":
		return Object{"next": "security", "sign": "new-private-sign", "sms": "fixture-content", "send_to": "1069"}, nil
	case "check_sms", "confirm_identity_sms":
		for _, v := range f.accounts {
			b := Obj(v)
			if Int(b["self_id"]) == Int(p["self_id"]) && Int(Obj(b["protocol"])["id"]) == Int(p["protocol_id"]) {
				b["status"] = int64(1)
			}
		}
		return Object{"code": int64(0), "next": "done"}, nil
	case "submit_identity_phone":
		return Object{"next": "identity", "sms": "fixture-content", "send_to": "1069"}, nil
	case "get_security_verify_qr":
		return Object{"next": "security", "guarantee_token": "private-security-token", "qr_url": "https://ssl.ptlogin2.qq.com/security?fixture=1"}, nil
	case "query_security_verify_qr":
		return Object{"next": "security", "status": "waiting"}, nil
	case "scan_qr":
		if f.scanReply != nil {
			return f.scanReply, nil
		}
		return nil, &RemoteError{Code: "UPSTREAM_REJECTED"}
	case "auth_qr":
		return Object{"code": int64(0), "success": true}, nil
	case "query_login_qr":
		if f.queryErr != nil {
			return nil, f.queryErr
		}
		return Object{"code": int64(0), "next": "qr", "state": "waiting_for_confirm", "url": "https://ssl.ptlogin2.qq.com/qr?k=fixture"}, nil
	case "level_task_management":
		if f.restrict && Str(p["operation"]) != "status" {
			return nil, &RemoteError{Code: "QQ_LEVEL_TOO_LOW"}
		}
		switch Str(p["operation"]) {
		case "settings":
			f.settings["selected_tasks"] = copyObject(p)["tasks"]
			f.settings["schedule_enabled"] = p["schedule_enabled"]
			return Object{"settings": f.settings}, nil
		case "panel":
			return Object{"payload": Object{"uin": Int(p["self_id"]), "tasks": f.taskRows}, "state": Object{"supported_tasks": f.supported, "settings": f.settings, "membership_tasks": f.membershipRows}}, nil
		default:
			return Object{"settings": f.settings, "supported_tasks": f.supported}, nil
		}
	case "file_transfer":
		switch Str(p["operation"]) {
		case "start_upload":
			return Object{"token": "private-transfer"}, nil
		case "finish":
			return Object{"file": "transfer://private-transfer"}, nil
		default:
			return Object{}, nil
		}
	case "upload_friend_image":
		return Object{"type": "image", "data": Object{"file_id": "per-recipient-media"}}, nil
	}
	return nil, errors.New("UNEXPECTED_ACTION")
}
func (f *fakeAPI) Submit(ctx context.Context, a string, p Object) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, invocation{a, copyObject(p)})
	return nil
}
func fixture(t *testing.T) (*Engine, *fakeAPI, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{accounts: []any{account(robot, 1, 1, 1, "pad")}, profiles: []any{Object{"id": int64(1)}, Object{"id": int64(2)}, Object{"id": int64(9)}}, settings: Object{"selected_tasks": []any{}, "schedule_enabled": false, "schedule_time": "07:30"}, supported: []any{"签到", "浏览动态", "电脑QQ在线"}, taskRows: []any{Object{"title": "签到", "is_done": false}, Object{"title": "浏览动态", "is_done": false}, Object{"title": "电脑QQ在线", "is_done": false}}}
	return NewEngine(api, store, Config{BotQQ: robot, SessionMinutes: 15, QuickLogin: true}), api, dir
}
func count(api *fakeAPI, a string) int {
	n := 0
	for _, c := range api.calls {
		if c.action == a {
			n++
		}
	}
	return n
}
func last(api *fakeAPI, a string) Object {
	for i := len(api.calls) - 1; i >= 0; i-- {
		if api.calls[i].action == a {
			return api.calls[i].params
		}
	}
	return nil
}
func message(api *fakeAPI) string {
	return Str(Obj(Obj(Arr(last(api, "send_friend_msg")["message"])[0])["data"])["text"])
}
func event(qq int64, id int64, text string) Object {
	return Object{"post_type": "friend_message", "self_id": robot, "protocol_id": int64(1), "sender": Object{"user_id": qq}, "message_id": id, "message": []any{Object{"type": "text", "data": Object{"text": text}}}}
}
func TestOnlyConfiguredBotFriendAndSenderScope(t *testing.T) {
	e, api, _ := fixture(t)
	for _, mutate := range []func(Object){func(m Object) { m["post_type"] = "group_message" }, func(m Object) { m["post_type"] = "group_temp_message" }, func(m Object) { m["self_id"] = other }, func(m Object) { m["protocol_id"] = int64(0) }, func(m Object) { m["sender"] = Object{"user_id": robot} }} {
		v := event(user, 1, "等级菜单")
		mutate(v)
		e.Handle(v)
	}
	if count(api, "send_friend_msg") != 0 {
		t.Fatal("foreign event replied")
	}
	v := event(user, 2, "等级菜单")
	e.Handle(v)
	e.Handle(v)
	if count(api, "send_friend_msg") != 1 {
		t.Fatal("event replay was not suppressed")
	}
	p := last(api, "send_friend_msg")
	if Int(p["self_id"]) != robot || Int(p["user_id"]) != user || !strings.Contains(message(api), "电脑登录") {
		t.Fatal("menu identity")
	}
}
func TestPasswordOwnershipLatestUnusedAndNoPersistedSecret(t *testing.T) {
	e, api, dir := fixture(t)
	api.accounts = append(api.accounts, account(other, 0, 9, 1, "phone"))
	if err := e.startPad(user); err != nil {
		t.Fatal(err)
	}
	secret := " fixture-private-password "
	if err := e.password(user, secret); err != nil {
		t.Fatal(err)
	}
	p := last(api, "add_account")
	if Int(p["self_id"]) != user || Int(p["protocol_id"]) != 1 || Int(p["device_profile_id"]) != 2 || Str(p["password"]) != secret || Int(p["nt"]) != 1 {
		t.Fatal("wrong creation identity/profile/password")
	}
	if e.flow(user).Stage != "backend" {
		t.Fatal("slider must use backend")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "state.enc"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("secret stored in state")
	}
	stateRaw, _ := json.Marshal(e.Store.Snapshot())
	if strings.Contains(string(stateRaw), secret) {
		t.Fatal("password persisted")
	}
	for _, c := range api.calls {
		if c.action == "send_friend_msg" {
			raw, _ := json.Marshal(c.params)
			if strings.Contains(string(raw), secret) {
				t.Fatal("password echoed")
			}
		}
	}
}
func TestUnknownCreateNeverReplayedAndReservationSurvivesRestart(t *testing.T) {
	e, api, dir := fixture(t)
	api.addUnknown = true
	if _, err := e.ensureAccount(user, "pad", "fixture"); !uncertain(err) {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	e.Store = reopened
	if _, err = e.ensureAccount(user, "pad", "different"); !uncertain(err) {
		t.Fatal(err)
	}
	if count(api, "add_account") != 1 {
		t.Fatal("unknown creation replayed")
	}
	if len(e.Store.Snapshot().Profiles) != 1 {
		t.Fatal("reservation lost")
	}
}
func TestExistingKeepsDeviceAndManualValidationEnablesNewTasks(t *testing.T) {
	e, api, _ := fixture(t)
	if err := e.startPad(user); err != nil {
		t.Fatal(err)
	}
	if err := e.password(user, "fixture"); err != nil {
		t.Fatal(err)
	}
	for _, v := range api.accounts {
		if Int(Obj(v)["self_id"]) == user {
			Obj(v)["status"] = int64(1)
		}
	}
	if err := e.continueLogin(user); err != nil {
		t.Fatal(err)
	}
	if e.flow(user) != nil {
		t.Fatal("flow not completed")
	}
	if len(stringsList(api.settings["selected_tasks"])) != 3 || Bool(api.settings["schedule_enabled"]) || Str(api.settings["schedule_time"]) != "07:30" {
		t.Fatal("new defaults/schedule preservation")
	}
	b, err := e.ensureAccount(user, "pad", "unused")
	if err != nil || b.Device != 9 || count(api, "add_account") != 1 {
		t.Fatal("existing device changed")
	}
}
func TestProjectsPreserveOtherSelectionsAndGlobalSchedule(t *testing.T) {
	e, api, _ := fixture(t)
	api.accounts = append(api.accounts, account(user, 1, 2, 1, "pad"))
	api.settings["selected_tasks"] = []any{"签到", "浏览动态"}
	api.settings["schedule_enabled"] = true
	if err := e.toggle(user, "签到", false); err != nil {
		t.Fatal(err)
	}
	if includes(stringsList(api.settings["selected_tasks"]), "签到") || !includes(stringsList(api.settings["selected_tasks"]), "浏览动态") || !Bool(api.settings["schedule_enabled"]) {
		t.Fatal("toggle damaged other settings")
	}
	if err := e.toggle(user, "不存在", true); err != nil {
		t.Fatal(err)
	}
	if count(api, "add_account") != 0 {
		t.Fatal("toggle provisioned")
	}
}
func TestBatchSkipsDoneAndPCUsesNoWaitAndHasCooldown(t *testing.T) {
	e, api, _ := fixture(t)
	api.accounts = append(api.accounts, account(user, 1, 2, 1, "pad"))
	api.settings["selected_tasks"] = []any{"签到", "浏览动态", "电脑QQ在线"}
	Obj(api.taskRows[0])["is_done"] = true
	if err := e.repair(user); err != nil {
		t.Fatal(err)
	}
	batch := stringsList(last(api, "execute_level_tasks")["tasks"])
	if len(batch) != 1 || batch[0] != "浏览动态" || !strings.Contains(message(api), "不代表任务已完成") {
		t.Fatal("batch semantics")
	}
	if err := e.repair(user); err != nil {
		t.Fatal(err)
	}
	if count(api, "execute_level_tasks") != 1 {
		t.Fatal("cooldown bypass")
	}
}
func TestRestrictedNeverSubmitsTasks(t *testing.T) {
	e, api, _ := fixture(t)
	api.accounts = append(api.accounts, account(user, 1, 2, 1, "pad"))
	api.settings["selected_tasks"] = []any{"签到"}
	api.restrict = true
	if err := e.repair(user); err == nil {
		t.Fatal("restriction ignored")
	}
	if count(api, "execute_level_tasks") != 0 {
		t.Fatal("restricted task submitted")
	}
}
func TestLinuxPasswordlessFailureFallsBackToUploadedImage(t *testing.T) {
	e, api, _ := fixture(t)
	api.accounts = append(api.accounts, account(user, 1, 2, 1, "pad"))
	if err := e.startLinux(user); err != nil {
		t.Fatal(err)
	}
	p := last(api, "add_account")
	if Str(p["password"]) != "" || Int(p["protocol_id"]) != 2 || Int(p["device_profile_id"]) != 9 {
		t.Fatal("Linux/password/profile")
	}
	if count(api, "scan_qr") != 1 || count(api, "auth_qr") != 0 || count(api, "upload_friend_image") != 1 || count(api, "file_transfer") != 4 {
		t.Fatal("QR transfer or retry behavior")
	}
	image := last(api, "send_friend_msg")
	if Int(image["user_id"]) != user || Str(Obj(Arr(image["message"])[0])["type"]) != "image" {
		t.Fatal("image recipient")
	}
	if err := e.continueLogin(user); err != nil {
		t.Fatal(err)
	}
	if count(api, "upload_friend_image") != 1 {
		t.Fatal("poll resent QR")
	}
}
func TestCancelNeverOfflinesCompletedSession(t *testing.T) {
	e, api, _ := fixture(t)
	api.accounts = append(api.accounts, account(user, 1, 2, 1, "pad"))
	e.setFlow(user, e.newSession("pad", "backend", 1))
	if err := e.cancel(user); err != nil {
		t.Fatal(err)
	}
	if count(api, "offline_account") != 0 || e.flow(user) != nil {
		t.Fatal("cancel interrupted logged in account")
	}
}
func TestCorruptStateStopsInsteadOfForgettingOwnership(t *testing.T) {
	_, _, dir := fixture(t)
	if err := os.WriteFile(filepath.Join(dir, "state.enc"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(dir); err == nil {
		t.Fatal("corrupt state accepted")
	}
}
func TestSenderCannotChooseAnotherAccountInPasswordOrCommand(t *testing.T) {
	e, api, _ := fixture(t)
	e.Handle(event(user, 1, "开始登录 30000003"))
	if count(api, "login_account") != 0 || count(api, "add_account") != 0 {
		t.Fatal("target injection")
	}
	e.setFlow(user, e.newSession("pad", "sms_code", 1))
	if err := e.handle(user, "30000003 登录", "30000003 登录"); err != nil {
		t.Fatal(err)
	}
	if count(api, "check_sms") != 0 {
		t.Fatal("unvalidated SMS")
	}
}
func TestExpiredSessionAndProtocolZero(t *testing.T) {
	e, _, _ := fixture(t)
	f := e.newSession("pad", "password", 0)
	f.Expires = time.Now().Add(-time.Second)
	e.setFlow(user, f)
	if e.flow(user) != nil {
		t.Fatal("session did not expire")
	}
	d := Directory{Protocols: []any{Object{"id": int64(0), "platform": "Android", "type": "Pad", "state": true}}}
	id, err := protocol(d, "pad", nil)
	if err != nil || id != 0 {
		t.Fatal("protocol zero invalid")
	}
}

func TestSMSUsesCurrentReturnedSignAndUserIdentity(t *testing.T) {
	e, api, dir := fixture(t)
	api.accounts = append(api.accounts, account(user, 1, 2, 2, "pad"))
	e.setFlow(user, e.newSession("pad", "backend", 1))
	if err := e.acceptLogin(user, Object{"next": "security", "code": int64(140022010)}); err != nil {
		t.Fatal(err)
	}
	if err := e.startSMS(user, 4); err != nil {
		t.Fatal(err)
	}
	if e.flow(user).Sign != "new-private-sign" || e.flow(user).Stage != "sms_code" {
		t.Fatal("SMS sign/session not saved")
	}
	if err := e.checkSMS(user, "123456"); err != nil {
		t.Fatal(err)
	}
	p := last(api, "check_sms")
	if Int(p["self_id"]) != user || Str(p["sign"]) != "new-private-sign" || Str(p["code"]) != "123456" {
		t.Fatal("SMS identity or stale sign")
	}
	if e.flow(user) != nil {
		t.Fatal("login not finalized")
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "state.enc"))
	if strings.Contains(string(raw), "private-sign") {
		t.Fatal("sign persisted")
	}
}
func TestIdentityPhoneRequiresActualIdentityStep(t *testing.T) {
	e, api, _ := fixture(t)
	api.accounts = append(api.accounts, account(user, 1, 2, 2, "pad"))
	e.setFlow(user, e.newSession("pad", "backend", 1))
	if err := e.handle(user, "身份手机号", "身份手机号"); err != nil {
		t.Fatal(err)
	}
	if e.flow(user).Stage != "backend" {
		t.Fatal("wrong step accepted")
	}
	if err := e.acceptLogin(user, Object{"next": "identity"}); err != nil {
		t.Fatal(err)
	}
	if err := e.handle(user, "身份手机号", "身份手机号"); err != nil {
		t.Fatal(err)
	}
	if err := e.handle(user, "86 13800138000", "86 13800138000"); err != nil {
		t.Fatal(err)
	}
	if e.flow(user).Stage != "identity_sent" {
		t.Fatal("identity phone not accepted")
	}
	if err := e.checkSent(user); err != nil {
		t.Fatal(err)
	}
	p := last(api, "confirm_identity_sms")
	if Str(p["mobile"]) != "13800138000" || Str(p["area_code"]) != "86" || Int(p["self_id"]) != user {
		t.Fatal("identity confirmation mismatch")
	}
}
func TestNewDefaultDoesNotOverwriteExistingAndRobotCannotBecomeTarget(t *testing.T) {
	e, api, _ := fixture(t)
	api.accounts = append(api.accounts, account(user, 1, 2, 1, "pad"))
	api.settings["selected_tasks"] = []any{"签到"}
	if err := e.showProjects(user); err != nil {
		t.Fatal(err)
	}
	if count(api, "level_task_management") != 1 || len(stringsList(api.settings["selected_tasks"])) != 1 {
		t.Fatal("existing plan was replaced")
	}
	e.Handle(event(robot, 999, "开始登录"))
	if count(api, "add_account") != 0 {
		t.Fatal("robot managed itself")
	}
}

func (f *fakeAPI) hasCreated() bool {
	for _, p := range f.profiles {
		if Str(Obj(p)["name"]) == "fixture-created" {
			return true
		}
	}
	return false
}
func TestAutomaticDeviceCreationIndependentPadLinuxAndExisting(t *testing.T) {
	e, api, _ := fixture(t)
	api.profiles = []any{Object{"id": int64(1)}}
	pad, err := e.ensureAccount(user, "pad", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	linux, err := e.ensureAccount(user, "linux", "")
	if err != nil {
		t.Fatal(err)
	}
	if pad.Device == linux.Device || count(api, "create_device_profile") != 2 {
		t.Fatal("devices must be independent")
	}
	if _, err = e.ensureAccount(user, "pad", "fixture"); err != nil || count(api, "create_device_profile") != 2 {
		t.Fatal("existing device replaced", err)
	}
	for _, c := range api.calls {
		for _, k := range []string{"node", "node_id", "nodeId", "proxy"} {
			if _, ok := c.params[k]; ok {
				t.Fatal("removed routing parameter", c.action)
			}
		}
		if c.action == "create_device_profile" && len(c.params) != 0 {
			t.Fatal("creation takes no parameters")
		}
	}
}
func TestUnknownDeviceCreationBlocksReplayAcrossRestart(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(fmt.Sprint(malformed), func(t *testing.T) {
			e, api, dir := fixture(t)
			api.profiles = []any{}
			api.createMalformed = malformed
			if !malformed {
				api.createErr = &RemoteError{Code: "IPC_TIMEOUT", Uncertain: true}
			}
			if _, err := e.ensureAccount(user, "pad", "fixture"); !uncertain(err) {
				t.Fatal(err)
			}
			store, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			e.Store = store
			if _, err = e.ensureAccount(user, "pad", "fixture"); !uncertain(err) {
				t.Fatal(err)
			}
			if count(api, "create_device_profile") != 1 || count(api, "add_account") != 0 {
				t.Fatal("uncertain request replayed")
			}
			// Administrator reconciles through the framework: a real unique account resolves the intent.
			api.contextFailAfterCreate = false
			api.accounts = append(api.accounts, account(user, 1, 10, 0, "pad"))
			if _, err = e.ensureAccount(user, "pad", "fixture"); err != nil {
				t.Fatal(err)
			}
			if len(e.Store.Snapshot().Intents) != 0 {
				t.Fatal("manual reconciliation did not resolve intent")
			}
		})
	}
}
func TestDefinitiveCreateRejectionCanRetry(t *testing.T) {
	e, api, _ := fixture(t)
	api.profiles = []any{}
	api.createErr = &RemoteError{Code: "DENIED", Status: "restricted"}
	if _, err := e.ensureAccount(user, "pad", "fixture"); err == nil {
		t.Fatal("ignored rejection")
	}
	if len(e.Store.Snapshot().Intents) != 0 || count(api, "add_account") != 0 {
		t.Fatal("rejection left intent or created account")
	}
	api.createErr = nil
	if _, err := e.ensureAccount(user, "pad", "fixture"); err != nil {
		t.Fatal(err)
	}
}
func TestCreatedDeviceReservationSurvivesRefreshFailure(t *testing.T) {
	e, api, dir := fixture(t)
	api.profiles = []any{Object{"id": int64(1)}}
	api.contextFailAfterCreate = true
	if _, err := e.ensureAccount(user, "pad", "fixture"); err == nil {
		t.Fatal("refresh failure ignored")
	}
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	e.Store = store
	api.contextFailAfterCreate = false
	if _, err = e.ensureAccount(user, "pad", "fixture"); err != nil {
		t.Fatal(err)
	}
	if count(api, "create_device_profile") != 1 {
		t.Fatal("lost created reservation")
	}
}
func TestCreatedDeviceOccupiedByAnotherToolStopsAccountCreation(t *testing.T) {
	e, api, _ := fixture(t)
	api.profiles = []any{}
	api.stealCreated = true
	if _, err := e.ensureAccount(user, "pad", "fixture"); err == nil {
		t.Fatal("shared device accepted")
	}
	if count(api, "add_account") != 0 {
		t.Fatal("shared device bound")
	}
}
func TestDefinitiveAccountRejectionRetainsDeviceForRetry(t *testing.T) {
	e, api, _ := fixture(t)
	api.profiles = []any{}
	api.addErr = &RemoteError{Code: "DENIED", Status: "restricted"}
	if _, err := e.ensureAccount(user, "pad", "fixture"); err == nil {
		t.Fatal("ignored rejection")
	}
	api.addErr = nil
	if _, err := e.ensureAccount(user, "pad", "fixture"); err != nil {
		t.Fatal(err)
	}
	if count(api, "create_device_profile") != 1 {
		t.Fatal("created extra device after account rejection")
	}
}
func TestFrameworkContextRejectsLegacyAndIncompleteSnapshots(t *testing.T) {
	for _, ctx := range []Object{
		{"api_version": int64(2), "scope": "node", "accounts": []any{}, "protocols": []any{}},
		{"api_version": int64(2), "scope": "framework", "protocols": []any{}},
	} {
		if _, err := directory(ctx); err == nil {
			t.Fatal("invalid context accepted")
		}
	}
	if _, err := directory(Object{"api_version": int64(2), "scope": "framework", "accounts": []any{}, "protocols": []any{}}); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentProvisioningCreatesIndependentDevices(t *testing.T) {
	e, api, _ := fixture(t)
	api.profiles = []any{}
	errs := make(chan error, 2)
	for _, qq := range []int64{user, other} {
		go func(qq int64) { _, err := e.ensureAccount(qq, "pad", "fixture"); errs <- err }(qq)
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if count(api, "create_device_profile") != 2 || len(e.Store.Snapshot().Profiles) != 2 {
		t.Fatal("concurrent allocation lost device claims")
	}
}
func TestLinuxAuthorizationRejectionNeverFallsThroughToQR(t *testing.T) {
	e, api, _ := fixture(t)
	api.loginErr = &RemoteError{Code: "QQ_LEVEL_UNKNOWN", Status: "restricted"}
	if err := e.startLinux(user); err == nil {
		t.Fatal("ignored Linux login restriction")
	}
	if count(api, "scan_qr") != 0 || count(api, "file_transfer") != 0 || count(api, "query_login_qr") != 0 {
		t.Fatal("restricted login continued")
	}
	if err := e.continueLogin(user); err != nil {
		t.Fatal(err)
	}
	if count(api, "login_account") != 1 {
		t.Fatal("replayed restricted login")
	}
}
func TestLinuxQueryRestrictionDoesNotReplayLoginOrSendQR(t *testing.T) {
	e, api, _ := fixture(t)
	if err := e.startLinux(user); err != nil {
		t.Fatal(err)
	}
	api.queryErr = &RemoteError{Code: "QQ_LEVEL_UNKNOWN", Status: "restricted"}
	if err := e.continueLogin(user); err == nil {
		t.Fatal("ignored Linux query restriction")
	}
	if count(api, "login_account") != 1 || count(api, "upload_friend_image") != 1 {
		t.Fatal("replayed login or QR")
	}
}

func TestLevelMenuReplacesOldTrigger(t *testing.T) {
	e, api, _ := fixture(t)
	old := "代" + "练菜单"
	e.Handle(event(user, 800, old))
	if count(api, "send_friend_msg") != 0 {
		t.Fatal("old menu trigger still active")
	}
	e.Handle(event(user, 801, "等级菜单"))
	if count(api, "send_friend_msg") != 1 || !strings.HasPrefix(message(api), "等级菜单\n") {
		t.Fatal("new trigger failed")
	}
}

func TestLinuxQuickLoginHonorsScanNeedA1(t *testing.T) {
	for _, tc := range []struct {
		name     string
		needA1   any
		wantAuth bool
	}{
		{"false", false, true},
		{"true", true, true},
		{"missing", nil, false},
		{"invalid", "false", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, api, _ := fixture(t)
			api.accounts = append(api.accounts, account(user, 1, 2, 1, "pad"))
			api.scanReply = Object{"code": int64(0)}
			if tc.needA1 != nil {
				api.scanReply["need_a1"] = tc.needA1
			}
			if err := e.startLinux(user); err != nil {
				t.Fatal(err)
			}
			if p := last(api, "auth_qr"); tc.wantAuth {
				if p["need_a1"] != tc.needA1 || Int(p["self_id"]) != user || Int(p["protocol_id"]) != 1 || Str(p["k"]) != "fixture" {
					t.Fatalf("incorrect QR authorization params: %v", p)
				}
				if _, ok := p["skip_phone_confirm"]; ok {
					t.Fatal("framework must decide whether phone confirmation is needed")
				}
			} else if p != nil {
				t.Fatal("authorized without a valid scan flag")
			}
			if count(api, "upload_friend_image") != 1 || count(api, "login_account") != 1 {
				t.Fatal("should fall back to the current login QR without opening another session")
			}
		})
	}
}

func TestRepairChecksMembershipSignInAndCurrentTaskCatalog(t *testing.T) {
	e, api, _ := fixture(t)
	api.accounts = append(api.accounts, account(user, 1, 2, 1, "pad"))
	const signIn = "会员官网连续签到奖励"
	const yuanbao = "去元宝体验技能"
	const removed = "去京东浏览15秒商品"
	api.settings["selected_tasks"] = []any{signIn, yuanbao, removed, "状态缺失", "不可补挂"}
	api.supported = []any{signIn, yuanbao, "状态缺失", "不可补挂"}
	api.taskRows = []any{
		Object{"title": signIn, "status": int64(0)},
		Object{"title": yuanbao, "status": "0"},
		Object{"title": removed, "status": int64(0)},
		Object{"title": "状态缺失"},
		Object{"title": "不可补挂", "is_done": false, "executable": false},
	}
	today := time.Now().In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format(time.DateOnly)
	api.membershipRows = []any{Object{"title": signIn, "action_done": true, "action_done_date": today, "reward_status": "not_due", "is_done": false}}
	if err := e.repair(user); err != nil {
		t.Fatal(err)
	}
	if batch := stringsList(last(api, "execute_level_tasks")["tasks"]); len(batch) != 1 || batch[0] != yuanbao {
		t.Fatalf("unexpected batch: %v", batch)
	}
}
