package levelup

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

func (e *Engine) newSession(kind, stage string, pid int64) *Session {
	return &Session{Kind: kind, Stage: stage, PID: pid, Expires: time.Now().Add(time.Duration(e.Config.SessionMinutes) * time.Minute)}
}
func (e *Engine) startPad(qq int64) error {
	d, err := e.dir()
	if err != nil {
		return err
	}
	b, err := findConfiguredBot(d, qq, "pad", e.Config.PadProtocolID)
	if err != nil {
		return err
	}
	if b != nil && b.Status == 1 {
		if err = e.initialize(qq, b.PID); err != nil {
			return err
		}
		return e.send(qq, "Pad账号已经在线。可发送“立即补挂”或“项目开关”。")
	}
	if b != nil && b.Status == 2 {
		return e.send(qq, "账号正在验证，请到Mengka后台完成当前验证，再发“继续验证”。")
	}
	if f := e.flow(qq); f != nil {
		return e.send(qq, "已有操作尚未完成，请按上条提示继续，或发送“取消操作”。")
	}
	pid, err := protocol(d, "pad", e.Config.PadProtocolID)
	if err != nil {
		return err
	}
	e.setFlow(qq, e.newSession("pad", "password", pid))
	if err = e.send(qq, "开始登录流程"); err != nil {
		return err
	}
	return e.send(qq, "请发送您的QQ密码给我。下一条非菜单指令将原样作为密码；只用于登录您当前私聊的QQ。也可发送“取消操作”。")
}
func (e *Engine) password(qq int64, password string) error {
	f := e.flow(qq)
	if f == nil || f.Stage != "password" {
		return errors.New("FLOW_MISMATCH")
	}
	d, err := e.dir()
	if err != nil {
		return err
	}
	existing, err := findConfiguredBot(d, qq, "pad", e.Config.PadProtocolID)
	if err != nil {
		return err
	}
	if existing != nil && existing.Status != 0 {
		f.Stage = "backend"
		return e.send(qq, "账号状态已变化，请到后台核对，再发送“继续验证”。")
	}
	// Consume the password step BEFORE any uncertain mutation. Do not replay a password after timeout.
	f.Stage = "backend"
	b, err := e.ensureAccount(qq, "pad", password)
	if err != nil {
		return err
	}
	f.PID = b.PID
	if existing != nil {
		p := selector(qq, b.PID)
		p["new_protocol_id"] = b.PID
		p["password"] = password
		p["device_profile_id"] = b.Device
		p["nt"] = 1
		if _, err = Call(e.API, "update_account", p); err != nil {
			return err
		}
	}
	v, err := Call(e.API, "login_account", selector(qq, b.PID))
	if err != nil {
		return err
	}
	return e.acceptLogin(qq, Obj(v))
}
func (e *Engine) backendHint() string {
	u, err := url.Parse(e.Config.AdminURL)
	if err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil {
		return "\n后台地址：" + u.String()
	}
	return ""
}
func (e *Engine) online(qq int64, f *Session) (bool, error) {
	d, err := e.dir()
	if err != nil {
		return false, err
	}
	b, err := findConfiguredBot(d, qq, f.Kind, &f.PID)
	if err != nil {
		return false, err
	}
	return b != nil && b.PID == f.PID && b.Status == 1 && b.NT == 1, nil
}
func (e *Engine) finish(qq int64, f *Session) error {
	if f.Kind == "pad" {
		if err := e.initialize(qq, f.PID); err != nil {
			e.setFlow(qq, nil)
			_ = e.send(qq, "Pad已确认在线，但任务设置未完成；请稍后发送“项目开关”重试。")
			return err
		}
	}
	e.setFlow(qq, nil)
	name := "Pad"
	if f.Kind == "linux" {
		name = "Linux电脑"
	}
	return e.send(qq, name+"已确认登录成功。")
}
func (e *Engine) acceptLogin(qq int64, r Object) error {
	f := e.flow(qq)
	if f == nil {
		return errors.New("FLOW_MISSING")
	}
	online, err := e.online(qq, f)
	if err != nil {
		return err
	}
	if online {
		return e.finish(qq, f)
	}
	next := Str(r["next"])
	switch next {
	case "slider":
		f.Stage = "backend"
		return e.send(qq, "本次登录需要滑块验证。请到Mengka后台完成该QQ的当前验证，然后回这里发送“继续验证”。"+e.backendHint())
	case "identity":
		f.Stage = "identity_backend"
		return e.send(qq, "本次登录需要身份验证。请先到Mengka后台完成身份验证页面；可在后台完成整个流程后发送“继续验证”。若后台已完成身份滑块并要求核验手机，可发送“身份手机号”，随后用“区号 手机号”继续。"+e.backendHint())
	case "security":
		methods := Obj(r["security_verify"])
		if methods == nil {
			v, err := Call(e.API, "get_security_verify_methods", selector(qq, f.PID))
			if err != nil {
				return err
			}
			methods = Obj(v)
		}
		f.Methods = methods
		f.Stage = "security"
		list := Arr(Obj(methods["methods"])["verify_list"])
		choices := []string{}
		for _, v := range list {
			switch Int(v) {
			case 3:
				choices = append(choices, "发送短信")
			case 4:
				choices = append(choices, "接收短信")
			case 10:
				choices = append(choices, "安全扫码")
			}
		}
		if len(choices) == 0 {
			f.Stage = "backend"
			return e.send(qq, "请到Mengka后台完成当前安全验证，再发送“继续验证”。"+e.backendHint())
		}
		return e.send(qq, "请选择安全验证方式，发送以下任一指令：\n"+strings.Join(choices, " | "))
	case "qr":
		if f.QRURL != "" {
			return e.send(qq, "二维码尚未确认，请用手机QQ确认后再发“继续验证”。")
		}
		f.Stage = "linux_qr"
		u := Str(r["url"])
		if u == "" {
			u = Str(r["qr_url"])
		}
		return e.sendQR(qq, u, "请用手机QQ扫描二维码并确认，再发送“继续验证”。")
	case "failed":
		f.Stage = "backend"
		return e.send(qq, "本次登录未成功。请核对QQ密码或查看框架账号日志；先发“取消操作”，再重新开始。")
	default:
		// Old numeric codes are observations, never proof of login.
		if Int(r["code"]) == 140022008 || Str(r["slider_url"]) != "" {
			f.Stage = "backend"
			return e.send(qq, "请到Mengka后台完成滑块，再发送“继续验证”。"+e.backendHint())
		}
		f.Stage = "backend"
		return e.send(qq, "登录结果尚未确认，请到后台查看当前状态，再发送“继续验证”。"+e.backendHint())
	}
}
func (e *Engine) continueLogin(qq int64) error {
	f := e.flow(qq)
	if f == nil {
		d, err := e.dir()
		if err != nil {
			return err
		}
		pad, err := findConfiguredBot(d, qq, "pad", e.Config.PadProtocolID)
		if err != nil {
			return err
		}
		linux, err := findConfiguredBot(d, qq, "linux", e.Config.LinuxProtocolID)
		if err != nil {
			return err
		}
		out := "当前没有插件验证会话。"
		if pad != nil && pad.Status == 1 {
			if err = e.initialize(qq, pad.PID); err != nil {
				return err
			}
			out += "\nPad已在线。"
		}
		if linux != nil && linux.Status == 1 {
			out += "\nLinux电脑已在线。"
		}
		return e.send(qq, out+"\n可发送“等级菜单”重新选择。")
	}
	online, err := e.online(qq, f)
	if err != nil {
		return err
	}
	if online {
		return e.finish(qq, f)
	}
	if f.Stage == "linux_qr" {
		v, err := Call(e.API, "query_login_qr", selector(qq, f.PID))
		if err != nil {
			return err
		}
		r := Obj(v)
		switch Str(r["state"]) {
		case "code_expired", "canceled", "invalid":
			f.Stage = "backend"
			return e.send(qq, "二维码已失效，请发送“取消操作”后重新发“电脑登录”。")
		}
		return e.acceptLogin(qq, r)
	}
	if f.Stage == "security_qr" {
		p := selector(qq, f.PID)
		p["guarantee_token"] = f.QRToken
		v, err := Call(e.API, "query_security_verify_qr", p)
		if err != nil {
			return err
		}
		r := Obj(v)
		if Str(r["status"]) == "waiting" || Str(r["status"]) == "scanned" {
			return e.send(qq, "二维码尚未确认，请用手机QQ确认后再发“继续验证”。")
		}
		return e.acceptLogin(qq, r)
	}
	return e.send(qq, "账号尚未确认在线，请完成当前短信、二维码或后台验证后再发“继续验证”。"+e.backendHint())
}
func (e *Engine) cancel(qq int64) error {
	f := e.flow(qq)
	if f == nil {
		return e.send(qq, "当前没有待取消的插件操作。")
	}
	if f.Stage != "password" {
		d, err := e.dir()
		if err != nil {
			return err
		}
		b, err := findConfiguredBot(d, qq, f.Kind, &f.PID)
		if err != nil {
			return err
		}
		if b != nil && b.PID == f.PID && b.Status == 1 {
			e.setFlow(qq, nil)
			return e.send(qq, "账号已经在线，已结束对话操作，保留在线状态。")
		}
		if b != nil && b.PID == f.PID && b.Status == 2 {
			if _, err = Call(e.API, "offline_account", selector(qq, f.PID)); err != nil {
				return err
			}
		}
	}
	e.setFlow(qq, nil)
	return e.send(qq, "已取消当前对话操作。")
}
func (e *Engine) startLinux(qq int64) error {
	if e.flow(qq) != nil {
		return e.send(qq, "已有操作尚未完成，请先完成或发送“取消操作”。")
	}
	d, err := e.dir()
	if err != nil {
		return err
	}
	old, err := findConfiguredBot(d, qq, "linux", e.Config.LinuxProtocolID)
	if err != nil {
		return err
	}
	if old != nil && old.Status == 1 {
		return e.send(qq, "Linux电脑已经在线。")
	}
	if old != nil && old.Status == 2 {
		return e.send(qq, "电脑账号正在验证，请先到后台完成或取消当前验证。")
	}
	b, err := e.ensureAccount(qq, "linux", "")
	if err != nil {
		return err
	}
	// Enforce passwordless QR login even for a pre-existing manually created Linux instance.
	if old != nil {
		p := selector(qq, b.PID)
		p["new_protocol_id"] = b.PID
		p["password"] = ""
		p["device_profile_id"] = b.Device
		p["nt"] = 1
		if _, err = Call(e.API, "update_account", p); err != nil {
			return err
		}
	}
	f := e.newSession("linux", "backend", b.PID)
	e.setFlow(qq, f)
	v, err := Call(e.API, "login_account", selector(qq, b.PID))
	if err != nil {
		return err
	}
	r := Obj(v)
	qrURL := Str(r["url"])
	if qrURL == "" {
		qrURL = Str(r["qr_url"])
	}
	if Str(r["next"]) != "qr" || qrURL == "" {
		return e.acceptLogin(qq, r)
	}
	f.Stage = "linux_qr"
	if e.Config.QuickLogin {
		d, readErr := e.dir()
		pad, padErr := findConfiguredBot(d, qq, "pad", e.Config.PadProtocolID)
		u, parseErr := url.Parse(qrURL)
		if readErr == nil && padErr == nil && pad != nil && pad.Status == 1 && parseErr == nil && u.Query().Get("k") != "" {
			p := selector(qq, pad.PID)
			p["k"] = u.Query().Get("k")
			scan, scanErr := Call(e.API, "scan_qr", p)
			s := Obj(scan)
			accepted := Int(s["code"]) == 0 || Int(s["code"]) == 1 && Int(s["status"]) == 1
			if scanErr == nil && accepted {
				p["skip_phone_confirm"] = true
				auth, authErr := Call(e.API, "auth_qr", p)
				a := Obj(auth)
				if authErr == nil && Int(a["code"]) == 0 && Bool(a["success"]) {
					poll, pollErr := Call(e.API, "query_login_qr", selector(qq, f.PID))
					if pollErr == nil {
						online, err := e.online(qq, f)
						if err == nil && online {
							return e.finish(qq, f)
						}
						_ = poll
					}
				}
			}
		}
	}
	f.QRURL = qrURL
	return e.sendQR(qq, qrURL, "免扫未确认成功，请用手机QQ扫描当前二维码并确认，再发送“继续验证”。")
}
func (e *Engine) sendQR(qq int64, raw, prompt string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || len(raw) > 2048 {
		return errors.New("INVALID_QR_URL")
	}
	png, err := qrcode.Encode(raw, qrcode.Medium, 320)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(png)
	v, err := Call(e.API, "file_transfer", Object{"operation": "start_upload", "filename": "laoluo-login.png", "size": len(png), "sha256": hex.EncodeToString(hash[:])})
	if err != nil {
		return err
	}
	token := Str(Obj(v)["token"])
	if token == "" {
		return errors.New("TRANSFER_SCHEMA")
	}
	defer func() { _, _ = Call(e.API, "file_transfer", Object{"operation": "cleanup", "token": token}) }()
	if _, err = Call(e.API, "file_transfer", Object{"operation": "write", "token": token, "offset": 0, "data": base64.StdEncoding.EncodeToString(png)}); err != nil {
		return err
	}
	v, err = Call(e.API, "file_transfer", Object{"operation": "finish", "token": token})
	if err != nil {
		return err
	}
	file := Str(Obj(v)["file"])
	if !strings.HasPrefix(file, "transfer://") {
		return errors.New("TRANSFER_SCHEMA")
	}
	bot, err := e.bot()
	if err != nil {
		return err
	}
	p := selector(bot.QQ, bot.PID)
	p["user_id"] = qq
	p["file_path"] = file
	segment, err := Call(e.API, "upload_friend_image", p)
	if err != nil {
		return err
	}
	if Str(Obj(segment)["type"]) != "image" || Str(Obj(Obj(segment)["data"])["file_id"]) == "" {
		return errors.New("MEDIA_SCHEMA")
	}
	if err = e.send(qq, prompt); err != nil {
		return err
	}
	p = selector(bot.QQ, bot.PID)
	p["user_id"] = qq
	p["message"] = []any{segment}
	v, err = Call(e.API, "send_friend_msg", p)
	if err != nil {
		return err
	}
	if !Bool(Obj(v)["success"]) {
		return errors.New("SEND_NOT_CONFIRMED")
	}
	return nil
}
func (e *Engine) startSMS(qq, kind int64) error {
	f := e.flow(qq)
	if f == nil || f.Stage != "security" {
		return e.send(qq, "当前没有可选择的短信验证步骤，请按当前验证提示操作。")
	}
	m := Obj(f.Methods["methods"])
	allowed := false
	for _, v := range Arr(m["verify_list"]) {
		if Int(v) == kind {
			allowed = true
		}
	}
	if !allowed {
		return e.send(qq, "当前会话不支持这项短信验证方式。")
	}
	sign := Str(m["sign"])
	if sign == "" {
		return errors.New("SMS_SIGN_MISSING")
	}
	p := selector(qq, f.PID)
	p["verify_type"] = kind
	p["sign"] = sign
	v, err := Call(e.API, "get_sms", p)
	if err != nil {
		return err
	}
	r := Obj(v)
	if Obj(r["result"])["state"] != nil {
		return e.send(qq, "框架未确认短信步骤可继续，请在后台核对当前验证状态。")
	}
	f.Sign = Str(r["sign"])
	if f.Sign == "" {
		return errors.New("SMS_SIGN_MISSING")
	}
	f.VerifyType = kind
	if kind == 4 {
		f.Stage = "sms_code"
		return e.send(qq, "请发送您收到的短信验证码。")
	}
	f.Stage = "sms_sent"
	return e.smsInstructions(qq, r)
}
func (e *Engine) smsInstructions(qq int64, r Object) error {
	text, to := Str(r["sms"]), Str(r["send_to"])
	if text == "" || to == "" {
		return e.send(qq, "请在Mengka后台查看需要发送的短信内容及接收号码，发送后回这里发“已发送”。"+e.backendHint())
	}
	return e.send(qq, fmt.Sprintf("请使用绑定的密保手机，将以下内容发送到 %s：\n%s\n发送后请回复：已发送", to, text))
}
func (e *Engine) checkSMS(qq int64, code string) error {
	f := e.flow(qq)
	if f == nil || f.Stage != "sms_code" && f.Stage != "sms_sent" {
		return errors.New("FLOW_MISMATCH")
	}
	p := selector(qq, f.PID)
	p["verify_type"] = f.VerifyType
	p["sign"] = f.Sign
	if f.VerifyType == 4 {
		p["code"] = code
	}
	v, err := Call(e.API, "check_sms", p)
	if err != nil {
		return err
	}
	r := Obj(v)
	if Obj(r["result"])["state"] != nil {
		return e.send(qq, "短信验证尚未通过，请核对后重试，或到后台完成。")
	}
	return e.acceptLogin(qq, r)
}
func (e *Engine) checkSent(qq int64) error {
	f := e.flow(qq)
	if f == nil {
		return errors.New("FLOW_MISSING")
	}
	if f.Stage == "sms_sent" {
		return e.checkSMS(qq, "")
	}
	if f.Stage != "identity_sent" {
		return e.send(qq, "当前步骤不是发送短信确认，请按上条提示操作。")
	}
	p := selector(qq, f.PID)
	p["mobile"] = f.Mobile
	p["area_code"] = f.Area
	v, err := Call(e.API, "confirm_identity_sms", p)
	if err != nil {
		return err
	}
	return e.acceptLogin(qq, Obj(v))
}
func (e *Engine) securityQR(qq int64) error {
	f := e.flow(qq)
	if f == nil || f.Stage != "security" {
		return errors.New("FLOW_MISMATCH")
	}
	allowed := false
	for _, v := range Arr(Obj(f.Methods["methods"])["verify_list"]) {
		if Int(v) == 10 {
			allowed = true
		}
	}
	if !allowed {
		return e.send(qq, "当前会话不支持安全扫码。")
	}
	v, err := Call(e.API, "get_security_verify_qr", selector(qq, f.PID))
	if err != nil {
		return err
	}
	r := Obj(v)
	f.QRToken = Str(r["guarantee_token"])
	if f.QRToken == "" {
		return errors.New("QR_TOKEN_MISSING")
	}
	f.Stage = "security_qr"
	return e.sendQR(qq, Str(r["qr_url"]), "请用手机QQ扫描安全验证二维码并确认，再发送“继续验证”。")
}
