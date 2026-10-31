package core

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

func TestSMTPWithVerifiedTLSAndBothAuthMethods(t *testing.T) {
	for _, mode := range []string{"starttls", "tls"} {
		t.Run(mode, func(t *testing.T) {
			certificateServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			serverTLS := certificateServer.TLS.Clone()
			roots := x509.NewCertPool()
			roots.AddCert(certificateServer.Certificate())
			certificateServer.Close()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			received := make(chan string, 1)
			finished := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					finished <- err
					return
				}
				defer conn.Close()
				if mode == "tls" {
					conn = tls.Server(conn, serverTLS)
				}
				reader := bufio.NewReader(conn)
				fmt.Fprint(conn, "220 localhost ESMTP\r\n")
				loginStage := 0
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						finished <- err
						return
					}
					line = strings.TrimSpace(line)
					if loginStage > 0 {
						decoded, err := base64.StdEncoding.DecodeString(line)
						if err != nil {
							finished <- err
							return
						}
						if loginStage == 1 {
							if string(decoded) != "smtp-user" {
								finished <- fmt.Errorf("wrong LOGIN username")
								return
							}
							loginStage = 2
							fmt.Fprint(conn, "334 UGFzc3dvcmQ6\r\n")
						} else {
							if string(decoded) != "smtp-secret" {
								finished <- fmt.Errorf("wrong LOGIN password")
								return
							}
							loginStage = 0
							fmt.Fprint(conn, "235 authenticated\r\n")
						}
						continue
					}
					switch {
					case strings.HasPrefix(line, "EHLO "):
						fmt.Fprint(conn, "250-localhost\r\n250-STARTTLS\r\n250 AUTH PLAIN LOGIN\r\n")
					case line == "STARTTLS":
						fmt.Fprint(conn, "220 ready\r\n")
						conn = tls.Server(conn, serverTLS)
						reader = bufio.NewReader(conn)
					case strings.HasPrefix(line, "AUTH PLAIN "):
						decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "AUTH PLAIN "))
						if err != nil || string(decoded) != "\x00smtp-user\x00smtp-secret" {
							finished <- fmt.Errorf("wrong PLAIN credentials")
							return
						}
						fmt.Fprint(conn, "235 authenticated\r\n")
					case line == "AUTH LOGIN":
						loginStage = 1
						fmt.Fprint(conn, "334 VXNlcm5hbWU6\r\n")
					case strings.HasPrefix(line, "MAIL FROM:") || strings.HasPrefix(line, "RCPT TO:"):
						fmt.Fprint(conn, "250 accepted\r\n")
					case line == "DATA":
						fmt.Fprint(conn, "354 send data\r\n")
						body, err := textproto.NewReader(reader).ReadDotBytes()
						if err != nil {
							finished <- err
							return
						}
						received <- string(body)
						fmt.Fprint(conn, "250 queued\r\n")
					case line == "QUIT":
						fmt.Fprint(conn, "221 goodbye\r\n")
						finished <- nil
						return
					default:
						finished <- fmt.Errorf("unexpected SMTP command")
						return
					}
				}
			}()
			host, port, _ := net.SplitHostPort(listener.Addr().String())
			values := Values{"smtp_host": host, "smtp_port": port, "smtp_tls_mode": mode, "smtp_username": "smtp-user", "smtp_password": "smtp-secret", "smtp_from_name": "GoAPI", "smtp_from_email": "noreply@example.com", "smtp_timeout_seconds": "3", "smtp_force_auth_login": "false"}
			if mode == "tls" {
				values["smtp_force_auth_login"] = "true"
			}
			if err = sendCode(context.Background(), values, "member@example.com", "A1b2C3", "registration", 600, &tls.Config{RootCAs: roots}); err != nil {
				t.Fatal(err)
			}
			if err = <-finished; err != nil {
				t.Fatal(err)
			}
			message, err := mail.ReadMessage(strings.NewReader(<-received))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(message.Header.Get("From"), "GoAPI") {
				t.Fatal("missing GoAPI sender")
			}
			_, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
			if err != nil {
				t.Fatal(err)
			}
			parts := multipart.NewReader(message.Body, params["boundary"])
			for i := 0; i < 2; i++ {
				part, err := parts.NextPart()
				if err != nil {
					t.Fatal(err)
				}
				var reader io.Reader = part
				if part.Header.Get("Content-Transfer-Encoding") == "quoted-printable" {
					reader = quotedprintable.NewReader(part)
				}
				body, err := io.ReadAll(reader)
				if err != nil {
					t.Fatal(err)
				}
				for _, needle := range []string{"A1b2C3", "10 minutes", "registration"} {
					if !strings.Contains(string(body), needle) {
						t.Fatalf("missing %q in MIME part", needle)
					}
				}
			}
		})
	}
}

func TestSMTPRejectsUntrustedCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	host, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	values := Values{"smtp_host": host, "smtp_port": port, "smtp_tls_mode": "tls", "smtp_from_name": "GoAPI", "smtp_from_email": "noreply@example.com", "smtp_timeout_seconds": "3"}
	if err := sendCode(context.Background(), values, "member@example.com", "Ab1234", "login", 600, nil); err == nil {
		t.Fatal("untrusted TLS certificate was accepted")
	}
}

func TestMailRejectsHeaderInjectionAndPlaintext(t *testing.T) {
	if _, err := codeMessage("GoAPI\r\nBcc: victim@example.com", "sender@example.com", "member@example.com", "Ab1234", "subject", "sign in", 600); err == nil {
		t.Fatal("sender header injection accepted")
	}
	values := Values{"smtp_host": "127.0.0.1", "smtp_port": "25", "smtp_tls_mode": "none", "smtp_from_email": "noreply@example.com"}
	if err := sendCode(context.Background(), values, "member@example.com", "Ab1234", "login", 600, nil); err == nil {
		t.Fatal("plaintext SMTP was accepted")
	}
	if _, _, err := (loginAuth{host: "example.com"}).Start(&smtp.ServerInfo{Name: "example.com"}); err == nil {
		t.Fatal("plaintext LOGIN was accepted")
	}
}

func TestSMTPRefusesSTARTTLSDowngrade(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	commands := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			commands <- "accept failed"
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		fmt.Fprint(conn, "220 localhost ESMTP\r\n")
		reader := bufio.NewReader(conn)
		_, _ = reader.ReadString('\n')
		fmt.Fprint(conn, "250-localhost\r\n250 AUTH PLAIN LOGIN\r\n")
		next, _ := reader.ReadString('\n')
		commands <- next
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	values := Values{"smtp_host": host, "smtp_port": port, "smtp_tls_mode": "starttls", "smtp_username": "secret-user", "smtp_password": "secret-password", "smtp_from_email": "noreply@example.com", "smtp_timeout_seconds": "3"}
	if err = sendCode(context.Background(), values, "member@example.com", "Ab1234", "login", 600, nil); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatal("STARTTLS downgrade was accepted", err)
	}
	if next := <-commands; next != "" {
		t.Fatal("sent credentials or mail after STARTTLS refusal")
	}
}

func TestSMTPCancelClosesStalledConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	closed := make(chan bool, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			closed <- false
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		var b [1]byte
		_, err = conn.Read(b[:])
		closed <- err == io.EOF
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	values := Values{"smtp_host": host, "smtp_port": port, "smtp_tls_mode": "starttls", "smtp_from_email": "noreply@example.com", "smtp_timeout_seconds": "3"}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err = sendCode(ctx, values, "member@example.com", "Ab1234", "login", 600, nil); err == nil {
		t.Fatal("stalled SMTP succeeded")
	}
	if time.Since(start) > time.Second {
		t.Fatal("SMTP did not honor request cancellation")
	}
	if !<-closed {
		t.Fatal("cancelled SMTP connection remained open")
	}
}
