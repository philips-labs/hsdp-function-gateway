package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"path"
	"strings"

	socks5 "github.com/armon/go-socks5"
	"github.com/hashicorp/go-hclog"
	"golang.org/x/net/websocket"
)

var (
	certsDir = flag.String("certs_dir", "", "Directory of certs for starting a wss:// server, or empty for ws:// server. Expected files are: cert.pem and key.pem.")
)

type RuleSet []string

func newRuleSet(allowedHosts string) *RuleSet {
	if allowedHosts == "" {
		rs := make(RuleSet, 0)
		return &rs
	}

	nms := strings.Split(allowedHosts, ",")
	rs := make(RuleSet, len(nms))
	for i, nm := range nms {
		hclog.Default().Info("adding", "host", nm)
		rs[i] = nm
	}
	return &rs
}

func (rs *RuleSet) Allow(ctx context.Context, req *socks5.Request) (context.Context, bool) {
	fqdn := fmt.Sprintf("%s:%d", req.DestAddr.FQDN, req.DestAddr.Port)
	if len(*rs) == 0 {
		hclog.Default().Info("allowing as allow-list is empty", "fqdn", fqdn)
		return ctx, true
	}
	for _, host := range *rs {
		hclog.Default().Info("testing", "host", host, "fqdn", fqdn)
		if fqdn == host {
			hclog.Default().Info("allowing as it matches allow-list", "host", host, "fqdn", fqdn)
			return ctx, true
		}
	}
	hclog.Default().Info("denying, not on allow-list", "fqdn", fqdn)
	return ctx, false
}

func getTlsConfig() (*tls.Config, error) {
	tlscfg := &tls.Config{
		ClientAuth:               tls.RequireAndVerifyClientCert,
		ClientCAs:                x509.NewCertPool(),
		CurvePreferences:         []tls.CurveID{tls.CurveP521, tls.CurveP384, tls.CurveP256},
		MinVersion:               tls.VersionTLS12,
		PreferServerCipherSuites: true,
		CipherSuites: []uint16{
			tls.TLS_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
		},
	}
	if ca, err := ioutil.ReadFile(path.Join(*certsDir, "cacert.pem")); err == nil {
		tlscfg.ClientCAs.AppendCertsFromPEM(ca)
	} else {
		return nil, fmt.Errorf("Failed reading CA certificate: %v", err)
	}

	if cert, err := tls.LoadX509KeyPair(path.Join(*certsDir, "/cert.pem"), path.Join(*certsDir, "/key.pem")); err == nil {
		tlscfg.Certificates = append(tlscfg.Certificates, cert)
	} else {
		return nil, fmt.Errorf("Failed reading client certificate: %v", err)
	}

	return tlscfg, nil
}

func setDebugHandlers(mux *http.ServeMux) *http.ServeMux {
	mux.HandleFunc("/generate_204", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	mux.HandleFunc("/success", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("success\n")) })
	return mux
}

func authWrapper(h http.Handler, authToken string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-STL-Auth") != authToken {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r) // call original
	})
}

func NewServerMux(authToken, allowedHosts string) (*http.ServeMux, error) {
	if authToken == "" {
		return nil, fmt.Errorf("expecting non-empty token")
	}
	socks, err := socks5.New(&socks5.Config{Rules: newRuleSet(allowedHosts)})
	if err != nil {
		return nil, err
	}
	httpMux := setDebugHandlers(http.NewServeMux())
	httpMux.Handle("/ws", authWrapper(websocket.Handler(func(conn *websocket.Conn) { socks.ServeConn(conn) }), authToken))
	return httpMux, nil
}
