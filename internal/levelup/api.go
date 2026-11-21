package levelup

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type Object = map[string]any

const Version = "0.3.2"

var Actions = []string{"get_plugin_context", "get_account_management_context", "get_device_profile_list", "create_device_profile", "send_friend_msg", "upload_friend_image", "file_transfer", "add_account", "update_account", "offline_account", "login_account", "query_login_qr", "scan_qr", "auth_qr", "get_security_verify_methods", "get_security_verify_qr", "query_security_verify_qr", "get_sms", "check_sms", "submit_identity_phone", "confirm_identity_sms", "level_task_management", "execute_level_tasks"}
var allowed = func() map[string]bool {
	m := map[string]bool{}
	for _, s := range Actions {
		m[s] = true
	}
	return m
}()

type API interface {
	Call(context.Context, string, Object) (any, error)
	Submit(context.Context, string, Object) error
}
type RemoteError struct {
	Code      string
	Status    string
	Uncertain bool
}

func (e *RemoteError) Error() string { return e.Code }
func uncertain(err error) bool       { var e *RemoteError; return errors.As(err, &e) && e.Uncertain }
func Friendly(err error) string {
	var e *RemoteError
	if errors.As(err, &e) {
		if e.Status == "restricted" {
			return "框架拒绝了当前操作：请核对授权、账号在线状态及最新QQ等级。3.0.7的等级任务与扫码接口要求Plus最新等级至少32级或Pro；扫码受限时可到框架后台完成登录。"
		}
		switch e.Code {
		case "DEVICE_CREATE_RESULT_UNKNOWN":
			return "指纹创建结果尚未确认，已暂停自动建号，重启也不会重复创建。请联系管理员在框架后台核对设备与账号后再继续。"
		case "QQ_LEVEL_TOO_LOW", "QQ_LEVEL_UNKNOWN", "ACCOUNT_OFFLINE", "AUTHORIZATION_UNAVAILABLE", "restricted":
			return "当前账号等级、在线状态或框架授权不满足要求。3.0.7等级任务与扫码接口要求Plus最新等级至少32级或Pro；扫码受限时请到框架后台完成登录。"
		}
	}
	if uncertain(err) {
		return "本次结果尚未确认，请先发送“继续验证”核对；不会自动重复提交。"
	}
	return "操作未完成，请稍后核对。若持续失败，请联系插件管理员查看框架账号日志。"
}
func Obj(v any) Object { m, _ := v.(map[string]any); return m }
func Arr(v any) []any  { a, _ := v.([]any); return a }
func Str(v any) string { s, _ := v.(string); return s }
func Int(v any) int64 {
	switch n := v.(type) {
	case json.Number:
		i, _ := n.Int64()
		return i
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		if n == float64(int64(n)) {
			return int64(n)
		}
	}
	return -1
}
func Bool(v any) bool { b, _ := v.(bool); return b }
func decode(raw []byte) (Object, error) {
	var m Object
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	err := d.Decode(&m)
	return m, err
}

type response struct {
	data any
	err  error
}
type IPC struct {
	InstanceID string
	writer     io.Writer
	writeMu    sync.Mutex
	mu         sync.Mutex
	pending    map[string]chan response
	seq        atomic.Uint64
	Ready      chan struct{}
	Events     chan Object
	readyOnce  sync.Once
	done       chan struct{}
	closeOnce  sync.Once
}

