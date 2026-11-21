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

const Version = "0.3.6"

var Actions = []string{"get_account_management_context", "get_device_profile_list", "create_device_profile", "send_friend_msg", "upload_friend_image", "file_transfer", "add_account", "update_account", "offline_account", "login_account", "query_login_qr", "scan_qr", "auth_qr", "get_security_verify_methods", "get_security_verify_qr", "query_security_verify_qr", "get_sms", "check_sms", "submit_identity_phone", "confirm_identity_sms", "level_task_management", "execute_level_tasks"}
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
	Code          string
	Status        string
	RequiredLevel int64
	CurrentLevel  int64
	Uncertain     bool
}

func (e *RemoteError) Error() string { return e.Code }
func uncertain(err error) bool       { var e *RemoteError; return errors.As(err, &e) && e.Uncertain }
func Friendly(err error) string {
	id, values := errorReply(err)
	return renderReply(nil, id, values...)
}

func errorReply(err error) (string, []string) {
	var e *RemoteError
	if errors.As(err, &e) {
		switch e.Code {
		case "DEVICE_CREATE_RESULT_UNKNOWN":
			return "error_device_pending", nil
		case "TIER_CAPABILITY_FORBIDDEN":
			return "error_tier", nil
		case "QQ_LEVEL_TOO_LOW":
			if e.RequiredLevel > 0 && e.CurrentLevel >= 0 {
				return "error_level", []string{
					"当前等级", strconv.FormatInt(e.CurrentLevel, 10),
					"所需等级", strconv.FormatInt(e.RequiredLevel, 10),
				}
			}
			return "error_level_generic", nil
		case "QQ_LEVEL_UNKNOWN":
			return "error_level_unknown", nil
		case "ACCOUNT_OFFLINE":
			return "error_offline", nil
		case "AUTHORIZATION_UNAVAILABLE":
			return "error_auth", nil
		}
		if e.Status == "restricted" || e.Code == "restricted" {
			return "error_restricted", nil
		}
		if e.Uncertain {
			return "error_uncertain", nil
		}
	}
	return "error_generic", nil
}
func Obj(v any) Object { m, _ := v.(map[string]any); return m }
func Arr(v any) []any  { a, _ := v.([]any); return a }
func Str(v any) string { s, _ := v.(string); return s }
func Int(v any) int64 {
	switch n := v.(type) {
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i
		}
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
				r.err = &RemoteError{
					Code:          code,
					Status:        Str(m["status"]),
					RequiredLevel: Int(m["requiredLevel"]),
					CurrentLevel:  Int(m["currentLevel"]),
				}
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
func key(qq, pid int64) string { return fmt.Sprintf("%d:%d", qq, pid) }
