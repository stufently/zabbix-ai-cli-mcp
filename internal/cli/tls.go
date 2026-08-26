package cli

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"os"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/config"
	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
)

// transportFor builds the HTTP transport for a profile.
//
// Verification is on unless a profile explicitly turns it off, and a custom
// certificate authority is offered first, because "just disable the check" is
// the answer people reach for when the real problem is a private CA.
func transportFor(p config.Profile) (http.RoundTripper, error) {
	if p.CAFile == "" && !p.Insecure {
		return http.DefaultTransport, nil
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if p.CAFile != "" {
		pem, err := os.ReadFile(p.CAFile)
		if err != nil {
			return nil, errs.Usage("could not read ca_file %s: %v", p.CAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errs.Usage("ca_file %s contains no certificates", p.CAFile)
		}
		tlsConfig.RootCAs = pool
	}
	if p.Insecure {
		tlsConfig.InsecureSkipVerify = true
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = tlsConfig
	return base, nil
}
