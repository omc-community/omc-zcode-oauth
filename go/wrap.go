package main

import (
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

//go:embed assets/prompt.json
var promptJSON []byte
var prompt struct {
	CliPrefix         string   `json:"cliPrefix"`
	StableSections    []string `json:"stableSections"`
	BeforeEnvironment string   `json:"beforeEnvironment"`
	AfterEnvironment  string   `json:"afterEnvironment"`
	Environment       struct {
		Heading        string `json:"heading"`
		InvokedLine    string `json:"invokedLine"`
		PlatformLabel  string `json:"platformLabel"`
		OSVersionLabel string `json:"osVersionLabel"`
		CwdLabel       string `json:"cwdLabel"`
		GitLabel       string `json:"gitLabel"`
		GitNo          string `json:"gitNo"`
		ShellLabel     string `json:"shellLabel"`
		UntrustedNote  string `json:"untrustedNote"`
	} `json:"environment"`
	Context struct {
		Intro              string `json:"intro"`
		Outro              string `json:"outro"`
		CurrentDateHeading string `json:"currentDateHeading"`
		CurrentDateLine    string `json:"currentDateLine"`
	} `json:"context"`
}

func init() {
	if err := json.Unmarshal(promptJSON, &prompt); err != nil || len(prompt.StableSections) != 2 {
		panic("invalid embedded ZCode prompt")
	}
}

var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func stableUUID(seed string) string {
	h := sha256.Sum256([]byte(seed))
	h[6] = (h[6] & 15) | 64
	h[8] = (h[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[:4], h[4:6], h[6:8], h[8:10], h[10:16])
}
func randomUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
func sessionID(in, device string) string {
	in = strings.TrimSpace(in)
	if uuidPattern.MatchString(in) {
		return strings.ToLower(in)
	}
	if in != "" {
		return stableUUID("zcode-session\x00" + in)
	}
	return stableUUID("zcode-session-device\x00" + device)
}
func userID(device, sid string) string {
	b, _ := json.Marshal(map[string]string{"device_id": device, "account_uuid": "", "session_id": sid})
	return string(b)
}
func cachedText(s string) map[string]any {
	return map[string]any{"type": "text", "text": s, "cache_control": map[string]any{"type": "ephemeral"}}
}
func clientLanguage() string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		s := os.Getenv(k)
		if s != "" && s != "C" && s != "C.UTF-8" {
			return strings.ReplaceAll(strings.Split(s, ".")[0], "_", "-")
		}
	}
	return "unknown"
}
func clientTimezone() string {
	if s := os.Getenv("TZ"); s != "" {
		return s
	}
	if s := time.Now().Location().String(); s != "" && s != "Local" {
		return s
	}
	return "unknown"
}
func giftHeaders(jwt, sid, queryID string) (http.Header, error) {
	requestID, err := randomUUID()
	if err != nil {
		return nil, err
	}
	traceID, err := randomUUID()
	if err != nil {
		return nil, err
	}
	category := runtime.GOOS
	if category == "darwin" {
		category = "macos"
	} else if category == "windows" {
		category = "windows"
	} else {
		category = "linux"
	}
	h := http.Header{
		"Content-Type": {"application/json"}, "Authorization": {"Bearer " + jwt}, "Anthropic-Version": {"2023-06-01"}, "Http-Referer": {"https://zcode.z.ai"}, "User-Agent": {"ZCode/" + getConfig().AppVersion + " ai-sdk/anthropic/3.0.81"}, "X-Zcode-App-Version": {getConfig().AppVersion}, "X-Title": {"Z Code@cli"}, "X-Release-Channel": {"production"}, "X-Client-Language": {clientLanguage()}, "X-Client-Timezone": {clientTimezone()}, "X-Zcode-Agent": {"glm"}, "X-Platform": {zcodePlatform()}, "X-Os-Category": {category}, "X-Os-Version": {osRelease()}, "X-Request-Id": {requestID}, "X-Zcode-Session-Type": {"main"}, "X-Zcode-Trace-Id": {traceID}, "X-Zcode-Query-Source": {"main_turn"}, "X-Session-Id": {sid},
	}
	// createModelRequestAttributionHeaders: one id per user turn, shared by the
	// tool calls inside it.
	if queryID != "" {
		h.Set("X-Query-Id", queryID)
	}
	return h, nil
}
func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok && m != nil {
		return m
	}
	return map[string]any{}
}
func searchless(body map[string]any) bool {
	tools, ok := body["tools"].([]any)
	if !ok {
		return false
	}
	found, alias := false, false
	for _, v := range tools {
		t := asMap(v)
		if t["name"] == "ToolSearch" {
			found = true
		}
		if t["name"] == "ToolSearchTool" {
			alias = true
		}
	}
	if !found || alias {
		return false
	}
	var out []any
	for _, v := range tools {
		t, ok := v.(map[string]any)
		if !ok {
			out = append(out, v)
			continue
		}
		if t["name"] == "DeferredToolPlaceholder" {
			continue
		}
		delete(t, "defer_loading")
		if t["name"] == "ToolSearch" {
			t["name"] = "ToolSearchTool"
		}
		out = append(out, t)
	}
	body["tools"] = out
	if messages, ok := body["messages"].([]any); ok {
		for _, v := range messages {
			m := asMap(v)
			if m["role"] != "assistant" {
				continue
			}
			if blocks, ok := m["content"].([]any); ok {
				for _, x := range blocks {
					b := asMap(x)
					if b["type"] == "tool_use" && b["name"] == "ToolSearch" {
						b["name"] = "ToolSearchTool"
					}
				}
			}
		}
	}
	choice := asMap(body["tool_choice"])
	if choice["type"] == "tool" && choice["name"] == "ToolSearch" {
		choice["name"] = "ToolSearchTool"
	}
	return true
}
func restoreSearchName(raw []byte) []byte {
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		return raw
	}
	changed := false
	if blocks, ok := body["content"].([]any); ok {
		for _, v := range blocks {
			b := asMap(v)
			if b["type"] == "tool_use" && b["name"] == "ToolSearchTool" {
				b["name"] = "ToolSearch"
				changed = true
			}
		}
	}
	if !changed {
		return raw
	}
	out, err := json.Marshal(body)
	if err != nil {
		return raw
	}
	return out
}
func prepareBody(raw []byte, c credential, model, sid string, gift bool) ([]byte, bool, error) {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, false, errors.New("invalid messages body")
	}
	messages, ok := body["messages"].([]any)
	if !ok {
		return nil, false, errors.New("messages array required")
	}
	body["model"] = model
	// The host's openai→claude translator rewrites reasoning_effort into a
	// thinking.budget_tokens number (max is 128000) because the model is not in
	// the claude registry. ZCode reads output_config.effort instead, so undo the
	// budget here; a body that already carries output_config.effort is left as is.
	if _, ok := body["output_config"]; !ok {
		if thinking, ok := asMap(body["thinking"])["type"]; ok && thinking == "enabled" {
			if budget, ok := asMap(body["thinking"])["budget_tokens"].(float64); ok && budget > 0 {
				effort := "low"
				switch {
				case budget >= 65536:
					effort = "max"
				case budget > 24576:
					effort = "xhigh"
				case budget > 1024:
					effort = "high"
				}
				body["thinking"] = map[string]any{"type": "adaptive"}
				body["output_config"] = map[string]any{"effort": effort}
			}
		}
	}
	// The vendor 400s on the OpenAI nested tool shape ({"type":"function",
	// "function":{...}}) and takes only the flat Anthropic one. The host no
	// longer translates, so flatten here.
	if tools, ok := body["tools"].([]any); ok {
		flat := make([]any, 0, len(tools))
		for _, v := range tools {
			fn, nested := asMap(v)["function"].(map[string]any)
			if !nested {
				flat = append(flat, v)
				continue
			}
			t := map[string]any{"name": fn["name"], "description": fn["description"]}
			if schema, ok := fn["parameters"]; ok {
				t["input_schema"] = schema
			}
			flat = append(flat, t)
		}
		body["tools"] = flat
	}
	renamed := searchless(body)
	meta := asMap(body["metadata"])
	existing, _ := meta["user_id"].(string)
	if gift || strings.TrimSpace(existing) == "" {
		meta["user_id"] = userID(c.Device, sid)
	}
	body["metadata"] = meta
	if gift {
		var own []string
		switch s := body["system"].(type) {
		case string:
			if strings.TrimSpace(s) != "" {
				own = append(own, s)
			}
		case []any:
			for _, v := range s {
				m := asMap(v)
				text, _ := m["text"].(string)
				if m["type"] == "text" && strings.TrimSpace(text) != "" {
					own = append(own, text)
				}
			}
		}
		e := prompt.Environment
		// buildEnvInfoContent (core/src/context/sections/env-info.ts): heading,
		// the invoked line with its trailing space, then cwd, git, platform,
		// shell, os version and the untrusted-downloads note, each " - " prefixed.
		osname := runtime.GOOS
		if osname == "windows" {
			osname = "win32"
		}
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/bash"
		}
		cwd, _ := os.Getwd()
		environment := strings.Join([]string{
			e.Heading,
			e.InvokedLine,
			" - " + e.CwdLabel + ": " + cwd,
			" - " + e.GitLabel + ": " + e.GitNo,
			" - " + e.PlatformLabel + ": " + osname,
			" - " + e.ShellLabel + ": " + shell,
			" - " + e.OSVersionLabel + ": " + osRelease(),
			" - " + e.UntrustedNote,
		}, "\n")
		dynamic := append([]string{prompt.BeforeEnvironment, environment, prompt.AfterEnvironment}, own...)
		body["system"] = []any{cachedText(prompt.CliPrefix), cachedText(strings.Join(prompt.StableSections, "\n\n")), cachedText("\n\n" + strings.Join(dynamic, "\n\n"))}
		date := time.Now().Format("2006-01-02")
		context := prompt.Context
		reminder := strings.Join([]string{context.Intro, context.CurrentDateHeading + "\n" + strings.ReplaceAll(context.CurrentDateLine, "{date}", date), "", context.Outro}, "\n")
		messages = append([]any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "<system-reminder>" + reminder + "</system-reminder>"}}}}, messages...)
		var last map[string]any
		for _, v := range messages {
			m, ok := v.(map[string]any)
			if !ok || m["role"] == "system" {
				continue
			}
			last = m
			if blocks, ok := m["content"].([]any); ok {
				for _, v := range blocks {
					if b, ok := v.(map[string]any); ok {
						delete(b, "cache_control")
					}
				}
			}
		}
		if last != nil {
			switch content := last["content"].(type) {
			case string:
				last["content"] = []any{cachedText(content)}
			case []any:
				if len(content) > 0 {
					if b, ok := content[len(content)-1].(map[string]any); ok {
						b["cache_control"] = map[string]any{"type": "ephemeral"}
					}
				}
			}
		}
		if tools, ok := body["tools"].([]any); ok {
			for _, v := range tools {
				if t, ok := v.(map[string]any); ok {
					delete(t, "cache_control")
				}
			}
		}
		body["messages"] = messages
	}
	out, err := json.Marshal(body)
	return out, renamed, err
}

// queryID is the request's x-query-id, as the official client's
// createModelRequestAttributionHeaders sends it: one id per user turn, shared
// by the tool calls inside it. The vendor's own id is not in the request, so
// the plugin derives a stable one from the session and the number of user
// turns so far; a body that does not read gives none.
func queryID(raw []byte, sid string) string {
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		return ""
	}
	messages, ok := body["messages"].([]any)
	if !ok {
		return ""
	}
	n := 0
	for _, v := range messages {
		m, ok := v.(map[string]any)
		if !ok || m["role"] != "user" {
			continue
		}
		switch c := m["content"].(type) {
		case string:
			if strings.TrimSpace(c) != "" {
				n++
			}
		case []any:
			for _, x := range c {
				if b, ok := x.(map[string]any); ok && b["type"] != "tool_result" {
					n++
					break
				}
			}
		}
	}
	if n == 0 {
		return ""
	}
	return "query_" + stableUUID("zcode-query\x00"+sid+"\x00"+strconv.Itoa(n))
}
