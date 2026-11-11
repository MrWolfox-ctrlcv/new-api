package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type hostedTransport func(*http.Request) (*http.Response, error)

func (f hostedTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func hostedFixture(t *testing.T) (*model.User, *model.Token, *gin.Engine) {
	t.Helper()
	require.NoError(t, i18n.Init())
	previousDB, previousSecret, previousClient := model.DB, common.CryptoSecret, service.HostedHTTPClient
	previousType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	common.CryptoSecret = strings.Repeat("local-test-secret/", 3)
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		model.DB = previousDB
		common.CryptoSecret = previousSecret
		service.HostedHTTPClient = previousClient
		common.SetMainDatabaseType(previousType)
		_ = sqlDB.Close()
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.HostedAccount{}, &model.HostedUpstream{}, &model.HostedUsage{}, &model.HostedReservation{}))
	user := &model.User{Username: "hosted-user", Password: "unused", Status: common.UserStatusEnabled, Role: common.RoleCommonUser, Group: "default", Quota: 12345}
	require.NoError(t, db.Create(user).Error)
	token := &model.Token{UserId: user.Id, Key: "hostedfixturekey123456789", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 0, Group: "default"}
	require.NoError(t, db.Create(token).Error)
	upstream := model.HostedUpstream{UserID: user.Id, CredentialID: "fixture", Name: "my upstream", BaseURL: "https://upstream.example/v1", Models: `["chat-a","chat-b"]`, Enabled: true}
	require.NoError(t, service.EncryptHostedKey(&upstream, "sk-upstream-fixture"))
	require.NoError(t, model.SaveHostedUpstream(user.Id, &upstream))
	engine := gin.New()
	engine.Use(middleware.BodyStorageCleanup())
	engine.POST("/v1/hosted/chat/completions", middleware.HostedTokenAuth(), HostedRelay, func(c *gin.Context) {
		require.Equal(t, "/v1/chat/completions", c.Request.URL.Path)
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		var request map[string]any
		require.NoError(t, common.Unmarshal(body, &request))
		require.Equal(t, "chat-a", request["model"])
		c.Data(418, "text/plain", []byte("platform-pipeline"))
	})
	engine.GET("/v1/hosted/models", middleware.HostedTokenAuth(), ListHostedModels)
	return user, token, engine
}

