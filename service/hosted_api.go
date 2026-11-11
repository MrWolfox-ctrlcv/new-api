package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// Reuse the installation's persistent secret, with a separate derivation domain.
// Deployments must keep CRYPTO_SECRET/SESSION_SECRET stable across restarts.
func hostedCipher() (cipher.AEAD, error) {
	configured := os.Getenv("CRYPTO_SECRET")
	if configured == "" {
		configured = os.Getenv("SESSION_SECRET")
	}
	if len(configured) < 32 || configured != common.CryptoSecret {
		return nil, errors.New("Hosted API encryption requires a persistent secret of at least 32 characters")
	}
	key := sha256.Sum256([]byte("new-api/hosted-credentials/v1\x00" + common.CryptoSecret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func hostedAAD(upstream *model.HostedUpstream) []byte {
	return fmt.Appendf(nil, "%d/%s/%s", upstream.UserID, upstream.CredentialID, upstream.BaseURL)
}

func EncryptHostedKey(upstream *model.HostedUpstream, key string) error {
	if len(key) < 8 || len(key) > 4096 {
		return errors.New("Invalid upstream API key")
	}
	aead, err := hostedCipher()
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	upstream.EncryptedKey = base64.RawStdEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(key), hostedAAD(upstream)))
	upstream.KeyHint = key[len(key)-4:]
	return nil
}

func DecryptHostedKey(upstream *model.HostedUpstream) (string, error) {
	aead, err := hostedCipher()
	if err != nil {
		return "", err
	}
	data, err := base64.RawStdEncoding.DecodeString(upstream.EncryptedKey)
	if err != nil || len(data) < aead.NonceSize() {
		return "", errors.New("Invalid hosted credential")
	}
	plain, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], hostedAAD(upstream))
	return string(plain), err
}

func ValidateHostedURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 512 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || (u.Port() != "" && u.Port() != "443") {
		return "", errors.New("Hosted upstream must use a public HTTPS Base URL without credentials or query parameters")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return "", errors.New("Private upstream destinations are not allowed")
	}
	if ip, err := netip.ParseAddr(host); err == nil && !hostedPublicIP(ip) {
		return "", errors.New("Private upstream destinations are not allowed")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

var hostedDeniedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("64:ff9b::/96"),
}

func hostedPublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return false
	}
	// Permit only the currently allocated global-unicast IPv6 space.
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, prefix := range hostedDeniedPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func hostedDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("Invalid upstream address")
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("Upstream DNS resolution failed")
	}
	// Reject a mixed public/private answer rather than selecting just its public IP.
	for _, ip := range ips {
		if !hostedPublicIP(ip) {
			return nil, errors.New("Private upstream destinations are not allowed")
		}
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	for _, ip := range ips {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, errors.New("Upstream connection failed")
}

// No environment proxy and no redirects: secrets stay at the validated host.
var HostedHTTPClient = &http.Client{
	Timeout:       180 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Transport:     &http.Transport{DialContext: hostedDial, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 45 * time.Second, MaxIdleConns: 20, MaxIdleConnsPerHost: 2, IdleConnTimeout: 60 * time.Second},
}
