package levelup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedRuntimeConfigUsesPersistentDirectory(t *testing.T) {
	base := t.TempDir()
	data := t.TempDir()
	connection := filepath.Join(t.TempDir(), "connection.json")
	defaults := []byte(`{"bot_qq":0,"data_dir":"data"}`)
	if err := os.WriteFile(filepath.Join(base, "config.json"), defaults, 0400); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(ManagedRuntime{Schema: 1, Transport: "native-ipc-v1", FrameworkAPI: "3.0.0", InstanceID: "instance-1", DataDirectory: data})
	if err := os.WriteFile(connection, raw, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := ReadManagedRuntime(connection, "")
	if err != nil {
		t.Fatal(err)
	}
	path, err := PrepareManagedConfig(base, r)
	if err != nil || path != filepath.Join(data, "config.json") {
		t.Fatal(path, err)
	}
	saved := []byte(`{"bot_qq":12345678}`)
	if err = os.WriteFile(path, saved, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = PrepareManagedConfig(base, r); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(saved) {
		t.Fatal("managed config overwritten")
	}
	got, _ = os.ReadFile(filepath.Join(base, "config.json"))
	if string(got) != string(defaults) {
		t.Fatal("immutable defaults changed")
	}
}
func TestManagedRuntimeRejectsInvalidIdentityAndRelativeData(t *testing.T) {
	p := filepath.Join(t.TempDir(), "connection.json")
	for _, raw := range []string{`{}`, `{"schema":1,"transport":"native-ipc-v1","framework_api":"3.0.0","instance_id":"../bad","data_directory":"/data"}`, `{"schema":1,"transport":"native-ipc-v1","framework_api":"3.0.0","instance_id":"good","data_directory":"data"}`} {
		if err := os.WriteFile(p, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadManagedRuntime(p, "/fallback"); err == nil {
			t.Fatal("invalid connection accepted")
		}
	}
	if _, err := ReadManagedRuntime("", "relative"); err == nil {
		t.Fatal("relative fallback accepted")
	}
}
func TestReadyMustMatchControlledInstance(t *testing.T) {
	for _, identity := range []string{"wrong", "expected"} {
		ipc := NewIPC(&strings.Builder{})
		ipc.InstanceID = "expected"
		raw := `{"type":"ready","transport":"native-ipc-v1","api_version":"1","framework_api":"3.0.0","plugin_id":"laoluo-levelup","instance_id":"` + identity + `"}` + "\n"
		err := ipc.Read(strings.NewReader(raw))
		if identity == "wrong" {
			if err == nil {
				t.Fatal("wrong instance accepted")
			}
			select {
			case <-ipc.Ready:
				t.Fatal("ready exposed before validation")
			default:
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-ipc.Ready:
			default:
				t.Fatal("matching ready rejected")
			}
		}
	}
}
