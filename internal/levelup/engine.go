package levelup

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	BotQQ           int64  `json:"bot_qq"`
	BotProtocolID   *int64 `json:"bot_protocol_id"`
	PadProtocolID   *int64 `json:"pad_protocol_id"`
	LinuxProtocolID *int64 `json:"linux_protocol_id"`
	AdminURL        string `json:"admin_url"`
	DataDir         string `json:"data_dir"`
	SessionMinutes  int    `json:"session_minutes"`
	QuickLogin      bool   `json:"quick_login"`
	WebListen       string `json:"web_listen"`
}
type Bot struct {
	QQ, PID, Device, Status, NT, Level int64
	Kind                               string
	Raw                                Object
}
type Directory struct {
	Bots      []Bot
	Protocols []any
}

func directory(v any) (Directory, error) {
	m := Obj(v)
	if Int(m["api_version"]) != 2 || Str(m["scope"]) != "framework" || Arr(m["accounts"]) == nil || Arr(m["protocols"]) == nil {
		return Directory{}, errors.New("CONTEXT_SCHEMA")
	}
	d := Directory{Protocols: Arr(m["protocols"])}
	for _, v := range Arr(m["accounts"]) {
		r := Obj(v)
		p := Obj(r["protocol"])
		dev := Obj(r["device"])
		kind := "other"
		if Str(p["platform"]) == "Android" && strings.EqualFold(Str(p["type"]), "Pad") {
			kind = "pad"
		}
		if strings.EqualFold(Str(p["type"]), "Linux") {
			kind = "linux"
		}
		{
			b := Bot{QQ: Int(r["self_id"]), PID: Int(p["id"]), Device: Int(dev["id"]), Status: Int(r["status"]), NT: Int(r["nt"]), Level: Int(r["level"]), Kind: kind, Raw: r}
			if b.QQ < 10000 || b.PID < 0 || b.Device < 1 {
				return d, errors.New("ACCOUNT_SCHEMA")
			}
			d.Bots = append(d.Bots, b)
		}
	}
	return d, nil
}
func findBot(d Directory, qq int64, kind string) (*Bot, error) {
	var found *Bot
	for i := range d.Bots {
		b := &d.Bots[i]
		if b.QQ == qq && b.Kind == kind {
			if found != nil {
				return nil, errors.New("AMBIGUOUS_ACCOUNT")
			}
			found = b
		}
	}
	return found, nil
}
func protocol(d Directory, kind string, wanted *int64) (int64, error) {
	result := int64(-1)
	for _, v := range d.Protocols {
		p := Obj(v)
		match := kind == "pad" && Str(p["platform"]) == "Android" && strings.EqualFold(Str(p["type"]), "Pad") || kind == "linux" && strings.EqualFold(Str(p["type"]), "Linux")
		id := Int(p["id"])
		if match && Bool(p["state"]) && id >= 0 && (wanted == nil || *wanted == id) {
			if result >= 0 {
				return -1, errors.New("AMBIGUOUS_PROTOCOL")
			}
			result = id
		}
	}
	if result < 0 {
		return -1, errors.New("PROTOCOL_UNAVAILABLE")
	}
	return result, nil
}
func selector(qq, pid int64) Object { return Object{"self_id": qq, "protocol_id": pid} }
func stringsList(v any) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, v := range Arr(v) {
		s := Str(v)
		if s != "" && !seen[s] {
			out = append(out, s)
			seen[s] = true
		}
	}
	return out
}
func includes(a []string, s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
}

type Session struct {
	Kind, Stage                        string
	PID                                int64
	Expires                            time.Time
	Sign, Mobile, Area, QRToken, QRURL string
	VerifyType                         int64
	Methods                            Object
}
type Engine struct {
	API         API
	Store       *Store
	Config      Config
	mu          sync.Mutex
	users       map[int64]*sync.Mutex
	flows       map[int64]*Session
	last        map[int64]time.Time
	provisionMu sync.Mutex
	configGate  sync.RWMutex
}

