package levelup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/pbkdf2"
)

//go:embed web.html
var webPage string

type webAuthFile struct {
	SetupHash    string `json:"setup_hash,omitempty"`
	Salt         string `json:"salt,omitempty"`
	PasswordHash string `json:"password_hash,omitempty"`
}
type webSession struct {
	CSRF    string
	Expires time.Time
}
type loginAttempt struct {
	Count int
	Since time.Time
}
type AdminWeb struct {
	Engine              *Engine
	ConfigPath, DataDir string
	InitialCode         string
	mu                  sync.Mutex
	auth                webAuthFile
	sessions            map[string]webSession
	attempts            map[string]loginAttempt
	ready               bool
	note                string
	cacheMu             sync.Mutex
	cache               Object
	cacheAt             time.Time
}

func randomString() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic("system random unavailable")
	}
	return hex.EncodeToString(b)
}
func digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func same(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func passwordHash(password, salt string) string {
	return hex.EncodeToString(pbkdf2.Key([]byte(password), []byte(salt), 120000, 32, sha256.New))
}
func validPassword(s string) bool { return len(s) >= 10 && len(s) <= 128 && strings.TrimSpace(s) != "" }
func NewAdminWeb(e *Engine, path, dir string) (*AdminWeb, error) {
	a := &AdminWeb{Engine: e, ConfigPath: path, DataDir: dir, sessions: map[string]webSession{}, attempts: map[string]loginAttempt{}, note: "等待框架连接"}
	data, err := os.ReadFile(filepath.Join(dir, "web-auth.json"))
	if os.IsNotExist(err) {
		code := randomString()
		a.InitialCode = code
		a.auth.SetupHash = digest(code)
		if err = a.saveAuth(a.auth); err != nil {
			return nil, err
		}
		if err = atomicFile(filepath.Join(dir, "web-first-access.txt"), []byte("laoluo-levelup 首次网页初始化码（使用一次后失效）：\n"+code+"\n")); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else {
		if err = json.Unmarshal(data, &a.auth); err != nil {
			return nil, errors.New("WEB_AUTH_CORRUPT")
		}
		if a.auth.SetupHash == "" && (a.auth.Salt == "" || a.auth.PasswordHash == "") {
			return nil, errors.New("WEB_AUTH_CORRUPT")
		}
	}
	return a, nil
}
func (a *AdminWeb) saveAuth(v webAuthFile) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return atomicFile(filepath.Join(a.DataDir, "web-auth.json"), raw)
}
func (a *AdminWeb) SetReady(ready bool, note string) {
	a.mu.Lock()
	a.ready = ready
	a.note = note
	a.mu.Unlock()
}
func (a *AdminWeb) write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (a *AdminWeb) fail(w http.ResponseWriter, status int, msg string) {
	a.write(w, status, Object{"ok": false, "message": msg})
}
func (a *AdminWeb) body(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Method != "POST" {
		a.fail(w, 405, "请使用POST请求")
		return false
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		a.fail(w, 415, "请求格式不正确")
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
			a.fail(w, 403, "请求来源不匹配")
			return false
		}
	}
	limit := int64(16 << 10)
	if r.URL.Path == "/api/config" {
		limit = 512 << 10
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		a.fail(w, 400, "参数格式不正确")
		return false
	}
	return true
}
func (a *AdminWeb) session(r *http.Request) (webSession, bool) {
	cookie, err := r.Cookie("laoluo_admin")
	if err != nil {
		return webSession{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	v, ok := a.sessions[digest(cookie.Value)]
	if !ok || time.Now().After(v.Expires) {
		delete(a.sessions, digest(cookie.Value))
		return webSession{}, false
	}
	return v, true
}
func (a *AdminWeb) secure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	return (adminSocketRequest(r) || ip != nil && ip.IsLoopback()) && r.Header.Get("X-Forwarded-Proto") == "https"
}
func (a *AdminWeb) newSession(w http.ResponseWriter, r *http.Request) webSession {
	token := randomString()
	v := webSession{CSRF: randomString(), Expires: time.Now().Add(8 * time.Hour)}
	a.mu.Lock()
	for k, s := range a.sessions {
		if time.Now().After(s.Expires) {
			delete(a.sessions, k)
		}
	}
	if len(a.sessions) > 32 {
		a.sessions = map[string]webSession{}
	}
	a.sessions[digest(token)] = v
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "laoluo_admin", Value: token, Path: "/", HttpOnly: true, Secure: a.secure(r), SameSite: http.SameSiteStrictMode, MaxAge: 8 * 3600})
	return v
}
func (a *AdminWeb) rateAllowed(r *http.Request) bool {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if adminSocketRequest(r) {
		if ip := net.ParseIP(r.Header.Get("X-Real-IP")); ip != nil {
			host = ip.String()
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for k, v := range a.attempts {
		if now.Sub(v.Since) > 5*time.Minute {
			delete(a.attempts, k)
		}
	}
	v := a.attempts[host]
	if v.Since.IsZero() {
		if len(a.attempts) >= 256 {
			return false
		}
		v.Since = now
	}
	if v.Count >= 5 {
		return false
	}
	v.Count++
	a.attempts[host] = v
	return true
}
func (a *AdminWeb) require(w http.ResponseWriter, r *http.Request, write bool) (webSession, bool) {
	s, ok := a.session(r)
	if !ok {
		a.fail(w, 401, "请先登录控制台")
		return s, false
	}
	if write && !same(r.Header.Get("X-CSRF-Token"), s.CSRF) {
		a.fail(w, 403, "页面已失效，请刷新后重试")
		return s, false
	}
	return s, true
}
func (a *AdminWeb) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
	switch r.URL.Path {
	case "/":
		if r.Method != "GET" {
			a.fail(w, 405, "不支持此请求")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(webPage))
	case "/api/session":
		if r.Method != "GET" {
			a.fail(w, 405, "不支持此请求")
			return
		}
		s, ok := a.session(r)
		a.mu.Lock()
		setup := a.auth.SetupHash != ""
		a.mu.Unlock()
		a.write(w, 200, Object{"authenticated": ok, "setup_required": setup, "csrf": s.CSRF, "version": Version})
	case "/api/setup":
		var input struct {
			Code     string `json:"code"`
			Password string `json:"password"`
		}
		if !a.body(w, r, &input) {
			return
		}
		if !a.rateAllowed(r) {
			a.fail(w, 429, "尝试次数过多，请5分钟后再试")
			return
		}
		if !validPassword(input.Password) {
			a.fail(w, 400, "管理密码需要10至128个字符")
			return
		}
		a.mu.Lock()
		if a.auth.SetupHash == "" || !same(digest(input.Code), a.auth.SetupHash) {
			a.mu.Unlock()
			a.fail(w, 403, "初始化码无效或已使用")
			return
		}
		salt := randomString()
		next := webAuthFile{Salt: salt, PasswordHash: passwordHash(input.Password, salt)}
		if err := a.saveAuth(next); err != nil {
			a.mu.Unlock()
			a.fail(w, 500, "访问配置保存失败")
			return
		}
		a.auth = next
		a.InitialCode = ""
		a.mu.Unlock()
		_ = os.Remove(filepath.Join(a.DataDir, "web-first-access.txt"))
		s := a.newSession(w, r)
		a.write(w, 200, Object{"ok": true, "csrf": s.CSRF})
	case "/api/login":
		var input struct {
			Password string `json:"password"`
		}
		if !a.body(w, r, &input) {
			return
		}
		if !a.rateAllowed(r) {
			a.fail(w, 429, "尝试次数过多，请5分钟后再试")
			return
		}
		a.mu.Lock()
		auth := a.auth
		a.mu.Unlock()
		if auth.PasswordHash == "" || len(input.Password) > 128 || !same(passwordHash(input.Password, auth.Salt), auth.PasswordHash) {
			a.fail(w, 403, "管理密码不正确")
			return
		}
		s := a.newSession(w, r)
		a.write(w, 200, Object{"ok": true, "csrf": s.CSRF})
	case "/api/logout":
		if _, ok := a.require(w, r, true); !ok {
			return
		}
		var input struct{}
		if !a.body(w, r, &input) {
			return
		}
		if c, err := r.Cookie("laoluo_admin"); err == nil {
			a.mu.Lock()
			delete(a.sessions, digest(c.Value))
			a.mu.Unlock()
		}
		http.SetCookie(w, &http.Cookie{Name: "laoluo_admin", Value: "", Path: "/", HttpOnly: true, Secure: a.secure(r), SameSite: http.SameSiteStrictMode, MaxAge: -1})
		a.write(w, 200, Object{"ok": true})
	case "/api/password":
		if _, ok := a.require(w, r, true); !ok {
			return
		}
		var input struct {
			Old string `json:"old_password"`
			New string `json:"new_password"`
		}
		if !a.body(w, r, &input) {
			return
		}
		if !a.rateAllowed(r) {
			a.fail(w, 429, "尝试次数过多，请稍后重试")
			return
		}
		if !validPassword(input.New) {
			a.fail(w, 400, "新密码需要10至128个字符")
			return
		}
		a.mu.Lock()
		if !same(passwordHash(input.Old, a.auth.Salt), a.auth.PasswordHash) {
			a.mu.Unlock()
			a.fail(w, 403, "当前管理密码不正确")
			return
		}
		salt := randomString()
		next := webAuthFile{Salt: salt, PasswordHash: passwordHash(input.New, salt)}
		if err := a.saveAuth(next); err != nil {
			a.mu.Unlock()
			a.fail(w, 500, "密码保存失败")
			return
		}
		a.auth = next
		a.sessions = map[string]webSession{}
		a.mu.Unlock()
		s := a.newSession(w, r)
		a.write(w, 200, Object{"ok": true, "csrf": s.CSRF})
	case "/api/status":
		if r.Method != "GET" {
			a.fail(w, 405, "不支持此请求")
			return
		}
		if _, ok := a.require(w, r, false); !ok {
			return
		}
		a.write(w, 200, a.status())
	case "/api/reply-templates":
		if r.Method != "GET" {
			a.fail(w, 405, "不支持此请求")
			return
		}
		if _, ok := a.require(w, r, false); !ok {
			return
		}
		a.write(w, 200, Object{"templates": replyCatalog})
	case "/api/project-catalog":
		a.projectCatalog(w, r)
	case "/api/config":
		if _, ok := a.require(w, r, true); !ok {
			return
		}
		var c Config
		if !a.body(w, r, &c) {
			return
		}
		a.mu.Lock()
		ready := a.ready
		a.mu.Unlock()
		if !ready {
			a.fail(w, 409, "框架尚未连接，暂时无法核对机器人配置")
			return
		}
		if err := a.Engine.SaveConfig(a.ConfigPath, c); err != nil {
			a.fail(w, 400, err.Error())
			return
		}
		a.cacheMu.Lock()
		a.cache = nil
		a.cacheMu.Unlock()
		a.write(w, 200, Object{"ok": true, "message": "配置已保存并即时生效", "config": a.Engine.CurrentConfig()})
	default:
		a.fail(w, 404, "页面不存在")
	}
}

func (a *AdminWeb) projectCatalog(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.require(w, r, true); !ok {
		return
	}
	var input struct {
		QQ  int64  `json:"qq"`
		PID *int64 `json:"protocol_id"`
	}
	if !a.body(w, r, &input) {
		return
	}
	if input.QQ < 10000 || input.PID == nil || *input.PID < 0 {
		a.fail(w, 400, "请选择一个在线的 NT Pad 账号")
		return
	}
	a.mu.Lock()
	ready := a.ready
	a.mu.Unlock()
	if !ready {
		a.fail(w, 409, "框架尚未连接，可先手动添加项目映射")
		return
	}
	d, err := a.Engine.dir()
	if err != nil {
		a.fail(w, 502, "暂时无法查询框架账号，可先手动添加项目映射")
		return
	}
	b, err := findConfiguredBot(d, input.QQ, "pad", input.PID)
	if err != nil || b == nil || b.Status != 1 || b.NT != 1 {
		a.fail(w, 400, "请选择一个在线的 NT Pad 账号")
		return
	}
	_, names, _, err := a.Engine.settings(b.QQ, b.PID)
	if err != nil {
		a.fail(w, 502, Friendly(err)+" 可手动添加项目映射。")
		return
	}
	a.write(w, 200, Object{"names": names})
}

func (a *AdminWeb) status() Object {
	a.mu.Lock()
	ready, note := a.ready, a.note
	a.mu.Unlock()
	c := a.Engine.CurrentConfig()
	a.Engine.mu.Lock()
	flows := len(a.Engine.flows)
	a.Engine.mu.Unlock()
	result := Object{"version": Version, "ready": ready, "note": note, "config": c, "active_sessions": flows, "bot_state": "unconfigured"}
	if !ready {
		return result
	}
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	if a.cache != nil && time.Since(a.cacheAt) < 3*time.Second {
		for k, v := range a.cache {
			result[k] = v
		}
		return result
	}
	d, err := a.Engine.dir()
	if err != nil {
		result["note"] = "框架状态暂不可读"
		result["bot_state"] = "unknown"
		return result
	}
	bots := []Object{}
	for _, b := range d.Bots {
		if b.Kind == "pad" && b.NT == 1 {
			bots = append(bots, Object{"qq": b.QQ, "protocol_id": b.PID, "online": b.Status == 1})
		}
	}
	protocols := []Object{}
	for _, v := range d.Protocols {
		p := Obj(v)
		if Bool(p["state"]) {
			typ := strings.ToLower(Str(p["type"]))
			if typ == "pad" || typ == "linux" {
				protocols = append(protocols, Object{"id": Int(p["id"]), "type": typ, "version": Str(p["version"])})
			}
		}
	}
	meta := Object{"bots": bots, "protocols": protocols, "account_count": len(d.Bots), "bot_state": "unconfigured"}
	if c.BotQQ != 0 {
		b, err := findConfiguredBot(d, c.BotQQ, "pad", c.BotProtocolID)
		state := "missing"
		if err == nil && b != nil {
			state = "offline"
			if b.Status == 1 && b.NT == 1 {
				state = "online"
			}
		}
		meta["bot_state"] = state
	}
	a.cache = meta
	a.cacheAt = time.Now()
	for k, v := range meta {
		result[k] = v
	}
	return result
}
func StartAdminServer(a *AdminWeb, address string) (*http.Server, net.Listener, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, nil, fmt.Errorf("网页端口监听失败：%w", err)
	}
	return serveAdmin(a, listener), listener, nil
}

