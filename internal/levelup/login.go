package levelup

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
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
		return e.reply(qq, "pad_online")
	}
	if b != nil && b.Status == 2 {
		return e.reply(qq, "pad_pending")
	}
	if f := e.flow(qq); f != nil {
		return e.reply(qq, "session_busy")
	}
	pid, err := protocol(d, "pad", e.Config.PadProtocolID)
	if err != nil {
		return err
	}
	e.setFlow(qq, e.newSession("pad", "password", pid))
	if err = e.reply(qq, "pad_start"); err != nil {
		return err
	}
	return e.reply(qq, "pad_password")
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
		return e.reply(qq, "login_state_changed")
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
			_ = e.reply(qq, "pad_settings_failed")
			return err
		}
	}
	e.setFlow(qq, nil)
	name := "Pad"
	if f.Kind == "linux" {
		name = "Linux电脑"
	}
	return e.reply(qq, "login_success", "账号类型", name)
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
		return e.reply(qq, "slider_required", "后台地址", e.backendHint())
	case "identity":
		f.Stage = "identity_backend"
		return e.reply(qq, "identity_required", "后台地址", e.backendHint())
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
			return e.reply(qq, "security_backend", "后台地址", e.backendHint())
		}
		return e.reply(qq, "security_choices", "可用验证指令", strings.Join(choices, " | "))
	case "qr":
		if f.QRURL != "" {
			return e.reply(qq, "qr_pending")
		}
		f.Stage = "linux_qr"
		u := Str(r["url"])
		if u == "" {
			u = Str(r["qr_url"])
		}
		return e.sendQR(qq, u, e.replyText("qr_scan"))
	case "failed":
		f.Stage = "backend"
		return e.reply(qq, "login_failed")
	default:
		// Old numeric codes are observations, never proof of login.
		if Int(r["code"]) == 140022008 || Str(r["slider_url"]) != "" {
			f.Stage = "backend"
			return e.reply(qq, "slider_backend", "后台地址", e.backendHint())
		}
		f.Stage = "backend"
		return e.reply(qq, "login_unconfirmed", "后台地址", e.backendHint())
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
		status := []string{e.replyText("continue_no_active")}
		if pad != nil && pad.Status == 1 {
			if err = e.initialize(qq, pad.PID); err != nil {
				return err
			}
			status = append(status, e.replyText("continue_pad_online"))
		}
		if linux != nil && linux.Status == 1 {
			status = append(status, e.replyText("continue_linux_online"))
		}
		return e.reply(qq, "continue_no_session", "账号状态", strings.Join(status, "\n"))
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
			return e.reply(qq, "qr_expired")
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
			return e.reply(qq, "qr_pending")
		}
		return e.acceptLogin(qq, r)
	}
	return e.reply(qq, "continue_pending", "后台地址", e.backendHint())
}
func (e *Engine) cancel(qq int64) error {
	f := e.flow(qq)
	if f == nil {
		return e.reply(qq, "cancel_empty")
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
			return e.reply(qq, "cancel_online")
		}
		if b != nil && b.PID == f.PID && b.Status == 2 {
			if _, err = Call(e.API, "offline_account", selector(qq, f.PID)); err != nil {
				return err
			}
		}
	}
	e.setFlow(qq, nil)
	return e.reply(qq, "cancel_done")
}
func (e *Engine) startLinux(qq int64) error {
	if e.flow(qq) != nil {
		return e.reply(qq, "linux_session_busy")
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
		return e.reply(qq, "linux_online")
	}
	if old != nil && old.Status == 2 {
		return e.reply(qq, "linux_pending")
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
			needA1, hasNeedA1 := s["need_a1"].(bool)
			if scanErr == nil && accepted && hasNeedA1 {
				p["need_a1"] = needA1
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
	return e.sendQR(qq, qrURL, e.replyText("linux_qr_fallback"))
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
		return e.reply(qq, "sms_unavailable")
	}
	m := Obj(f.Methods["methods"])
	allowed := false
	for _, v := range Arr(m["verify_list"]) {
		if Int(v) == kind {
			allowed = true
		}
	}
	if !allowed {
		return e.reply(qq, "sms_method_unsupported")
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
		return e.reply(qq, "sms_unconfirmed")
	}
	f.Sign = Str(r["sign"])
	if f.Sign == "" {
		return errors.New("SMS_SIGN_MISSING")
	}
	f.VerifyType = kind
	if kind == 4 {
		f.Stage = "sms_code"
		return e.reply(qq, "sms_code_request")
	}
	f.Stage = "sms_sent"
	return e.smsInstructions(qq, r)
}
func (e *Engine) smsInstructions(qq int64, r Object) error {
	text, to := Str(r["sms"]), Str(r["send_to"])
	if text == "" || to == "" {
		return e.reply(qq, "sms_backend", "后台地址", e.backendHint())
	}
	return e.reply(qq, "sms_instructions", "短信接收号码", to, "短信内容", text)
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
		return e.reply(qq, "sms_failed")
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
		return e.reply(qq, "sms_wrong_stage")
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
		return e.reply(qq, "security_qr_unsupported")
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
	return e.sendQR(qq, Str(r["qr_url"]), e.replyText("security_qr_scan"))
}
