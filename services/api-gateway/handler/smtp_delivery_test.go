package handler

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestSMTPRequiredTLSAcceptanceAndUnresolvedOutcome(t *testing.T) {
	certServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	certificate := certServer.TLS.Certificates[0]
	cert := certServer.Certificate()
	certServer.Close()
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	for _, mode := range []string{"tls", "starttls"} {
		for _, accept := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/accept=%v", mode, accept), func(t *testing.T) {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				finished := make(chan string, 1)
				go func() {
					conn, err := listener.Accept()
					if err != nil {
						finished <- err.Error()
						return
					}
					defer conn.Close()
					if mode == "tls" {
						conn = tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
					}
					reader := bufio.NewReader(conn)
					_, _ = fmt.Fprint(conn, "220 local SMTP test\r\n")
					var message strings.Builder
					for {
						line, e := reader.ReadString('\n')
						if e != nil {
							finished <- message.String()
							return
						}
						switch {
						case strings.HasPrefix(line, "EHLO"):
							if mode == "starttls" {
								_, _ = fmt.Fprint(conn, "250-local\r\n250 STARTTLS\r\n")
							} else {
								_, _ = fmt.Fprint(conn, "250 local\r\n")
							}
						case strings.HasPrefix(line, "STARTTLS"):
							_, _ = fmt.Fprint(conn, "220 Start TLS\r\n")
							conn = tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
							reader = bufio.NewReader(conn)
						case strings.HasPrefix(line, "MAIL"), strings.HasPrefix(line, "RCPT"):
							_, _ = fmt.Fprint(conn, "250 OK\r\n")
						case strings.HasPrefix(line, "DATA"):
							_, _ = fmt.Fprint(conn, "354 Send body\r\n")
							for {
								bodyLine, e := reader.ReadString('\n')
								if e != nil {
									finished <- message.String()
									return
								}
								if bodyLine == ".\r\n" {
									break
								}
								message.WriteString(bodyLine)
							}
							if !accept {
								finished <- message.String()
								return
							}
							_, _ = fmt.Fprint(conn, "250 Accepted\r\n")
						case strings.HasPrefix(line, "QUIT"):
							_, _ = fmt.Fprint(conn, "221 Bye\r\n")
							finished <- message.String()
							return
						default:
							_, _ = fmt.Fprint(conn, "500 Unexpected\r\n")
						}
					}
				}()
				host, port, _ := net.SplitHostPort(listener.Addr().String())
				number, _ := strconv.Atoi(port)
				s := emailDeliverySettings{Host: host, Port: number, TLSMode: mode, FromEmail: "otp@example.test", FromName: "HeyGo"}
				err = sendSMTPEmailTLS(context.Background(), s, "", "recipient@example.test", "OTP test", "Synthetic code", &tls.Config{ServerName: host, RootCAs: roots, MinVersion: tls.VersionTLS12})
				if accept && err != nil {
					t.Fatal(err)
				}
				if !accept && (err == nil || errors.Is(err, errEmailUnavailable)) {
					t.Fatal("uncertain DATA outcome was fallback-eligible")
				}
				message := <-finished
				if !strings.Contains(message, "Synthetic code") || !strings.Contains(message, "From: ") {
					t.Fatalf("missing SMTP body/envelope: %s", message)
				}
			})
		}
	}
}