func hostedCall(engine *gin.Engine, token *model.Token, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/hosted/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer sk-"+token.Key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

const hostedTextRequest = `{"model":"chat-a","messages":[{"role":"user","content":"Hello"}],"max_tokens":100}`

func TestHostedRelaySettlesUsageWithoutSpendingPlatformQuota(t *testing.T) {
	user, token, engine := hostedFixture(t)
	service.HostedHTTPClient = &http.Client{Transport: hostedTransport(func(request *http.Request) (*http.Response, error) {
		require.Equal(t, "https://upstream.example/v1/chat/completions", request.URL.String())
		require.Equal(t, "Bearer sk-upstream-fixture", request.Header.Get("Authorization"))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"Hi"}}],"usage":{"prompt_tokens":20,"completion_tokens":10,"total_tokens":30,"prompt_tokens_details":{"cached_tokens":12}}}`))}, nil
	})}
	response := hostedCall(engine, token, hostedTextRequest)
	require.Equal(t, 200, response.Code)
	require.Equal(t, "private", response.Header().Get("X-Hosted-Route"))
	_, _, usage, _, err := model.GetHostedState(user.Id)
	require.NoError(t, err)
	require.EqualValues(t, 30, usage.Used)
	var after model.User
	require.NoError(t, model.DB.First(&after, user.Id).Error)
	require.Equal(t, user.Quota, after.Quota)
	var afterToken model.Token
	require.NoError(t, model.DB.First(&afterToken, token.Id).Error)
	require.Zero(t, afterToken.UsedQuota)
}

func TestHostedFallbackRequiresAccountOptInAndPreservesBody(t *testing.T) {
	user, token, engine := hostedFixture(t)
	service.HostedHTTPClient = &http.Client{Transport: hostedTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("untrusted secret error"))}, nil
	})}
	response := hostedCall(engine, token, hostedTextRequest)
	require.Equal(t, 502, response.Code)
	require.NotContains(t, response.Body.String(), "untrusted secret")
	_, _, usage, _, err := model.GetHostedState(user.Id)
	require.NoError(t, err)
	require.Zero(t, usage.Used, "Explicit rejected completions are refunded")
	require.NoError(t, model.SetHostedFallback(user.Id, true))
	response = hostedCall(engine, token, hostedTextRequest)
	require.Equal(t, 418, response.Code)
	require.Equal(t, "platform", response.Header().Get("X-Hosted-Route"))
	// A depleted hosted allowance also requires this exact opt-in.
	reservation, err := model.ReserveHostedTokens(user.Id, 10_000_000, time.Now())
	require.NoError(t, err)
	require.NotNil(t, reservation)
	response = hostedCall(engine, token, hostedTextRequest)
	require.Equal(t, 418, response.Code)
	require.NoError(t, model.SetHostedFallback(user.Id, false))
	response = hostedCall(engine, token, hostedTextRequest)
	require.Equal(t, 429, response.Code)
}

func TestHostedMissingUsageAndTransportFailureKeepReservation(t *testing.T) {
	for _, failure := range []string{"usage missing", "invalid usage", "transport"} {
		t.Run(failure, func(t *testing.T) {
			user, token, engine := hostedFixture(t)
			service.HostedHTTPClient = &http.Client{Transport: hostedTransport(func(*http.Request) (*http.Response, error) {
				if failure == "transport" {
					return nil, errors.New("secret network failure")
				}
				body := `{"choices":[{"message":{"content":"Hi"}}]}`
				if failure == "invalid usage" {
					body = `{"usage":{"prompt_tokens":-1,"completion_tokens":10}}`
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			require.NoError(t, model.SetHostedFallback(user.Id, true))
			response := hostedCall(engine, token, hostedTextRequest)
			if failure == "transport" {
				require.Equal(t, 418, response.Code)
			} else {
				require.Equal(t, 502, response.Code)
				require.NotEqual(t, "platform", response.Header().Get("X-Hosted-Route"))
			}
			_, bound, err := validateHostedRequest([]byte(hostedTextRequest))
			require.NoError(t, err)
			_, _, usage, _, err := model.GetHostedState(user.Id)
			require.NoError(t, err)
			require.Equal(t, bound, usage.Used)
		})
	}
}

func TestHostedStreamingRejectsBufferedJSONBeforeSendingHeaders(t *testing.T) {
	user, token, engine := hostedFixture(t)
	require.NoError(t, model.SetHostedFallback(user.Id, true))
	service.HostedHTTPClient = &http.Client{Transport: hostedTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"usage":{"prompt_tokens":10,"completion_tokens":5}}`))}, nil
	})}
	body := strings.TrimSuffix(hostedTextRequest, "}") + `,"stream":true}`
	response := hostedCall(engine, token, body)
	require.Equal(t, 502, response.Code)
	require.Empty(t, response.Header().Get("X-Hosted-Route"))
	_, bound, err := validateHostedRequest([]byte(body))
	require.NoError(t, err)
	_, _, usage, _, err := model.GetHostedState(user.Id)
	require.NoError(t, err)
	require.Equal(t, bound, usage.Used)
}

func TestHostedStreamingCompletionAndAbortNeverFallThroughToPlatform(t *testing.T) {
	for _, complete := range []bool{true, false} {
		t.Run(strconvBool(complete), func(t *testing.T) {
			user, token, engine := hostedFixture(t)
			require.NoError(t, model.SetHostedFallback(user.Id, true))
			service.HostedHTTPClient = &http.Client{Transport: hostedTransport(func(request *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(request.Body)
				require.NoError(t, err)
				require.Contains(t, string(body), `"include_usage":true`)
				stream := "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n"
				if complete {
					stream += "data: {\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":10}}\n\ndata: [DONE]\n\n"
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}, nil
			})}
			body := strings.TrimSuffix(hostedTextRequest, "}") + `,"stream":true}`
			response := hostedCall(engine, token, body)
			require.Equal(t, 200, response.Code)
			require.Equal(t, "private", response.Header().Get("X-Hosted-Route"))
			require.Contains(t, response.Body.String(), "Hi")
			_, _, usage, _, err := model.GetHostedState(user.Id)
			require.NoError(t, err)
			if complete {
				require.EqualValues(t, 30, usage.Used)
			} else {
				_, bound, err := validateHostedRequest([]byte(body))
				require.NoError(t, err)
				require.Equal(t, bound, usage.Used)
			}
		})
	}
}

func strconvBool(value bool) string {
	if value {
		return "complete"
	}
	return "interrupted"
}

