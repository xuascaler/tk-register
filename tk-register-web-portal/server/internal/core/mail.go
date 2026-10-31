package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

type Mailer struct{ Store *Store }

func (m *Mailer) SendCode(ctx context.Context, email, code, purpose string, ttlSeconds int) error {
	values, err := m.Store.Settings(ctx)
	if err != nil {
		return err
	}
	return sendCode(ctx, values, email, code, purpose, ttlSeconds, nil)
}

func validSenderEmail(email string) bool {
	if len(email) > 254 || strings.ContainsAny(email, "\r\n\x00 \t") {
		return false
	}
	address, err := mail.ParseAddress(email)
	return err == nil && address.Address == email
}

func sendCode(ctx context.Context, values Values, email, code, purpose string, ttlSeconds int, testTLS *tls.Config) error {
	if !ValidEmail(email) || !validSenderEmail(values.String("smtp_from_email")) {
		return errors.New("invalid email address")
	}
	if values.String("smtp_host") == "" {
		return errors.New("SMTP is not configured")
	}
	if values.String("smtp_tls_mode") != "tls" && values.String("smtp_tls_mode") != "starttls" {
		return errors.New("SMTP must use TLS or STARTTLS")
	}
	if len(code) < 1 || len(code) > 32 || strings.IndexFunc(code, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') }) >= 0 {
		return errors.New("invalid verification code")
	}
	if ttlSeconds < 1 {
		return errors.New("invalid verification lifetime")
	}
	subject, action := "", ""
	switch purpose {
	case "register", "registration":
		subject = "Verify your email for TK Register"
		action = "complete your registration"
	case "login":
		subject = "Your TK Register sign-in code"
		action = "sign in to your account"
	case "admin_login", "admin":
		subject = "Confirm your TK Register administrator sign-in"
		action = "complete your administrator sign-in"
	default:
		return errors.New("invalid verification purpose")
	}
	message, err := codeMessage(values.String("smtp_from_name"), values.String("smtp_from_email"), email, code, subject, action, ttlSeconds)
	if err != nil {
		return err
	}
	timeout := time.Duration(values.Int("smtp_timeout_seconds")) * time.Second
	if timeout < time.Second || timeout > time.Minute {
		return errors.New("invalid SMTP timeout")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	host := values.String("smtp_host")
	address := net.JoinHostPort(host, values.String("smtp_port"))
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err = conn.SetDeadline(deadline); err != nil {
		return err
	}
	rawConn := conn
	stop := context.AfterFunc(ctx, func() { rawConn.Close() })
	defer stop()
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if testTLS != nil {
		tlsConfig = testTLS.Clone()
		tlsConfig.ServerName = host
		tlsConfig.MinVersion = tls.VersionTLS12
	}
	if values.String("smtp_tls_mode") == "tls" {
		secure := tls.Client(conn, tlsConfig)
		if err = secure.HandshakeContext(ctx); err != nil {
			return err
		}
		conn = secure
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer client.Close()
	if values.String("smtp_tls_mode") == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("SMTP server does not support required STARTTLS")
		}
		if err = client.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	username, password := values.String("smtp_username"), values.String("smtp_password")
	if username != "" {
		if password == "" {
			return errors.New("SMTP password is missing")
		}
		var auth smtp.Auth = smtp.PlainAuth("", username, password, host)
		if values.Bool("smtp_force_auth_login") {
			auth = loginAuth{username: username, password: password, host: host}
		}
		if err = client.Auth(auth); err != nil {
			return err
		}
	}
	if err = client.Mail(values.String("smtp_from_email")); err != nil {
		return err
	}
	if err = client.Rcpt(email); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err = writer.Write(message); err != nil {
		writer.Close()
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	// The server has accepted DATA. A failed QUIT must not make a delivered code
	// appear unsent or clear the cooldown, which could trigger duplicate emails.
	_ = client.Quit()
	return nil
}

type loginAuth struct{ username, password, host string }

func (a loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS || server.Name != a.host {
		return "", nil, errors.New("AUTH LOGIN requires a verified TLS connection")
	}
	return "LOGIN", nil, nil
}
func (a loginAuth) Next(challenge []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(challenge))) {
	case "username:", "username", "user name:":
		return []byte(a.username), nil
	case "password:", "password":
		return []byte(a.password), nil
	default:
		return nil, errors.New("unexpected AUTH LOGIN challenge")
	}
}