func NewEngine(api API, store *Store, c Config) *Engine {
	if c.SessionMinutes < 1 || c.SessionMinutes > 30 {
		c.SessionMinutes = 15
	}
	return &Engine{API: api, Store: store, Config: c, users: map[int64]*sync.Mutex{}, flows: map[int64]*Session{}, last: map[int64]time.Time{}}
}
func (e *Engine) acquire(qq int64) (func(), bool) {
	e.mu.Lock()
	l := e.users[qq]
	if l == nil {
		l = &sync.Mutex{}
		e.users[qq] = l
	}
	if !l.TryLock() {
		e.mu.Unlock()
		return nil, false
	}
	e.mu.Unlock()
	return func() { e.mu.Lock(); l.Unlock(); delete(e.users, qq); e.mu.Unlock() }, true
}
func (e *Engine) flow(qq int64) *Session {
	e.mu.Lock()
	defer e.mu.Unlock()
	f := e.flows[qq]
	if f != nil && time.Now().After(f.Expires) {
		delete(e.flows, qq)
		return nil
	}
	return f
}
func (e *Engine) setFlow(qq int64, f *Session) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if f == nil {
		delete(e.flows, qq)
	} else {
		e.flows[qq] = f
	}
}
func (e *Engine) dir() (Directory, error) {
	v, err := Call(e.API, "get_account_management_context", Object{})
	if err != nil {
		return Directory{}, err
	}
	return directory(v)
}
func (e *Engine) bot() (Bot, error) {
	d, err := e.dir()
	if err != nil {
		return Bot{}, err
	}
	b, err := findConfiguredBot(d, e.Config.BotQQ, "pad", e.Config.BotProtocolID)
	if err != nil || b == nil {
		return Bot{}, errors.New("BOT_NOT_FOUND")
	}
	if b.Status != 1 || b.NT != 1 || e.Config.BotProtocolID != nil && b.PID != *e.Config.BotProtocolID {
		return Bot{}, errors.New("BOT_UNAVAILABLE")
	}
	return *b, nil
}
func (e *Engine) send(qq int64, text string) error {
	b, err := e.bot()
	if err != nil {
		return err
	}
	p := selector(b.QQ, b.PID)
	p["user_id"] = qq
	p["message"] = []any{Object{"type": "text", "data": Object{"text": text}}}
	v, err := Call(e.API, "send_friend_msg", p)
	if err != nil {
		return err
	}
	if !Bool(Obj(v)["success"]) {
		return errors.New("SEND_NOT_CONFIRMED")
	}
	return nil
}

const Menu = "等级菜单\n-------\n开始登录 | 立即补挂\n项目开关 | 电脑登录\n继续验证 | 取消操作\n\n仅能操作您当前发消息的QQ；请勿发送其他号码的密码。"