func TestHostedAuthRejectsExpiredDisabledForeignAndModelRestrictedKeys(t *testing.T) {
	_, token, engine := hostedFixture(t)
	for _, status := range []int{common.TokenStatusDisabled, common.TokenStatusExpired} {
		require.NoError(t, model.DB.Model(token).Update("status", status).Error)
		require.Equal(t, 401, hostedCall(engine, token, hostedTextRequest).Code)
	}
	require.NoError(t, model.DB.Model(token).Updates(map[string]any{"status": common.TokenStatusEnabled, "expired_time": time.Now().Unix() - 1}).Error)
	require.Equal(t, 401, hostedCall(engine, token, hostedTextRequest).Code)
	require.NoError(t, model.DB.Model(token).Updates(map[string]any{"expired_time": -1, "model_limits_enabled": true, "model_limits": "chat-b"}).Error)
	require.Equal(t, 403, hostedCall(engine, token, hostedTextRequest).Code)
	foreign := *token
	foreign.Key = "anotheraccountkey"
	foreign.Id = 0
	foreign.UserId = 999
	require.NoError(t, model.DB.Create(&foreign).Error)
	require.Equal(t, 401, hostedCall(engine, &foreign, hostedTextRequest).Code)
	request := httptest.NewRequest("GET", "/v1/hosted/models", nil)
	request.Header.Set("Authorization", "Bearer sk-"+token.Key)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	require.Equal(t, 200, recorder.Code)
	require.Contains(t, recorder.Body.String(), "chat-b")
	require.NotContains(t, recorder.Body.String(), "chat-a")
	// Account context is always derived from the validated token, not JSON.
	response := hostedCall(engine, token, `{"model":"chat-b","user_id":999,"messages":[{"role":"user","content":"hi"}]}`)
	require.Equal(t, 400, response.Code)
}

func TestHostedRequestRejectsUnboundedAndNonTextInputs(t *testing.T) {
	for _, body := range []string{
		`{"model":"x","messages":[],"max_tokens":1}`,
		`{"model":"x","messages":[{"content":"hi"}],"max_tokens":-1}`,
		`{"model":"x","messages":[{"content":"hi"}],"max_tokens":18446744073686646784}`,
		`{"model":"x","messages":[{"content":"hi"}],"n":2}`,
		`{"model":"x","messages":[{"content":[{"type":"image_url","image_url":"https://example.com"}]}]}`,
		`{"model":"x","messages":[{"content":"hi"}],"tools":[{"type":"web_search"}]}`,
		`{"model":"x","messages":[{"content":"hi"}],"max_tokens":1,"max_completion_tokens":2}`,
	} {
		_, _, err := validateHostedRequest([]byte(body))
		require.Error(t, err, body)
	}
	_, _, err := validateHostedRequest([]byte(hostedTextRequest))
	require.NoError(t, err)
	for _, body := range []string{`{"usage":{"prompt_tokens":10,"completion_tokens":9223372036854775807}}`, `{"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":10}}`, `{"usage":{"prompt_tokens":-1,"completion_tokens":5}}`, `{"usage":{}}`} {
		require.EqualValues(t, -1, hostedUsage([]byte(body), 1000))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/", nil).WithContext(ctx)
	require.EqualValues(t, 1000, forwardHostedStream(c, strings.NewReader("data: {\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n"), 1000))
}

func TestHostedSecondAccountCannotListOrCallAnotherAccountsModels(t *testing.T) {
	_, token, engine := hostedFixture(t)
	other := model.User{Username: "other-owner", Password: "unused", AffCode: "other-owner", Status: common.UserStatusEnabled, Role: common.RoleCommonUser, Group: "vip"}
	require.NoError(t, model.DB.Create(&other).Error)
	otherKey := *token
	otherKey.Id = 0
	otherKey.UserId = other.Id
	otherKey.Key = "otherrealaccountkey"
	require.NoError(t, model.DB.Create(&otherKey).Error)
	request := httptest.NewRequest("GET", "/v1/hosted/models", nil)
	request.Header.Set("Authorization", "Bearer sk-"+otherKey.Key)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	require.Equal(t, 200, response.Code)
	require.JSONEq(t, `{"object":"list","data":[]}`, response.Body.String())
	service.HostedHTTPClient = &http.Client{Transport: hostedTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("Cross-account request must never reach an upstream")
		return nil, nil
	})}
	require.Equal(t, 502, hostedCall(engine, &otherKey, hostedTextRequest).Code)
}

