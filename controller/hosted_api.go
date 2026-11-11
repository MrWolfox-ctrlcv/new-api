package controller

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func GetHostedAPIs(c *gin.Context) {
	account, upstreams, usage, group, err := model.GetHostedState(c.GetInt("id"))
	if err != nil {
		hostedManagementError(c, err)
		return
	}
	connections, tokens := model.HostedLimits(group)
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"upstreams": upstreams, "platform_fallback": account.PlatformFallback, "connection_limit": connections, "daily_token_limit": tokens, "used_tokens": usage.Used, "day": usage.Day, "tier": group, "base_path": "/v1/hosted"}})
}

// Bind a one-use security proof to the exact mutation body, without putting
// credentials into authentication flows, audit events, or verification dialogs.
func hostedMutation(c *gin.Context, action string, id int) ([]byte, bool) {
	data, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10))
	if err != nil {
		hostedManagementError(c, errors.New("Invalid hosted API request"))
		return nil, false
	}
	hash := sha256.Sum256(data)
	context, _ := common.Marshal(service.HostedMutationContext{Action: action, UpstreamID: id, Digest: hex.EncodeToString(hash[:])})
	if middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeHostedManage, Context: context}) == nil {
		return nil, false
	}
	return data, true
}

func SaveHostedAPI(c *gin.Context) {
	id := 0
	if c.Param("id") != "" {
		var err error
		id, err = strconv.Atoi(c.Param("id"))
		if err != nil || id <= 0 {
			hostedManagementError(c, gorm.ErrRecordNotFound)
			return
		}
	}
	data, ok := hostedMutation(c, "save", id)
	if !ok {
		return
	}
	var input struct {
		Name    string   `json:"name"`
		BaseURL string   `json:"base_url"`
		Key     string   `json:"key"`
		Models  []string `json:"models"`
		Enabled bool     `json:"enabled"`
	}
	if common.Unmarshal(data, &input) != nil || len(strings.TrimSpace(input.Name)) == 0 || utf8.RuneCountInString(input.Name) > 80 || len(input.Models) == 0 || len(input.Models) > 100 {
		hostedManagementError(c, errors.New("Invalid hosted API request"))
		return
	}
	base, err := service.ValidateHostedURL(input.BaseURL)
	if err != nil {
		hostedManagementError(c, err)
		return
	}
	for _, name := range input.Models {
		if strings.TrimSpace(name) != name || name == "" || len(name) > 200 || strings.ContainsAny(name, "\r\n,") {
			hostedManagementError(c, errors.New("Invalid hosted model list"))
			return
		}
	}
	value := model.HostedUpstream{ID: id, UserID: c.GetInt("id"), Name: strings.TrimSpace(input.Name), BaseURL: base, Enabled: input.Enabled}
	value.CredentialID = uuid.NewString()
	if input.Key != "" || id == 0 {
		if len(input.Key) < 8 || len(input.Key) > 4096 || strings.ContainsAny(input.Key, "\r\n") {
			hostedManagementError(c, errors.New("Invalid upstream API key"))
			return
		}
		if err = service.EncryptHostedKey(&value, input.Key); err != nil {
			hostedManagementError(c, err)
			return
		}
	}
	slices.Sort(input.Models)
	input.Models = slices.Compact(input.Models)
	models, _ := common.Marshal(input.Models)
	value.Models = string(models)
	if err = model.SaveHostedUpstream(value.UserID, &value); err != nil {
		hostedManagementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": value})
}

func DeleteHostedAPI(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		hostedManagementError(c, gorm.ErrRecordNotFound)
		return
	}
	if _, ok := hostedMutation(c, "delete", id); !ok {
		return
	}
	if err = model.DeleteHostedUpstream(c.GetInt("id"), id); err != nil {
		hostedManagementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func UpdateHostedSettings(c *gin.Context) {
	data, ok := hostedMutation(c, "fallback", 0)
	if !ok {
		return
	}
	var input struct {
		PlatformFallback *bool `json:"platform_fallback"`
	}
	if common.Unmarshal(data, &input) != nil || input.PlatformFallback == nil {
		hostedManagementError(c, errors.New("Invalid hosted API request"))
		return
	}
	if err := model.SetHostedFallback(c.GetInt("id"), *input.PlatformFallback); err != nil {
		hostedManagementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func hostedManagementError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	message := "Hosted API operation failed"
	// Only controlled validation messages may cross this boundary.
	allowed := []string{"Hosted API allowance exceeded", "Invalid hosted API request", "Invalid hosted model list", "Enter the upstream API key again when changing its Base URL", "Invalid upstream API key", "Hosted upstream must use a public HTTPS Base URL without credentials or query parameters", "Private upstream destinations are not allowed", "Hosted API encryption requires a persistent secret of at least 32 characters"}
	if slices.Contains(allowed, err.Error()) {
		status = http.StatusBadRequest
		message = err.Error()
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		status = http.StatusNotFound
		message = "Hosted upstream not found"
	}
	c.JSON(status, gin.H{"success": false, "message": common.NewMessage(message)})
}

func ListHostedModels(c *gin.Context) {
	_, upstreams, _, group, err := model.GetHostedState(c.GetInt("id"))
	if err != nil {
		hostedRelayError(c, 500, "hosted_unavailable")
		return
	}
	limit, _ := model.HostedLimits(group)
	token := c.MustGet("hosted_token").(*model.Token)
	seen := map[string]bool{}
	data := []gin.H{}
	for _, upstream := range upstreams[:min(limit, len(upstreams))] {
		if !upstream.Enabled {
			continue
		}
		var names []string
		if common.UnmarshalJsonStr(upstream.Models, &names) != nil {
			continue
		}
		for _, name := range names {
			if !seen[name] && hostedModelAllowed(token, name) {
				seen[name] = true
				data = append(data, gin.H{"id": name, "object": "model", "owned_by": "user"})
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}

func hostedModelAllowed(token *model.Token, name string) bool {
	return !token.ModelLimitsEnabled || token.GetModelLimitsMap()[name]
}

// Returning without Abort is permitted only before a private completion starts
// and only after the account explicitly enabled wallet-backed fallback.
func HostedRelay(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		hostedRelayError(c, 400, "invalid_hosted_request")
		return
	}
	data, err := storage.Bytes()
	if err != nil {
		hostedRelayError(c, 400, "invalid_hosted_request")
		return
	}
	request, bound, err := validateHostedRequest(data)
	if err != nil {
		hostedRelayError(c, 400, "invalid_hosted_request")
		return
	}
	token := c.MustGet("hosted_token").(*model.Token)
	if !hostedModelAllowed(token, request.Model) {
		hostedRelayError(c, 403, "model_not_allowed")
		return
	}
	account, upstreams, _, group, err := model.GetHostedState(token.UserId)
	if err != nil {
		hostedRelayError(c, 503, "hosted_unavailable")
		return
	}
	limit, _ := model.HostedLimits(group)
	status, code := http.StatusBadGateway, "hosted_upstream_failed"
	for _, upstream := range upstreams[:min(limit, len(upstreams))] {
		if !upstream.Enabled {
			continue
		}
		var models []string
		if common.UnmarshalJsonStr(upstream.Models, &models) != nil || !slices.Contains(models, request.Model) {
			continue
		}
		reservation, reserveErr := model.ReserveHostedTokens(token.UserId, bound, time.Now())
		if reserveErr != nil {
			if errors.Is(reserveErr, model.ErrHostedBusy) {
				hostedRelayError(c, 429, "hosted_busy")
				return
			}
			if !errors.Is(reserveErr, model.ErrHostedLimit) {
				hostedRelayError(c, 503, "hosted_unavailable")
				return
			}
			status, code = http.StatusTooManyRequests, "hosted_daily_limit"
			break
		}
		actual := bound
		key, keyErr := service.DecryptHostedKey(&upstream)
		if keyErr != nil {
			if err = model.SettleHostedTokens(token.UserId, reservation.ID, 0); err != nil {
				hostedRelayError(c, 503, "hosted_settlement_failed")
				return
			}
			continue
		}
		base, urlErr := service.ValidateHostedURL(upstream.BaseURL)
		if urlErr != nil {
			hostedRelayError(c, 503, "hosted_unavailable")
			return
		}
		outgoing, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, base+"/chat/completions", bytes.NewReader(request.Body))
		if err != nil {
			hostedRelayError(c, 503, "hosted_unavailable")
			return
		}
		outgoing.Header.Set("Authorization", "Bearer "+key)
		outgoing.Header.Set("Content-Type", "application/json")
		response, requestErr := service.HostedHTTPClient.Do(outgoing)
		if requestErr != nil {
			// The server may already have generated tokens when transport fails.
			if err = model.SettleHostedTokens(token.UserId, reservation.ID, actual); err != nil {
				hostedRelayError(c, 503, "hosted_settlement_failed")
				return
			}
			if c.Request.Context().Err() != nil {
				c.Abort()
				return
			}
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			_ = response.Body.Close()
			if err = model.SettleHostedTokens(token.UserId, reservation.ID, 0); err != nil {
				hostedRelayError(c, 503, "hosted_settlement_failed")
				return
			}
			continue
		}
		if request.Stream {
			media, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
			if mediaErr != nil || media != "text/event-stream" {
				_ = response.Body.Close()
				_ = model.SettleHostedTokens(token.UserId, reservation.ID, bound)
				hostedRelayError(c, 502, "invalid_hosted_response")
				return
			}
			c.Header("Content-Type", "text/event-stream")
			c.Header("Cache-Control", "no-cache")
			c.Header("X-Accel-Buffering", "no")
			c.Header("X-Hosted-Route", "private")
			actual = forwardHostedStream(c, response.Body, bound)
		} else {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
			if readErr != nil || len(body) > 16<<20 {
				_ = response.Body.Close()
				_ = model.SettleHostedTokens(token.UserId, reservation.ID, bound)
				hostedRelayError(c, 502, "invalid_hosted_response")
				return
			}
			actual = hostedUsage(body, bound)
			if actual < 0 {
				_ = response.Body.Close()
				_ = model.SettleHostedTokens(token.UserId, reservation.ID, bound)
				hostedRelayError(c, 502, "hosted_usage_missing")
				return
			}
			// Settle before emitting a successful buffered response.
			if err = model.SettleHostedTokens(token.UserId, reservation.ID, actual); err != nil {
				_ = response.Body.Close()
				hostedRelayError(c, 503, "hosted_settlement_failed")
				return
			}
			c.Header("X-Hosted-Route", "private")
			c.Data(http.StatusOK, "application/json", body)
		}
		_ = response.Body.Close()
		if request.Stream {
			if err = model.SettleHostedTokens(token.UserId, reservation.ID, actual); err != nil {
				common.SysError("Hosted stream settlement failed; reservation retained")
			}
		}
		c.Abort()
		return
	}
	if account.PlatformFallback {
		// The next handlers authenticate monetary quota, select normal permitted
		// groups and run the unchanged native pricing/settlement pipeline.
		c.Request.URL.Path = "/v1/chat/completions"
		_, _ = storage.Seek(0, io.SeekStart)
		c.Request.Body = io.NopCloser(storage)
		c.Header("X-Hosted-Route", "platform")
		return
	}
	hostedRelayError(c, status, code)
}

type hostedChatRequest struct {
	Model  string
	Stream bool
	Body   []byte
}

func validateHostedRequest(data []byte) (hostedChatRequest, int64, error) {
	var fields map[string]common.RawMessage
	bad := errors.New("Invalid hosted request")
	if common.Unmarshal(data, &fields) != nil || len(fields) == 0 {
		return hostedChatRequest{}, 0, bad
	}
	allowed := strings.Fields("model messages max_tokens max_completion_tokens stream stream_options temperature top_p n tools tool_choice parallel_tool_calls response_format seed stop logprobs top_logprobs presence_penalty frequency_penalty logit_bias user reasoning_effort")
	for key := range fields {
		if !slices.Contains(allowed, key) {
			return hostedChatRequest{}, 0, bad
		}
	}
	var request hostedChatRequest
	if common.Unmarshal(fields["model"], &request.Model) != nil || request.Model == "" || len(request.Model) > 200 {
		return request, 0, bad
	}
	if raw, ok := fields["stream"]; ok && common.Unmarshal(raw, &request.Stream) != nil {
		return request, 0, bad
	}
	var messages []struct {
		Role    string            `json:"role"`
		Content common.RawMessage `json:"content"`
	}
	if common.Unmarshal(fields["messages"], &messages) != nil || len(messages) == 0 || len(messages) > 2048 {
		return request, 0, bad
	}
	var messageFields []map[string]common.RawMessage
	if common.Unmarshal(fields["messages"], &messageFields) != nil {
		return request, 0, bad
	}
	for _, message := range messageFields {
		for key := range message {
			if !slices.Contains([]string{"role", "content", "name", "tool_call_id", "tool_calls", "refusal"}, key) {
				return request, 0, bad
			}
		}
	}
	for _, message := range messages {
		if !slices.Contains([]string{"system", "developer", "user", "assistant", "tool"}, message.Role) {
			return request, 0, bad
		}
		if len(message.Content) == 0 || string(message.Content) == "null" {
			continue
		}
		if common.GetJsonType(message.Content) == "string" {
			continue
		}
		var content []struct {
			Type string  `json:"type"`
			Text *string `json:"text"`
		}
		if common.Unmarshal(message.Content, &content) != nil {
			return request, 0, bad
		}
		for _, part := range content {
			if part.Type != "text" || part.Text == nil {
				return request, 0, bad
			}
		}
	}
	var tools []struct {
		Type string `json:"type"`
	}
	if raw, ok := fields["tools"]; ok {
		if common.Unmarshal(raw, &tools) != nil {
			return request, 0, bad
		}
		for _, tool := range tools {
			if tool.Type != "function" {
				return request, 0, bad
			}
		}
	}
	if raw, ok := fields["n"]; ok {
		var n int
		if common.Unmarshal(raw, &n) != nil || n != 1 {
			return request, 0, bad
		}
	}
	maxOutput := int64(4096)
	if fields["max_tokens"] != nil && fields["max_completion_tokens"] != nil {
		return request, 0, bad
	}
	for _, key := range []string{"max_tokens", "max_completion_tokens"} {
		if raw, ok := fields[key]; ok {
			if common.Unmarshal(raw, &maxOutput) != nil || maxOutput < 1 || maxOutput > 32768 {
				return request, 0, bad
			}
		}
	}
	if fields["max_tokens"] == nil && fields["max_completion_tokens"] == nil {
		fields["max_tokens"], _ = common.Marshal(maxOutput)
	}
	if request.Stream {
		fields["stream_options"] = common.RawMessage(`{"include_usage":true}`)
	}
	request.Body, _ = common.Marshal(fields)
	// UTF-8 bytes conservatively cover text tokenization, plus per-message/tool
	// framing. A service-specific 32K output cap bounds all reservations.
	bound := int64(len(data))*2 + int64(len(messages))*32 + 4096 + maxOutput
	return request, bound, nil
}

func hostedUsage(data []byte, bound int64) int64 {
	var envelope struct {
		Usage *struct {
			Prompt     *int64 `json:"prompt_tokens"`
			Completion *int64 `json:"completion_tokens"`
			Total      *int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if common.Unmarshal(data, &envelope) != nil || envelope.Usage == nil || envelope.Usage.Prompt == nil || envelope.Usage.Completion == nil {
		return -1
	}
	input, output := *envelope.Usage.Prompt, *envelope.Usage.Completion
	if input < 0 || output < 0 || input > bound || output > bound-input || input+output == 0 {
		return -1
	}
	if envelope.Usage.Total != nil && *envelope.Usage.Total != input+output {
		return -1
	}
	// OpenAI cached tokens are a subset of prompt_tokens, never added twice.
	return input + output
}

func forwardHostedStream(c *gin.Context, body io.Reader, bound int64) int64 {
	scanner := bufio.NewScanner(io.LimitReader(body, (16<<20)+1))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	usage := int64(-1)
	done := false
	total := 0
	event := []string{}
	for scanner.Scan() {
		line := scanner.Text()
		total += len(line) + 1
		if total > 16<<20 {
			break
		}
		if line == "" {
			payload := strings.Join(event, "\n")
			event = nil
			if payload == "[DONE]" {
				done = true
			} else if n := hostedUsage([]byte(payload), bound); n >= 0 {
				usage = n
			}
		} else if data, ok := strings.CutPrefix(line, "data:"); ok {
			event = append(event, strings.TrimPrefix(data, " "))
		}
		if _, err := c.Writer.WriteString(line + "\n"); err != nil {
			return bound
		}
		c.Writer.Flush()
		if done {
			break
		}
	}
	if done && scanner.Err() == nil && usage >= 0 && c.Request.Context().Err() == nil {
		return usage
	}
	return bound
}

func hostedRelayError(c *gin.Context, status int, code string) {
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"message": i18n.T(c, "hosted."+code), "type": "hosted_api_error", "code": code}})
}
