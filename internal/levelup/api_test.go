package levelup

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestIPCHandshakeCorrelatesOutOfOrderAndKeepsErrorsPrivate(t *testing.T) {
	toHostR, toHostW := io.Pipe()
	fromHostR, fromHostW := io.Pipe()
	defer toHostR.Close()
	defer toHostW.Close()
	defer fromHostR.Close()
	defer fromHostW.Close()
	ipc := NewIPC(toHostW)
	go ipc.Read(fromHostR)
	frameworkDone := make(chan error, 1)
	go func() {
		r := bufio.NewScanner(toHostR)
		if !r.Scan() {
			frameworkDone <- io.EOF
			return
		}
		hello, err := decode(r.Bytes())
		if err != nil || Str(hello["type"]) != "hello" || Str(hello["plugin_id"]) != "laoluo-levelup" {
			frameworkDone <- errors.New("wrong hello")
			return
		}
		_, _ = io.WriteString(fromHostW, `{"type":"ready","transport":"native-ipc-v1","api_version":"1","framework_api":"3.0.0","plugin_id":"laoluo-levelup"}`+"\n")
		requests := []Object{}
		for len(requests) < 2 && r.Scan() {
			m, err := decode(r.Bytes())
			if err != nil {
				frameworkDone <- err
				return
			}
			requests = append(requests, m)
		}
		for i := len(requests) - 1; i >= 0; i-- {
			q := requests[i]
			reply := Object{"type": "action_result", "id": q["id"], "ok": true, "data": q["action"]}
			raw, _ := json.Marshal(reply)
			_, _ = fromHostW.Write(append(raw, '\n'))
		}
		frameworkDone <- nil
	}()
	if err := ipc.Hello(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ipc.Ready:
	case <-time.After(time.Second):
		t.Fatal("ready not received")
	}
	ch := make(chan string, 2)
	for _, a := range []string{"get_plugin_context", "get_device_profile_list"} {
		go func(a string) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			data, err := ipc.Call(ctx, a, Object{})
			if err != nil {
				ch <- "error"
			} else {
				ch <- Str(data)
			}
		}(a)
	}
	got := map[string]bool{<-ch: true, <-ch: true}
	if !got["get_plugin_context"] || !got["get_device_profile_list"] {
		t.Fatal("responses mismatched")
	}
	if err := <-frameworkDone; err != nil {
		t.Fatal(err)
	}
}
func TestIPCPublicAllowlistTimeoutAndSubmitContract(t *testing.T) {
	var out strings.Builder
	ipc := NewIPC(&out)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := ipc.Call(ctx, "private_admin_create_device", Object{}); err == nil || out.Len() != 0 {
		t.Fatal("undocumented request escaped")
	}
	if _, err := ipc.Call(ctx, "login_account", Object{"self_id": user, "protocol_id": int64(0)}); !uncertain(err) {
		t.Fatal("timeout not uncertain")
	}
	before := out.Len()
	if err := ipc.Submit(context.Background(), "send_friend_msg", Object{}); err == nil || out.Len() != before {
		t.Fatal("message allowed without confirmation")
	}
	if err := ipc.Submit(context.Background(), "execute_level_tasks", Object{"tasks": []string{"签到"}}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	last, _ := decode([]byte(lines[len(lines)-1]))
	if _, has := last["id"]; has {
		t.Fatal("submit-only requested a reply")
	}
	if strings.Contains(Friendly(&RemoteError{Code: "password=secret"}), "secret") {
		t.Fatal("remote details leaked")
	}
}
func TestIPCExactIntegerSelectorsAndUnknownEvents(t *testing.T) {
	m, err := decode([]byte(`{"self_id":1234567890,"message_id":9007199254740993,"msg":{"uid":"18446744073709551615"}}`))
	if err != nil || Int(m["message_id"]) != 9007199254740993 || Str(Obj(m["msg"])["uid"]) != "18446744073709551615" {
		t.Fatal("large identifiers rounded")
	}
}

func TestFrameworkCapabilitiesRequireDeviceCreation(t *testing.T) {
	actions := []any{}
	for _, a := range Actions {
		actions = append(actions, a)
	}
	ctx := Object{"management_api_version": int64(2), "scope": "framework", "account_selector": "self_id+protocol_id", "available_actions": actions}
	if err := ValidatePluginContext(ctx); err != nil {
		t.Fatal(err)
	}
	ctx["scope"] = "node"
	if ValidatePluginContext(ctx) == nil {
		t.Fatal("legacy scope accepted")
	}
	ctx["scope"] = "framework"
	filtered := []any{}
	for _, a := range actions {
		if a != "create_device_profile" {
			filtered = append(filtered, a)
		}
	}
	ctx["available_actions"] = filtered
	if ValidatePluginContext(ctx) == nil {
		t.Fatal("missing create permission accepted")
	}
}
func TestIPCRestrictionStatusPreservedAndPrivate(t *testing.T) {
	var out strings.Builder
	ipc := NewIPC(&out)
	ipc.pending["fixture"] = make(chan response, 1)
	ch := ipc.pending["fixture"]
	if err := ipc.Read(strings.NewReader(`{"type":"action_result","id":"fixture","ok":false,"code":"NEW_RESTRICTION","status":"restricted","error":"private-password"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	r := <-ch
	if r.err == nil || !strings.Contains(Friendly(r.err), "32") || strings.Contains(Friendly(r.err), "private-password") {
		t.Fatal("restriction not safely explained")
	}
}