func TestHostedManagementRequiresOneUseProofAndNeverReturnsSecret(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	logDB := model.LOG_DB
	t.Cleanup(func() {
		connection, err := logDB.DB()
		if err == nil {
			_ = connection.Close()
		}
	})
	require.NoError(t, model.DB.AutoMigrate(&model.HostedAccount{}, &model.HostedUpstream{}, &model.HostedUsage{}, &model.HostedReservation{}))
	old := common.CryptoSecret
	common.CryptoSecret = strings.Repeat("local-fixture/", 4)
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = old })
	body := `{"name":"my private upstream","base_url":"https://upstream.example/v1","key":"sk-secret-upstream","models":["chat-a","chat-b"],"enabled":true,"user_id":999}`
	missing := securityEnrollmentRequest("POST", "/api/hosted/upstreams", body, "", identity, SaveHostedAPI)
	require.Equal(t, 403, missing.Code)
	hash := sha256.Sum256([]byte(body))
	context, _ := common.Marshal(service.HostedMutationContext{Action: "save", UpstreamID: 0, Digest: hex.EncodeToString(hash[:])})
	proof := issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: service.VerificationScopeHostedManage, Context: context}, service.VerificationMethodPassword)
	response := securityEnrollmentRequest("POST", "/api/hosted/upstreams", body, proof, identity, SaveHostedAPI)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.NotContains(t, response.Body.String(), "sk-secret-upstream")
	require.NotContains(t, response.Body.String(), "encrypted_key")
	var upstream model.HostedUpstream
	require.NoError(t, model.DB.First(&upstream).Error)
	require.Equal(t, user.Id, upstream.UserID)
	key, err := service.DecryptHostedKey(&upstream)
	require.NoError(t, err)
	require.Equal(t, "sk-secret-upstream", key)
	replay := securityEnrollmentRequest("POST", "/api/hosted/upstreams", body, proof, identity, SaveHostedAPI)
	require.Equal(t, 403, replay.Code)
	changed := strings.Replace(body, "my private upstream", "different body", 1)
	proof = issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: service.VerificationScopeHostedManage, Context: context}, service.VerificationMethodPassword)
	response = securityEnrollmentRequest("POST", "/api/hosted/upstreams", changed, proof, identity, SaveHostedAPI)
	require.Equal(t, 403, response.Code)
	state := securityEnrollmentRequest("GET", "/api/hosted", "", "", identity, GetHostedAPIs)
	require.Equal(t, 200, state.Code)
	require.NotContains(t, state.Body.String(), "sk-secret-upstream")
	require.NotContains(t, state.Body.String(), upstream.EncryptedKey)
}

func TestHostedMetadataEditsKeepKeyAndChangingDestinationRequiresNewKey(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	logDB := model.LOG_DB
	t.Cleanup(func() {
		connection, err := logDB.DB()
		if err == nil {
			_ = connection.Close()
		}
	})
	require.NoError(t, model.DB.AutoMigrate(&model.HostedAccount{}, &model.HostedUpstream{}, &model.HostedUsage{}, &model.HostedReservation{}))
	old := common.CryptoSecret
	common.CryptoSecret = strings.Repeat("local-key-update/", 4)
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = old })
	upstream := model.HostedUpstream{UserID: user.Id, CredentialID: "update-fixture", BaseURL: "https://upstream.example/v1", Models: `["chat-a"]`, Enabled: true}
	require.NoError(t, service.EncryptHostedKey(&upstream, "sk-current-private-key"))
	require.NoError(t, model.SaveHostedUpstream(user.Id, &upstream))
	for _, changeDestination := range []bool{false, true} {
		base := "https://upstream.example/v1"
		if changeDestination {
			base = "https://different.example/v1"
		}
		bodyBytes, err := common.Marshal(map[string]any{"name": "renamed", "base_url": base, "key": "", "models": []string{"chat-a"}, "enabled": false})
		require.NoError(t, err)
		hash := sha256.Sum256(bodyBytes)
		context, _ := common.Marshal(service.HostedMutationContext{Action: "save", UpstreamID: upstream.ID, Digest: hex.EncodeToString(hash[:])})
		proof := issueSecurityEnrollmentProof(t, identity, service.VerificationOperation{Scope: service.VerificationScopeHostedManage, Context: context}, service.VerificationMethodPassword)
		response := securityEnrollmentRequest("PUT", "/api/hosted/upstreams/1", string(bodyBytes), proof, identity, withAccessTokenID(SaveHostedAPI, upstream.ID))
		if changeDestination {
			require.Equal(t, 400, response.Code)
		} else {
			require.Equal(t, 200, response.Code, response.Body.String())
		}
		var current model.HostedUpstream
		require.NoError(t, model.DB.First(&current, upstream.ID).Error)
		require.Equal(t, upstream.EncryptedKey, current.EncryptedKey)
		require.Equal(t, upstream.BaseURL, current.BaseURL)
		require.False(t, current.Enabled)
	}
}