func NewIPC(w io.Writer) *IPC {
	return &IPC{writer: w, pending: map[string]chan response{}, Ready: make(chan struct{}), Events: make(chan Object, 256), done: make(chan struct{})}
}
func (p *IPC) write(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	raw = append(raw, '\n')
	_, err = p.writer.Write(raw)
	return err
}
func (p *IPC) Hello() error {
	return p.write(Object{"type": "hello", "api_version": "1", "framework_api": "3.0.0", "plugin_id": "laoluo-levelup", "version": Version})
}
func (p *IPC) Read(r io.Reader) error {
	defer p.closeOnce.Do(func() { close(p.done) })
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 65536), 4*1024*1024)
	for scanner.Scan() {
		m, err := decode(scanner.Bytes())
		if err != nil {
			continue
		}
		switch Str(m["type"]) {
		case "ready":
			if Str(m["transport"]) != "native-ipc-v1" || Str(m["api_version"]) != "1" || Str(m["framework_api"]) != "3.0.0" || Str(m["plugin_id"]) != "laoluo-levelup" || p.InstanceID != "" && Str(m["instance_id"]) != p.InstanceID {
				return errors.New("IPC_READY_IDENTITY_MISMATCH")
			}
			p.readyOnce.Do(func() { close(p.Ready) })
		case "event":
			select {
			case p.Events <- Obj(m["data"]):
			default: /* Drop overload instead of unbounded goroutines. */
			}
		case "action_result":
			id := Str(m["id"])
			p.mu.Lock()
			ch := p.pending[id]
			delete(p.pending, id)
			p.mu.Unlock()
			if ch == nil {
				continue
			}
			r := response{data: m["data"]}
			if !Bool(m["ok"]) {
				code := Str(m["code"])
				if code == "" {
					code = Str(m["status"])
				}
				if code == "" {
					code = "REMOTE_REJECTED"
				}
				r.err = &RemoteError{Code: code, Status: Str(m["status"])}
			}
			ch <- r
		}
	}
	return scanner.Err()
}
func (p *IPC) Call(ctx context.Context, action string, params Object) (any, error) {
	if !allowed[action] {
		return nil, errors.New("ACTION_NOT_DOCUMENTED")
	}
	id := "laoluo-" + strconv.FormatUint(p.seq.Add(1), 10)
	ch := make(chan response, 1)
	p.mu.Lock()
	p.pending[id] = ch
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.pending, id); p.mu.Unlock() }()
	if err := p.write(Object{"type": "action", "id": id, "action": action, "params": params}); err != nil {
		return nil, &RemoteError{Code: "IPC_WRITE_UNKNOWN", Uncertain: true}
	}
	select {
	case r := <-ch:
		return r.data, r.err
	case <-ctx.Done():
		return nil, &RemoteError{Code: "IPC_TIMEOUT", Uncertain: true}
	case <-p.done:
		return nil, &RemoteError{Code: "IPC_CLOSED", Uncertain: true}
	}
}
func (p *IPC) Submit(ctx context.Context, action string, params Object) error {
	if action != "execute_level_tasks" {
		return errors.New("SUBMIT_ONLY_TASK_BATCH")
	}
	// No reply ID: the public protocol explicitly permits fire-and-forget actions.
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if err := p.write(Object{"type": "action", "action": action, "params": params}); err != nil {
		return &RemoteError{Code: "IPC_WRITE_UNKNOWN", Uncertain: true}
	}
	return nil
}
func Call(api API, action string, params Object) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return api.Call(ctx, action, params)
}
func number(s string) int64    { v, _ := strconv.ParseInt(s, 10, 64); return v }
func key(qq, pid int64) string { return fmt.Sprintf("%d:%d", qq, pid) }

// ValidatePluginContext uses management capabilities, not the IPC protocol version.
func ValidatePluginContext(ctx Object) error {
	if Int(ctx["management_api_version"]) != 2 || Str(ctx["scope"]) != "framework" || Str(ctx["account_selector"]) != "self_id+protocol_id" {
		return errors.New("当前框架插件API不兼容，需要Mengka NT 3.0.7及framework范围")
	}
	available := map[string]bool{}
	for _, a := range Arr(ctx["available_actions"]) {
		available[Str(a)] = true
	}
	for _, a := range Actions {
		if !available[a] {
			return fmt.Errorf("框架未开放公开接口%s，请检查版本及插件权限", a)
		}
	}
	return nil
}
