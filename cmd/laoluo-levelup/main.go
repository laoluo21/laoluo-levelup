package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"laoluo-levelup/internal/levelup"
)

func main() {
	log.SetOutput(os.Stderr)
	log.SetFlags(0)
	exe, err := os.Executable()
	if err != nil {
		log.Fatal("cannot locate plugin executable")
	}
	base := filepath.Dir(exe)
	configPath := flag.String("config", "", "plugin config file")
	check := flag.Bool("check-config", false, "validate configuration without connecting")
	flag.Parse()
	managed, runtimeErr := levelup.ReadManagedRuntime(os.Getenv("MENGKA_PLUGIN_CONNECTION_FILE"), os.Getenv("MENGKA_PLUGIN_DATA_DIR"))
	if runtimeErr != nil {
		log.Fatal("框架受控连接文件无效，已停止。")
	}
	if managed.DataDirectory != "" {
		path, err := levelup.PrepareManagedConfig(base, managed)
		if err != nil {
			log.Fatal("框架分配的数据目录不可写，已停止。")
		}
		*configPath = path
	} else if *configPath == "" {
		*configPath = filepath.Join(base, "config.json")
	}
	absolute, absErr := filepath.Abs(*configPath)
	if absErr != nil {
		log.Fatal("无法定位配置文件。")
	}
	*configPath = absolute
	raw, err := os.ReadFile(*configPath)
	if err != nil {
		log.Fatal("请先编辑插件包内的config.json，设置bot_qq；配置文件无法读取。")
	}
	var config levelup.Config
	if err = json.Unmarshal(raw, &config); err != nil {
		log.Fatal("config.json格式不正确。")
	}
	if config.SessionMinutes == 0 {
		config.SessionMinutes = 15
	}
	if config.WebListen == "" {
		config.WebListen = "127.0.0.1:18890"
	}
	if err = levelup.ValidateConfig(config, true); err != nil {
		log.Fatal("config.json无效：", err)
	}
	if *check {
		fmt.Fprintln(os.Stderr, "laoluo-levelup配置检查通过")
		return
	}
	dataDir := config.DataDir
	if managed.DataDirectory != "" {
		dataDir = managed.DataDirectory
	}
	if dataDir == "" {
		dataDir = filepath.Join(filepath.Dir(*configPath), "data")
	}
	if !filepath.IsAbs(dataDir) {
		dataDir = filepath.Join(filepath.Dir(*configPath), dataDir)
	}
	if err = os.MkdirAll(dataDir, 0700); err != nil {
		log.Fatal("插件自己的data目录不可写。")
	}
	lock, err := os.OpenFile(filepath.Join(dataDir, "instance.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		log.Fatal("无法打开插件实例锁。")
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		log.Fatal("同一个data目录已有插件运行，请勿重复启动。")
	}
	store, err := levelup.OpenStore(dataDir)
	if err != nil {
		log.Fatal("插件状态无法解密或写入，已停止。请恢复原data备份，不要删除状态后继续分配设备。")
	}
	ipc := levelup.NewIPC(os.Stdout)
	ipc.InstanceID = managed.InstanceID
	engine := levelup.NewEngine(ipc, store, config)
	admin, err := levelup.NewAdminWeb(engine, *configPath, dataDir)
	if err != nil {
		log.Fatal("网页访问配置无法读取或写入，已停止。")
	}
	web, listener, err := levelup.StartAdminServer(admin, config.WebListen)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = web.Shutdown(ctx)
	}()
	log.Printf("网页配置入口：http://%s/ （外部访问请配置反向代理或端口转发）", listener.Addr().String())
	if admin.InitialCode != "" {
		log.Printf("首次网页初始化码（仅用一次，不是QQ密码）：%s", admin.InitialCode)
		log.Printf("也保存在插件自己的 %s", filepath.Join(dataDir, "web-first-access.txt"))
	}
	if err = ipc.Hello(); err != nil {
		return
	}
	readDone := make(chan error, 1)
	go func() { readDone <- ipc.Read(os.Stdin) }()
	select {
	case <-ipc.Ready:
	case <-time.After(20 * time.Second):
		log.Print("未收到框架ready，已停止。")
		return
	case <-readDone:
		return
	}
	result, err := levelup.Call(ipc, "get_plugin_context", levelup.Object{})
	if err != nil {
		log.Print("无法读取公开插件上下文，已停止。")
		return
	}
	ctx := levelup.Obj(result)
	if err = levelup.ValidatePluginContext(ctx); err != nil {
		log.Print(err)
		return
	}
	admin.SetReady(true, "框架已连接")
	if config.BotQQ == 0 {
		log.Print("请在网页中选择机器人并保存；导入后即可配置，无需重新打包。")
	} else if err = engine.VerifyBot(); err != nil {
		log.Print("机器人暂未就绪，请在网页核对配置或先在后台登录机器人；网页仍可访问。")
	}
	for i := 0; i < 8; i++ {
		go func() {
			for event := range ipc.Events {
				engine.Handle(event)
			}
		}()
	}
	log.Print("laoluo-levelup已就绪，仅处理配置的Pad机器人好友私聊；不记录消息正文、密码或验证码。")
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			engine.Sweep()
		case <-readDone:
			return
		}
	}
}
