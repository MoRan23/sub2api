//go:build unit

package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"net"
	"net/smtp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// net/smtp permits plaintext PLAIN authentication for localhost, 127.0.0.1
// and ::1. Use another loopback address so losing the TLS state is observable.
func startContextTLSSMTPServer(t *testing.T) (*fakeSMTPServer, int) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"smtp.test"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.2")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	previousPool := smtpTestRootCAs
	smtpTestRootCAs = pool
	t.Cleanup(func() { smtpTestRootCAs = previousPool })

	listener, err := net.Listen("tcp", "127.0.0.2:0")
	require.NoError(t, err)
	srv := &fakeSMTPServer{tlsConfig: &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS12,
	}}
	srv.listener = tls.NewListener(listener, srv.tlsConfig)
	t.Cleanup(func() {
		_ = srv.listener.Close()
		srv.wg.Wait()
	})
	srv.wg.Add(1)
	go func() {
		defer srv.wg.Done()
		for {
			conn, err := srv.listener.Accept()
			if err != nil {
				return
			}
			srv.conns.Add(1)
			srv.wg.Add(1)
			go func() {
				defer srv.wg.Done()
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				srv.serve(conn, false)
			}()
		}
	}()
	return srv, listener.Addr().(*net.TCPAddr).Port
}

func TestSMTPContextImplicitTLSAuthenticatesNonLocalhost(t *testing.T) {
	srv, _ := startContextTLSSMTPServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Dial loopback directly while verifying the certificate for smtp.test;
	// no external DNS or disabled certificate verification is involved.
	conn, err := (&tls.Dialer{Config: smtpTLSConfig("smtp.test")}).DialContext(ctx, "tcp", srv.listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()
	client, err := newSMTPClientContext(ctx, conn, "smtp.test")
	require.NoError(t, err)
	defer client.Close()
	state, ok := client.TLSConnectionState()
	require.True(t, ok, "net/smtp must recognize the original TLS transport")
	require.True(t, state.HandshakeComplete)
	require.NotEmpty(t, state.VerifiedChains)
	require.NoError(t, client.Auth(smtp.PlainAuth("", "user", "pass", "smtp.test")))
	require.True(t, srv.sawCommand("AUTH PLAIN"))
	require.NoError(t, client.Quit())
}

func TestSMTPImplicitTLSNonLocalhostConfigPaths(t *testing.T) {
	for _, mode := range []string{"legacy_use_tls", "explicit_implicit"} {
		t.Run(mode, func(t *testing.T) {
			for _, operation := range []string{"test_connection", "send_email"} {
				t.Run(operation, func(t *testing.T) {
					srv, port := startContextTLSSMTPServer(t)
					config := smtpTestConfig(port, mode == "legacy_use_tls")
					config.Host = "127.0.0.2"
					if mode == "explicit_implicit" {
						config.TLSMode = "implicit"
					}
					svc := &EmailService{}
					if operation == "test_connection" {
						require.NoError(t, svc.TestSMTPConnectionWithConfig(config))
					} else {
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						require.NoError(t, svc.SendEmailWithConfigAndOptionsContext(ctx, config,
							"recipient@example.test", "TLS regression", "test body", EmailSendOptions{}))
						require.True(t, srv.sawCommand("DATA"))
					}
					require.True(t, srv.sawCommand("AUTH PLAIN"))
					require.False(t, srv.sawCommand("STARTTLS"))
					require.EqualValues(t, 1, srv.conns.Load())
				})
			}
		})
	}
}