func eventText(event Object) (string, bool) {
	var b strings.Builder
	for _, v := range Arr(event["message"]) {
		s := Obj(v)
		if Str(s["type"]) != "text" {
			return "", false
		}
		b.WriteString(Str(Obj(s["data"])["text"]))
	}
	text := b.String()
	return text, len(text) > 0 && len(text) <= 4096
}
func known(s string) bool {
	switch s {
	case "身份手机号", "等级菜单", "开始登录", "立即补挂", "项目开关", "电脑登录", "继续验证", "取消操作", "接收短信", "发送短信", "安全扫码", "已发送", "查询二维码":
		return true
	}
	return strings.HasSuffix(s, " 开启") || strings.HasSuffix(s, " 关闭")
}
func (e *Engine) Handle(event Object) {
	e.configGate.RLock()
	defer e.configGate.RUnlock()
	if e.Config.BotQQ == 0 {
		return
	}
	// Never process group/temp messages, self echoes, another bot or another protocol.
	if Str(event["post_type"]) != "friend_message" || Int(event["self_id"]) != e.Config.BotQQ {
		return
	}
	sender := Int(Obj(event["sender"])["user_id"])
	if sender < 10000 || sender == e.Config.BotQQ {
		return
	}
	if e.Config.BotProtocolID != nil && Int(event["protocol_id"]) != *e.Config.BotProtocolID {
		return
	}
	d, err := e.dir()
	if err != nil {
		return
	}
	b, err := findConfiguredBot(d, e.Config.BotQQ, "pad", e.Config.BotProtocolID)
	if err != nil || b == nil || b.PID != Int(event["protocol_id"]) {
		return
	}
	text, ok := eventText(event)
	if !ok {
		return
	}
	command := strings.TrimSpace(text)
	if !known(command) && e.flow(sender) == nil {
		return
	}
	msg := Obj(event["msg"])
	messageID := Int(event["message_id"])
	identity := fmt.Sprintf("%d:%d:%d:%d", e.Config.BotQQ, b.PID, sender, messageID)
	if messageID <= 0 {
		if Str(msg["uid"]) == "" {
			return
		}
		identity += fmt.Sprintf(":%s:%v:%v:%v", Str(msg["uid"]), msg["seq"], msg["random"], msg["time"])
	}
	release, ok := e.acquire(sender)
	if !ok {
		return
	}
	defer release()
	if !e.Store.Admit(identity) {
		return
	}
	e.mu.Lock()
	last := e.last[sender]
	e.last[sender] = time.Now()
	e.mu.Unlock()
	if time.Since(last) < time.Second {
		return
	}
	if err = e.handle(sender, command, text); err != nil {
		_ = e.send(sender, Friendly(err))
	}
}
func (e *Engine) handle(qq int64, command, raw string) error {
	switch command {
	case "等级菜单":
		return e.send(qq, Menu)
	case "取消操作":
		return e.cancel(qq)
	case "开始登录":
		return e.startPad(qq)
	case "电脑登录":
		return e.startLinux(qq)
	case "立即补挂":
		return e.repair(qq)
	case "项目开关":
		return e.showProjects(qq)
	case "继续验证", "查询二维码":
		return e.continueLogin(qq)
	case "身份手机号":
		f := e.flow(qq)
		if f == nil || f.Kind != "pad" || f.Stage != "identity_backend" {
			return e.send(qq, "请先在后台完成身份验证页面，并确认当前要求核验手机号。")
		}
		f.Stage = "identity_phone"
		return e.send(qq, "请发送“区号 手机号”，例如：86 13800138000。")
	case "接收短信":
		return e.startSMS(qq, 4)
	case "发送短信":
		return e.startSMS(qq, 3)
	case "安全扫码":
		return e.securityQR(qq)
	case "已发送":
		return e.checkSent(qq)
	}
	if f := e.flow(qq); f != nil && f.Stage == "password" {
		if len(raw) == 0 || len(raw) > 256 {
			return e.send(qq, "密码长度不正确，请重新发送，或发“取消操作”。")
		}
		return e.password(qq, raw)
	}
	if strings.HasSuffix(command, " 开启") || strings.HasSuffix(command, " 关闭") {
		i := strings.LastIndex(command, " ")
		return e.toggle(qq, command[:i], command[i+1:] == "开启")
	}
	f := e.flow(qq)
	if f == nil {
		return nil
	}
	switch f.Stage {
	case "password":
		if len(raw) == 0 || len(raw) > 256 {
			return e.send(qq, "密码长度不正确，请重新发送，或发“取消操作”。")
		}
		return e.password(qq, raw)
	case "sms_code":
		if len(command) < 4 || len(command) > 12 || strings.Trim(command, "0123456789") != "" {
			return e.send(qq, "请发送短信中的数字验证码，或发“取消操作”。")
		}
		return e.checkSMS(qq, command)
	case "identity_phone":
		parts := strings.Fields(command)
		if len(parts) != 2 || len(parts[1]) < 5 || len(parts[1]) > 20 || strings.Trim(parts[0]+parts[1], "0123456789") != "" {
			return e.send(qq, "请发送“区号 手机号”，例如：86 13800138000。")
		}
		p := selector(qq, f.PID)
		p["area_code"] = parts[0]
		p["mobile"] = parts[1]
		v, err := Call(e.API, "submit_identity_phone", p)
		if err != nil {
			return err
		}
		f.Area = parts[0]
		f.Mobile = parts[1]
		f.Stage = "identity_sent"
		return e.smsInstructions(qq, Obj(v))
	default:
		return e.send(qq, "验证尚未完成。请按上条提示操作，再发送“继续验证”；也可发“取消操作”。")
	}
}
func (e *Engine) requirePad(qq int64) (*Bot, error) {
	d, err := e.dir()
	if err != nil {
		return nil, err
	}
	b, err := findConfiguredBot(d, qq, "pad", e.Config.PadProtocolID)
	if err != nil {
		return nil, err
	}
	if b == nil || b.Status != 1 {
		_ = e.send(qq, "请先发送“开始登录”，完成Pad账号登录。")
		return nil, errors.New("LOGIN_REQUIRED")
	}
	if b.NT != 1 {
		return nil, errors.New("NT_REQUIRED")
	}
	return b, nil
}
func (e *Engine) ensureAccount(qq int64, kind, password string) (*Bot, error) {
	e.provisionMu.Lock()
	defer e.provisionMu.Unlock()
	d, err := e.dir()
	if err != nil {
		return nil, err
	}
	wanted := e.Config.PadProtocolID
	if kind == "linux" {
		wanted = e.Config.LinuxProtocolID
	}
	pid, err := protocol(d, kind, wanted)
	if err != nil {
		return nil, err
	}
	b, err := findConfiguredBot(d, qq, kind, &pid)
	if err != nil {
		return nil, err
	}
	owner := key(qq, pid)
	st := e.Store.Snapshot()
	if b != nil {
		if b.PID != pid || b.NT != 1 {
			return nil, errors.New("ACCOUNT_CONFIGURATION_CONFLICT")
		}
		for _, other := range d.Bots {
			if other.Device == b.Device && (other.QQ != qq || other.PID != pid) {
				return nil, errors.New("SHARED_DEVICE_CONFLICT")
			}
		}
		if claim := st.Profiles[strconv.FormatInt(b.Device, 10)]; claim != "" && claim != owner {
			return nil, errors.New("DEVICE_OWNER_CONFLICT")
		}
		if err = e.Store.Update(func(s *State) error {
			s.Profiles[strconv.FormatInt(b.Device, 10)] = owner
			if i, ok := s.Intents[owner]; ok && (i.Device == b.Device || i.Phase == "device_creating") {
				delete(s.Intents, owner)
				if kind == "pad" {
					s.Initialized[owner] = true
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
		return b, nil
	}
	if i, ok := st.Intents[owner]; ok && i.Phase == "device_creating" {
		return nil, &RemoteError{Code: "DEVICE_CREATE_RESULT_UNKNOWN", Uncertain: true}
	}
	if i, ok := st.Intents[owner]; ok && i.Phase == "sending" {
		return nil, &RemoteError{Code: "CREATE_RESULT_UNKNOWN", Uncertain: true}
	}
	v, err := Call(e.API, "get_device_profile_list", Object{})
	if err != nil {
		return nil, err
	}
	profiles := Arr(v)
	if profiles == nil {
		return nil, errors.New("DEVICE_SCHEMA")
	}
	seenProfiles := map[int64]bool{}
	for _, p := range profiles {
		id := Int(Obj(p)["id"])
		if id < 1 || seenProfiles[id] {
			return nil, errors.New("DEVICE_SCHEMA")
		}
		seenProfiles[id] = true
	}
	id := int64(-1)
	occupied := map[int64]bool{}
	for _, b := range d.Bots {
		occupied[b.Device] = true
	}
	for _, p := range profiles {
		n := Int(Obj(p)["id"])
		claim := st.Profiles[strconv.FormatInt(n, 10)]
		if claim == owner && !occupied[n] {
			if id >= 0 {
				return nil, errors.New("MULTIPLE_OWNED_DEVICES")
			}
			id = n
		}
	}
	if id < 0 {
		for _, p := range profiles {
			n := Int(Obj(p)["id"])
			if n > id && !occupied[n] && st.Profiles[strconv.FormatInt(n, 10)] == "" {
				id = n
			}
		}
	}
	if id < 1 {
		// Persist before a non-idempotent request; a lost reply must never grow the pool on retry.
		if err = e.Store.Update(func(s *State) error {
			s.Intents[owner] = Intent{QQ: qq, PID: pid, Phase: "device_creating", At: time.Now().Unix()}
			return nil
		}); err != nil {
			return nil, err
		}
		created, createErr := Call(e.API, "create_device_profile", Object{})
		if createErr != nil {
			if uncertain(createErr) {
				return nil, &RemoteError{Code: "DEVICE_CREATE_RESULT_UNKNOWN", Uncertain: true}
			}
			if err = e.Store.Update(func(s *State) error { delete(s.Intents, owner); return nil }); err != nil {
				return nil, err
			}
			return nil, createErr
		}
		id = Int(Obj(created)["id"])
		if id < 1 {
			return nil, &RemoteError{Code: "DEVICE_CREATE_RESULT_UNKNOWN", Uncertain: true}
		}
		// Reserve the returned ID immediately, even if the following refresh fails.
		if occupied[id] || st.Profiles[strconv.FormatInt(id, 10)] != "" {
			return nil, errors.New("DEVICE_OWNER_CONFLICT")
		}
		if err = e.Store.Update(func(s *State) error {
			s.Profiles[strconv.FormatInt(id, 10)] = owner
			s.Intents[owner] = Intent{QQ: qq, PID: pid, Device: id, Phase: "reserved", At: time.Now().Unix()}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	// Other tools can mutate the framework while we allocate. Recheck all visible instances.
	freshDevices, readErr := e.dir()
	if readErr != nil {
		return nil, readErr
	}
	for _, other := range freshDevices.Bots {
		if other.Device == id {
			return nil, errors.New("DEVICE_OWNER_CONFLICT")
		}
		if other.QQ == qq && other.PID == pid {
			return nil, &RemoteError{Code: "CREATE_RESULT_UNKNOWN", Uncertain: true}
		}
	}
	if err = e.Store.Update(func(s *State) error {
		s.Profiles[strconv.FormatInt(id, 10)] = owner
		s.Intents[owner] = Intent{QQ: qq, PID: pid, Device: id, Phase: "sending", At: time.Now().Unix()}
		return nil
	}); err != nil {
		return nil, err
	}
	p := selector(qq, pid)
	p["password"] = password
	p["device_profile_id"] = id
	p["nt"] = 1
	_, err = Call(e.API, "add_account", p)
	if err != nil {
		if !uncertain(err) {
			// A definitive rejection may be retried after fixing permissions; retain the device claim.
			if saveErr := e.Store.Update(func(s *State) error { delete(s.Intents, owner); return nil }); saveErr != nil {
				return nil, saveErr
			}
		}
		return nil, err
	}
	fresh, err := e.dir()
	if err != nil {
		return nil, err
	}
	b, err = findConfiguredBot(fresh, qq, kind, &pid)
	if err != nil || b == nil || b.Device != id || b.PID != pid {
		return nil, &RemoteError{Code: "CREATE_RESULT_UNKNOWN", Uncertain: true}
	}
	for _, other := range fresh.Bots {
		if other.Device == id && (other.QQ != qq || other.PID != pid) {
			return nil, errors.New("DEVICE_OWNER_CONFLICT")
		}
	}
	if err = e.Store.Update(func(s *State) error {
		delete(s.Intents, owner)
		if kind == "pad" {
			s.Initialized[owner] = true
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return b, nil
}
func taskSettings(v any) (Object, []string, []string, error) {
	m := Obj(v)
	settings := Obj(m["settings"])
	if settings == nil || settings["schedule_enabled"] == nil {
		return nil, nil, nil, errors.New("TASK_SETTINGS_SCHEMA")
	}
	return settings, stringsList(m["supported_tasks"]), stringsList(settings["selected_tasks"]), nil
}
func (e *Engine) settings(qq, pid int64) (Object, []string, []string, error) {
	p := selector(qq, pid)
	p["operation"] = "status"
	v, err := Call(e.API, "level_task_management", p)
	if err != nil {
		return nil, nil, nil, err
	}
	return taskSettings(v)
}
func (e *Engine) savePlan(qq, pid int64, settings Object, tasks []string) error {
	p := selector(qq, pid)
	p["operation"] = "settings"
	p["tasks"] = tasks
	p["schedule_enabled"] = Bool(settings["schedule_enabled"])
	p["schedule_time"] = Str(settings["schedule_time"])
	if p["schedule_time"] == "" {
		p["schedule_time"] = "04:00"
	}
	if id := Int(settings["pc_protocol_id"]); id >= 0 {
		p["pc_protocol_id"] = id
	}
	_, err := Call(e.API, "level_task_management", p)
	return err
}
func (e *Engine) initialize(qq, pid int64) error {
	owner := key(qq, pid)
	if !e.Store.Snapshot().Initialized[owner] {
		return nil
	}
	s, supported, _, err := e.settings(qq, pid)
	if err != nil {
		return err
	}
	if len(supported) == 0 {
		return nil
	}
	if err = e.savePlan(qq, pid, s, supported); err != nil {
		return err
	}
	return e.Store.Update(func(st *State) error { delete(st.Initialized, owner); return nil })
}
func (e *Engine) showProjects(qq int64) error {
	b, err := e.requirePad(qq)
	if err != nil {
		return err
	}
	if err = e.initialize(qq, b.PID); err != nil {
		return err
	}
	_, supported, selected, err := e.settings(qq, b.PID)
	if err != nil {
		return err
	}
	var out strings.Builder
	out.WriteString("项目开关\n-------\n")
	for _, s := range supported {
		state := "关闭❌"
		if includes(selected, s) {
			state = "开启✅"
		}
		fmt.Fprintf(&out, "%s-%s\n", s, state)
	}
	out.WriteString("------\n发送：项目名称 开启\n或：项目名称 关闭\n例如：电脑QQ在线 开启")
	return e.send(qq, out.String())
}
func (e *Engine) toggle(qq int64, title string, on bool) error {
	b, err := e.requirePad(qq)
	if err != nil {
		return err
	}
	if err = e.initialize(qq, b.PID); err != nil {
		return err
	}
	s, supported, selected, err := e.settings(qq, b.PID)
	if err != nil {
		return err
	}
	if !includes(supported, title) {
		return e.send(qq, "项目名称不匹配，请发送“项目开关”复制完整名称。")
	}
	next := []string{}
	for _, v := range selected {
		if v != title && includes(supported, v) {
			next = append(next, v)
		}
	}
	if on {
		next = append(next, title)
	}
	if err = e.savePlan(qq, b.PID, s, next); err != nil {
		return err
	}
	state := "关闭"
	if on {
		state = "开启"
	}
	return e.send(qq, title+" 已"+state)
}
func collectTasks(v any, out map[string][]Object) {
	switch value := v.(type) {
	case map[string]any:
		if title := Str(value["title"]); title != "" {
			out[title] = append(out[title], value)
		}
		for _, v := range value {
			collectTasks(v, out)
		}
	case []any:
		for _, v := range value {
			collectTasks(v, out)
		}
	}
}
func canRepairTask(rows []Object, today string) bool {
	incomplete := false
	for _, row := range rows {
		done, hasDone := row["is_done"].(bool)
		status := row["status"]
		switch Str(status) {
		case "1", "completed", "confirmed":
			return false
		}
		if done || Int(status) == 1 || Bool(row["is_paid_task"]) {
			return false
		}
		// A sign-in can be complete on a day with no acceleration reward.
		if Bool(row["action_done"]) {
			date := Str(row["action_done_date"])
			if date == today || date == "" {
				return false
			}
		}
		for _, key := range []string{"available", "can_execute", "executable"} {
			if allowed, ok := row[key].(bool); ok && !allowed {
				return false
			}
		}
		incomplete = incomplete || hasDone || Int(status) == 0 || Str(status) == "0"
	}
	return incomplete
}
func (e *Engine) repair(qq int64) error {
	b, err := e.requirePad(qq)
	if err != nil {
		return err
	}
	if err = e.initialize(qq, b.PID); err != nil {
		return err
	}
	_, _, selected, err := e.settings(qq, b.PID)
	if err != nil {
		return err
	}
	if len(selected) == 0 {
		return e.send(qq, "没有已开启项目，请先发送“项目开关”。")
	}
	p := selector(qq, b.PID)
	p["operation"] = "panel"
	p["refresh"] = true
	v, err := Call(e.API, "level_task_management", p)
	if err != nil {
		return err
	}
	panel := Obj(v)
	if Int(Obj(panel["payload"])["uin"]) != qq && Str(Obj(panel["payload"])["uin"]) != strconv.FormatInt(qq, 10) {
		return errors.New("PANEL_IDENTITY_MISMATCH")
	}
	tasks := map[string][]Object{}
	collectTasks(panel["payload"], tasks)
	state := Obj(panel["state"])
	collectTasks(state["membership_tasks"], tasks)
	supported := stringsList(state["supported_tasks"])
	today := time.Now().In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format(time.DateOnly)
	batch := []string{}
	pc := false
	for _, s := range selected {
		if s == "电脑QQ在线" {
			pc = true
			continue
		}
		if !includes(supported, s) || !canRepairTask(tasks[s], today) {
			continue
		}
		batch = append(batch, s)
	}
	sort.Strings(batch)
	if len(batch) > 100 {
		return errors.New("TASK_BATCH_LIMIT")
	}
	if len(batch) == 0 {
		message := "当前没有可提交的未完成项目。"
		if pc {
			message += "电脑上线请发送“电脑登录”。"
		}
		return e.send(qq, message)
	}
	now := time.Now().Unix()
	if now-e.Store.Snapshot().LastBatch[strconv.FormatInt(qq, 10)] < 30 {
		return e.send(qq, "刚提交过补挂，请至少30秒后再查询、补挂，避免重复请求。")
	}
	if err = e.Store.Update(func(s *State) error { s.LastBatch[strconv.FormatInt(qq, 10)] = now; return nil }); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = e.API.Submit(ctx, "execute_level_tasks", Object{"self_id": qq, "protocol_id": b.PID, "tasks": batch}); err != nil {
		return err
	}
	message := fmt.Sprintf("已提交%d项补挂：\n%s\n提交不代表任务已完成，下次补挂会重新查询实际状态。", len(batch), strings.Join(batch, "\n"))
	if pc {
		message += "\n电脑上线请发送“电脑登录”。"
	}
	return e.send(qq, message)
}

// Expire ephemeral verification material even when a user never sends another message.
func (e *Engine) Sweep() {
	e.mu.Lock()
	defer e.mu.Unlock()
	at := time.Now()
	for qq, t := range e.last {
		if at.Sub(t) > 24*time.Hour {
			delete(e.last, qq)
		}
	}
	for qq, f := range e.flows {
		if at.After(f.Expires) {
			delete(e.flows, qq)
		}
	}
}

func (e *Engine) VerifyBot() error {
	e.configGate.RLock()
	defer e.configGate.RUnlock()
	_, err := e.bot()
	return err
}