func codeMessage(name, from, to, code, subject, action string, ttlSeconds int) ([]byte, error) {
	if !validSenderEmail(from) || !ValidEmail(to) {
		return nil, errors.New("invalid email address")
	}
	if strings.ContainsAny(name, "\r\n\x00") {
		return nil, errors.New("invalid sender name")
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	lifetime := strconv.Itoa(ttlSeconds) + " seconds"
	if ttlSeconds%60 == 0 {
		lifetime = strconv.Itoa(ttlSeconds/60) + " minutes"
	}
	if ttlSeconds == 60 {
		lifetime = "1 minute"
	}
	plain := fmt.Sprintf("TK Register\n\nUse this code to %s:\n\n%s\n\nThis code expires in %s and can be used once.\nIf you did not request this email, you can ignore it.\n\nGoAPI\n", action, code, lifetime)
	body := fmt.Sprintf(`<!doctype html><html lang="en"><body style="margin:0;background:#f6f8fb;font-family:Arial,Helvetica,sans-serif;color:#23364d"><table role="presentation" width="100%%" cellspacing="0" cellpadding="0"><tr><td align="center" style="padding:40px 16px"><table role="presentation" width="520" cellspacing="0" cellpadding="0" style="max-width:520px;width:100%%;background:#ffffff;border:1px solid #dce5ef;border-radius:12px"><tr><td style="padding:36px"><div style="font-size:28px;font-weight:700;color:#163e6b">goapi</div><div style="font-size:12px;letter-spacing:1px;color:#7a8a9e;margin-top:6px">TK REGISTER</div><h1 style="font-size:24px;line-height:1.3;margin:32px 0 16px">Your verification code</h1><p style="font-size:15px;line-height:1.6;color:#64748b">Use this code to %s.</p><div style="padding:24px 16px;background:#edf3fa;border:1px solid #dce5ef;border-radius:8px;text-align:center;font-size:32px;letter-spacing:7px;font-weight:700;color:#163e6b">%s</div><p style="font-size:14px;line-height:1.6;color:#64748b;margin-top:24px">This code expires in %s and can be used once.</p><p style="font-size:13px;line-height:1.6;color:#7a8a9e;margin-bottom:0">If you did not request this email, you can ignore it.</p></td></tr></table></td></tr></table></body></html>`, html.EscapeString(action), html.EscapeString(code), html.EscapeString(lifetime))
	var parts bytes.Buffer
	multi := multipart.NewWriter(&parts)
	for _, part := range []struct{ kind, content string }{{"text/plain", plain}, {"text/html", body}} {
		header := textproto.MIMEHeader{}
		header.Set("Content-Type", part.kind+"; charset=UTF-8")
		header.Set("Content-Transfer-Encoding", "quoted-printable")
		writer, err := multi.CreatePart(header)
		if err != nil {
			return nil, err
		}
		qp := quotedprintable.NewWriter(writer)
		if _, err = qp.Write([]byte(part.content)); err != nil {
			return nil, err
		}
		if err = qp.Close(); err != nil {
			return nil, err
		}
	}
	if err := multi.Close(); err != nil {
		return nil, err
	}
	var message bytes.Buffer
	fmt.Fprintf(&message, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s@%s>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=%q\r\n\r\n", (&mail.Address{Name: name, Address: from}).String(), (&mail.Address{Address: to}).String(), subject, time.Now().UTC().Format(time.RFC1123Z), hex.EncodeToString(id), strings.SplitN(from, "@", 2)[1], multi.Boundary())
	message.Write(parts.Bytes())
	return message.Bytes(), nil
}