const AdminSocketDirectory = "admin-web"
const AdminSocketFilename = "admin.sock"

// StartAdminUnixServer runs under the same instance lock as the TCP listener.
func StartAdminUnixServer(a *AdminWeb) (*http.Server, string, error) {
	dir := filepath.Join(a.DataDir, AdminSocketDirectory)
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return nil, "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, "", err
	}
	if !info.IsDir() {
		return nil, "", errors.New("管理入口目录必须是独立目录")
	}
	socketPath := filepath.Join(dir, AdminSocketFilename)
	if info, err := os.Lstat(socketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, "", errors.New("管理入口路径已被非 Socket 文件占用")
		}
		if err := os.Remove(socketPath); err != nil {
			return nil, "", err
		}
	} else if !os.IsNotExist(err) {
		return nil, "", err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, "", err
	}
	// The administrator can assign a dedicated group on the parent directory.
	if err := os.Chmod(socketPath, 0660); err != nil {
		_ = listener.Close()
		return nil, "", err
	}
	return serveAdmin(a, listener), socketPath, nil
}

type adminSocketContextKey struct{}

func adminSocketRequest(r *http.Request) bool {
	isSocket, _ := r.Context().Value(adminSocketContextKey{}).(bool)
	return isSocket
}

func serveAdmin(a *AdminWeb, listener net.Listener) *http.Server {
	server := &http.Server{
		Handler: a, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 55 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384,
	}
	if listener.Addr().Network() == "unix" {
		server.ConnContext = func(ctx context.Context, _ net.Conn) context.Context {
			return context.WithValue(ctx, adminSocketContextKey{}, true)
		}
	}
	go func() { _ = server.Serve(listener) }()
	return server
}
