package service

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func TestHostedCredentialsAreEncryptedAndOwnerBound(t *testing.T) {
	old := common.CryptoSecret
	common.CryptoSecret = strings.Repeat("test-only/", 4)
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = old })
	u := model.HostedUpstream{UserID: 123, CredentialID: "fixture-credential", BaseURL: "https://api.example.com/v1"}
	require.NoError(t, EncryptHostedKey(&u, "sk-private-upstream-only"))
	require.NotContains(t, u.EncryptedKey, "sk-private")
	require.Equal(t, "only", u.KeyHint)
	key, err := DecryptHostedKey(&u)
	require.NoError(t, err)
	require.Equal(t, "sk-private-upstream-only", key)
	public, err := common.Marshal(u)
	require.NoError(t, err)
	require.NotContains(t, string(public), u.EncryptedKey)
	require.NotContains(t, string(public), key)
	for _, field := range []string{"owner", "url", "credential"} {
		copy := u
		switch field {
		case "owner":
			copy.UserID++
		case "url":
			copy.BaseURL = "https://evil.example/v1"
		case "credential":
			copy.CredentialID = "replacement"
		}
		_, err = DecryptHostedKey(&copy)
		require.Error(t, err)
	}
	require.Error(t, EncryptHostedKey(&u, "short"))
	common.CryptoSecret = "short"
	_, err = DecryptHostedKey(&u)
	require.Error(t, err)
}

func TestHostedDestinationRejectsSSRFAndRedirects(t *testing.T) {
	for _, raw := range []string{"http://api.example.com/v1", "https://localhost/v1", "https://127.0.0.1/v1", "https://[::1]/v1", "https://[::ffff:127.0.0.1]/v1", "https://10.2.3.4/v1", "https://169.254.169.254/", "https://100.64.0.1/", "https://example.com:3000/v1", "https://key@example.com/v1", "https://example.com/v1?key=secret", "https://example.com/v1#fragment"} {
		_, err := ValidateHostedURL(raw)
		require.Error(t, err, raw)
	}
	for _, raw := range []string{"127.0.0.1", "::1", "10.0.0.1", "192.168.1.1", "169.254.169.254", "100.100.100.200", "192.0.2.1", "198.19.1.1", "224.0.0.1", "240.0.0.1", "::ffff:10.0.0.1", "64:ff9b::7f00:1", "2002:7f00:1::", "2001:db8::1", "fd00::1", "fe80::1"} {
		require.False(t, hostedPublicIP(netip.MustParseAddr(raw)), raw)
	}
	require.True(t, hostedPublicIP(netip.MustParseAddr("1.1.1.1")))
	require.True(t, hostedPublicIP(netip.MustParseAddr("2606:4700:4700::1111")))
	_, err := hostedDial(context.Background(), "tcp", "127.0.0.1:443")
	require.Error(t, err)
	require.ErrorIs(t, HostedHTTPClient.CheckRedirect(&http.Request{}, nil), http.ErrUseLastResponse)
	transport := HostedHTTPClient.Transport.(*http.Transport)
	require.Nil(t, transport.Proxy)
}

func TestHostedMutationProofBindsActionIDAndBody(t *testing.T) {
	context := HostedMutationContext{Action: "save", UpstreamID: 3, Digest: strings.Repeat("a", 64)}
	data, err := common.Marshal(context)
	require.NoError(t, err)
	before, err := BindVerificationOperation(VerificationOperation{Scope: VerificationScopeHostedManage, Context: data})
	require.NoError(t, err)
	for _, field := range []string{"action", "id", "body"} {
		copy := context
		switch field {
		case "action":
			copy.Action = "delete"
		case "id":
			copy.UpstreamID = 4
		case "body":
			copy.Digest = strings.Repeat("b", 64)
		}
		data, _ = common.Marshal(copy)
		after, err := BindVerificationOperation(VerificationOperation{Scope: VerificationScopeHostedManage, Context: data})
		require.NoError(t, err)
		require.NotEqual(t, before.ContextHash, after.ContextHash)
	}
	_, err = BindVerificationOperation(VerificationOperation{Scope: VerificationScopeHostedManage, Context: []byte(`{"action":"save","upstream_id":1,"digest":"bad"}`)})
	require.Error(t, err)
	methods, err := securityVerificationPolicy(VerificationScopeHostedManage, model.UserVerificationState{HasPassword: true})
	require.NoError(t, err)
	require.Equal(t, VerificationMethodPassword, methods[0].Method)
	methods, err = securityVerificationPolicy(VerificationScopeHostedManage, model.UserVerificationState{HasPassword: true, HasTwoFA: true})
	require.NoError(t, err)
	require.Equal(t, VerificationMethodTwoFA, methods[0].Method)
}
