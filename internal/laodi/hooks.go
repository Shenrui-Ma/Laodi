package laodi

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"path"
	"reflect"
	"regexp"
	"strings"
	"unicode"
)

const HookParserID = "tool-hooks-v1"
const MaxHookInputBytes = 1 << 20

const maxHookDepth = 32
const maxHookNodes = 16384
const maxHookRuleCount = 32

// HookSignal is a whitelist record. It never contains matched text or paths.
type HookSignal struct {
	Kind     string         `json:"kind"`
	Counts   map[string]int `json:"counts,omitempty"`
	Unknowns []string       `json:"unknowns"`
}

// The IDs are transient correlation inputs, not display or storage fields.
// Even accidental JSON serialization cannot expose them.
type HookInspection struct {
	Adapter   string       `json:"adapter"`
	EventName string       `json:"event_name,omitempty"`
	SessionID string       `json:"-"`
	ToolUseID string       `json:"-"`
	Signals   []HookSignal `json:"signals,omitempty"`
	Unknowns  []string     `json:"unknowns,omitempty"`
}

var (
	errHookDepth = errors.New("input_depth_exceeded")
	errHookNodes = errors.New("input_nodes_exceeded")
	errHookJSON  = errors.New("input_invalid")
)

// InspectHook examines only the hook payload supplied by the caller. It never
// opens a path, follows transcript/output files, executes a command, or contacts
// a model. This observer returns no hook decision and cannot block a tool.
func InspectHook(adapter string, input io.Reader) HookInspection {
	r := HookInspection{Adapter: adapter}
	if adapter != "zcode" && adapter != "claude-code" {
		r.Adapter = "unknown"
		return hookDegraded(r, "adapter_unsupported")
	}
	if input == nil {
		return hookDegraded(r, "input_invalid")
	}
	data, err := io.ReadAll(io.LimitReader(input, MaxHookInputBytes+1))
	if err != nil {
		return hookDegraded(r, "input_read_failed")
	}
	if len(data) > MaxHookInputBytes {
		return hookDegraded(r, "input_too_large")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	nodes := 0
	value, err := readHookJSON(decoder, 0, &nodes)
	if err != nil {
		switch err {
		case errHookDepth:
			return hookDegraded(r, "input_depth_exceeded")
		case errHookNodes:
			return hookDegraded(r, "input_nodes_exceeded")
		default:
			return hookDegraded(r, "input_invalid")
		}
	}
	if _, err = decoder.Token(); err != io.EOF {
		return hookDegraded(r, "input_invalid")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return hookDegraded(r, "schema_unsupported")
	}
	field := func(snake, camel string) (any, bool) {
		v, exists := object[snake]
		if adapter == "zcode" {
			alias, found := object[camel]
			if exists && found && !reflect.DeepEqual(v, alias) {
				return nil, false
			}
			if !exists {
				return alias, found
			}
		}
		return v, exists
	}
	event, valid := field("hook_event_name", "hookEventName")
	if !valid {
		return hookDegraded(r, "schema_unsupported")
	}
	eventName, ok := event.(string)
	if !ok || eventName == "" {
		return hookDegraded(r, "schema_unsupported")
	}
	switch eventName {
	case "PreToolUse", "PostToolUse", "PostToolUseFailure":
		r.EventName = eventName
	case "SessionStart", "SessionEnd", "UserPromptSubmit", "PermissionRequest", "Stop", "SubagentStart", "SubagentStop", "Notification", "PreCompact", "PostCompact", "PostToolBatch":
		r.Unknowns = []string{"event_not_observed"}
		return r
	default:
		return hookDegraded(r, "schema_unsupported")
	}
	for _, id := range []struct {
		snake, camel string
		target       *string
	}{
		{"session_id", "sessionId", &r.SessionID},
		{"tool_use_id", "toolUseId", &r.ToolUseID},
	} {
		if v, found := field(id.snake, id.camel); found {
			s, ok := v.(string)
			if !ok || len(s) > 1024 {
				return hookDegraded(r, "schema_unsupported")
			}
			*id.target = s
		}
	}
	if r.SessionID == "" || r.ToolUseID == "" {
		r.Unknowns = append(r.Unknowns, "correlation_unavailable")
	}
	tool, valid := field("tool_name", "toolName")
	toolName, ok := tool.(string)
	if !valid || !ok || toolName == "" {
		return hookDegraded(r, "schema_unsupported")
	}
	// These are actual read/command tools, not names supplied by MCP servers.
	// Unsupported tools do not generate per-call incident storms.
	if toolName != "Bash" && toolName != "Read" && !(adapter == "zcode" && toolName == "read_file") {
		r.Unknowns = append(r.Unknowns, "tool_not_observed")
		return r
	}
	if eventName == "PreToolUse" {
		v, found := field("tool_input", "toolInput")
		args, ok := v.(map[string]any)
		if !found || !ok {
			return hookDegraded(r, "schema_unsupported")
		}
		counts := map[string]int{}
		if toolName == "Bash" {
			command, ok := args["command"].(string)
			if !ok {
				return hookDegraded(r, "schema_unsupported")
			}
			paths, parsed := hookReadCommandPaths(command)
			if !parsed {
				r.Unknowns = append(r.Unknowns, "shell_command_not_parsed")
			}
			for _, name := range paths {
				addHookCount(counts, sensitiveHookPath(name))
			}
		} else {
			name, ok := args["file_path"].(string)
			if !ok && adapter == "zcode" {
				name, ok = args["filePath"].(string)
			}
			if !ok {
				return hookDegraded(r, "schema_unsupported")
			}
			addHookCount(counts, sensitiveHookPath(name))
		}
		if len(counts) != 0 {
			r.Signals = append(r.Signals, HookSignal{Kind: "sensitive_tool_access_requested", Counts: counts,
				Unknowns: []string{"read_not_confirmed", "upload_not_observed"}})
		}
		return r
	}

	var texts []string
	if eventName == "PostToolUseFailure" {
		// Error text can itself contain a leaked credential. It is not evidence
		// of a successful tool execution or successful transmission.
		message, ok := object["error"].(string)
		if !ok || message == "" {
			r.Unknowns = append(r.Unknowns, "failed_tool_no_response")
			return r
		}
		texts = []string{message}
	} else {
		response, found := field("tool_response", "toolResponse")
		if !found || response == nil {
			return hookDegraded(r, "output_missing")
		}
		var observed, partial bool
		texts, observed, partial = hookResponseTexts(response, toolName)
		if !observed {
			return hookDegraded(r, "output_shape_unsupported")
		}
		if partial {
			r.Unknowns = append(r.Unknowns, "response_fields_not_observed")
			r = hookDegraded(r, "output_partial")
		}
		if body, ok := response.(map[string]any); ok {
			truncated, _ := body["truncated"].(bool)
			isTruncated, _ := body["isTruncated"].(bool)
			if truncated || isTruncated {
				r = hookDegraded(r, "output_truncated")
			}
		}
	}
	counts := map[string]int{}
	for _, text := range texts {
		detectHookSecrets(text, counts)
	}
	if len(counts) != 0 {
		unknowns := []string{"credential_validity_unknown", "model_delivery_not_observed", "upload_not_observed"}
		if eventName == "PostToolUseFailure" {
			unknowns = append(unknowns, "tool_failed")
		}
		for _, signal := range r.Signals {
			if signal.Kind == "hook_coverage_degraded" {
				unknowns = append(unknowns, "inspection_incomplete")
				break
			}
		}
		r.Signals = append(r.Signals, HookSignal{Kind: "sensitive_tool_output_detected", Counts: counts, Unknowns: unknowns})
	}
	return r
}

func hookDegraded(r HookInspection, reason string) HookInspection {
	for i := range r.Signals {
		if r.Signals[i].Kind == "hook_coverage_degraded" {
			if r.Signals[i].Counts == nil {
				r.Signals[i].Counts = map[string]int{}
			}
			r.Signals[i].Counts[reason] = 1
			return r
		}
	}
	r.Signals = append(r.Signals, HookSignal{Kind: "hook_coverage_degraded", Counts: map[string]int{reason: 1},
		Unknowns: []string{"inspection_incomplete", "upload_not_observed"}})
	return r
}

// A bounded decoder also rejects duplicate keys rather than silently choosing
// whichever potentially conflicting event or output field appears last.
func readHookJSON(d *json.Decoder, depth int, nodes *int) (any, error) {
	if depth > maxHookDepth {
		return nil, errHookDepth
	}
	*nodes++
	if *nodes > maxHookNodes {
		return nil, errHookNodes
	}
	token, err := d.Token()
	if err != nil {
		return nil, errHookJSON
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return token, nil
	}
	switch delim {
	case '{':
		object := map[string]any{}
		for d.More() {
			keyToken, err := d.Token()
			if err != nil {
				return nil, errHookJSON
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errHookJSON
			}
			if _, duplicate := object[key]; duplicate {
				return nil, errHookJSON
			}
			object[key], err = readHookJSON(d, depth+1, nodes)
			if err != nil {
				return nil, err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, errHookJSON
		}
		return object, nil
	case '[':
		var array []any
		for d.More() {
			item, err := readHookJSON(d, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			array = append(array, item)
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return nil, errHookJSON
		}
		return array, nil
	default:
		return nil, errHookJSON
	}
}

// Only known body locations are read. In particular, filePath, metadata,
// outputFile, transcript_path, command and other arbitrary strings are ignored.
func hookResponseTexts(value any, tool string) (texts []string, observed, partial bool) {
	var content func(any)
	content = func(value any) {
		switch v := value.(type) {
		case string:
			texts = append(texts, v)
			observed = true
		case []any:
			if len(v) == 0 {
				observed = true
			}
			for _, block := range v {
				object, ok := block.(map[string]any)
				if !ok || object["type"] != "text" {
					partial = true
					continue
				}
				text, ok := object["text"].(string)
				if !ok {
					partial = true
					continue
				}
				texts = append(texts, text)
				observed = true
			}
		default:
			partial = true
		}
	}
	switch v := value.(type) {
	case string, []any:
		content(v)
	case map[string]any:
		for _, key := range []string{"stdout", "stderr", "output", "content"} {
			if body, exists := v[key]; exists {
				content(body)
			}
		}
		if tool == "Read" || tool == "read_file" {
			if file, ok := v["file"].(map[string]any); ok {
				if body, exists := file["content"]; exists {
					content(body)
				}
			}
		}
	default:
		partial = true
	}
	return
}

func addHookCount(counts map[string]int, key string) {
	if key != "" && counts[key] < maxHookRuleCount {
		counts[key]++
	}
}

func sensitiveHookPath(name string) string {
	// Lexical only: no expansion, symlink resolution, or disk access.
	name = strings.ReplaceAll(name, "\\", "/")
	name = "/" + strings.TrimLeft(strings.ToLower(path.Clean(name)), "/")
	base := path.Base(name)
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		if strings.HasSuffix(base, ".example") || strings.HasSuffix(base, ".sample") || strings.HasSuffix(base, ".template") {
			return ""
		}
		return "env_file"
	}
	if strings.Contains(name, "/.ssh/") && (base == "id_rsa" || base == "id_dsa" || base == "id_ecdsa" || base == "id_ed25519" || base == "identity") {
		return "private_key_file"
	}
	if strings.HasSuffix(name, "/.aws/credentials") || strings.HasSuffix(name, "/.docker/config.json") || strings.HasSuffix(name, "/.kube/config") ||
		strings.HasSuffix(name, "/gcloud/application_default_credentials.json") || base == ".npmrc" || base == ".pypirc" || base == ".netrc" || base == ".git-credentials" {
		return "credential_file"
	}
	return ""
}

// This deliberately accepts a small direct-command grammar, not arbitrary
// shell. A complex/indirect command is unclassified, never claimed safe.
func hookReadCommandPaths(command string) ([]string, bool) {
	words, ok := hookSimpleShellWords(command)
	if !ok || len(words) == 0 {
		return nil, false
	}
	program := path.Base(words[0])
	if program != "cat" && program != "head" && program != "tail" && program != "sed" {
		return nil, false
	}
	var files []string
	options := true
	sedScript := false
	for i := 1; i < len(words); i++ {
		word := words[i]
		if options && word == "--" {
			options = false
			continue
		}
		if options && strings.HasPrefix(word, "-") {
			if (program == "head" || program == "tail") && (word == "-n" || word == "-c") {
				i++
				if i >= len(words) || !hookNumericArgument(words[i]) {
					return nil, false
				}
				continue
			}
			if program == "sed" {
				if word == "-n" {
					continue
				}
				if word == "-e" && !sedScript {
					i++
					if i >= len(words) || !hookSedPrint.MatchString(words[i]) {
						return nil, false
					}
					sedScript = true
					continue
				}
				return nil, false
			}
			if program == "cat" && strings.Trim(word, "-AbEenstTuv") == "" {
				continue
			}
			if (program == "head" || program == "tail") && (hookNumericArgument(strings.TrimPrefix(word, "-")) || word == "-q" || word == "-v") {
				continue
			}
			return nil, false
		}
		if program == "sed" && !sedScript {
			if !hookSedPrint.MatchString(word) {
				return nil, false
			}
			sedScript = true
			continue
		}
		if word != "-" {
			files = append(files, word)
		}
	}
	return files, true
}

var hookSedPrint = regexp.MustCompile(`^(?:[0-9]+(?:,[0-9]+)?)?p$`)

func hookNumericArgument(s string) bool {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "+"), "-")
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func hookSimpleShellWords(s string) ([]string, bool) {
	var words []string
	var word strings.Builder
	var quote rune
	started := false
	for _, r := range s {
		// Reject expansions/escapes even inside quotes rather than approximate
		// their semantics. Tilde paths remain useful lexical path indicators.
		if strings.ContainsRune("$`\\\n\r", r) {
			return nil, false
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			started = true
			continue
		}
		if strings.ContainsRune(";|&<>(){}*?[]#", r) {
			return nil, false
		}
		if unicode.IsSpace(r) {
			if started {
				words = append(words, word.String())
				word.Reset()
				started = false
			}
			continue
		}
		word.WriteRune(r)
		started = true
	}
	if quote != 0 {
		return nil, false
	}
	if started {
		words = append(words, word.String())
	}
	return words, true
}

var (
	hookPrivateKey     = regexp.MustCompile(`(?s)-----BEGIN (?:RSA |EC |DSA |OPENSSH |ENCRYPTED )?PRIVATE KEY-----[A-Za-z0-9+/=\r\n\t ]{32,}-----END (?:RSA |EC |DSA |OPENSSH |ENCRYPTED )?PRIVATE KEY-----`)
	hookProviderToken  = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,255}|github_pat_[A-Za-z0-9_]{40,255}|sk-(?:proj-|ant-api03-)?[A-Za-z0-9_-]{24,255}|xox[baprs]-[A-Za-z0-9-]{20,255}|AIza[0-9A-Za-z_-]{35})\b`)
	hookAssignment     = regexp.MustCompile(`(?m)(?:^|[\r\n{,])[\t ]*(?:export[\t ]+)?["']?([A-Za-z][A-Za-z0-9_]{0,80})["']?[\t ]*[:=][\t ]*["']?([A-Za-z0-9_/+.=-]{12,256})`)
	hookUUID           = regexp.MustCompile(`(?i)^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
	hookReadLinePrefix = regexp.MustCompile(`(?m)^[\t ]*[0-9]+(?:→|\t)[\t ]?`)
)

func detectHookSecrets(text string, counts map[string]int) {
	// Serialized Read output may add line numbers. This removes only that
	// known presentation prefix; it does not decode arbitrary encodings.
	text = hookReadLinePrefix.ReplaceAllString(text, "")
	for range hookPrivateKey.FindAllStringIndex(text, maxHookRuleCount) {
		addHookCount(counts, "private_key_block")
	}
	// Candidate scans cover the whole bounded payload. Limiting candidates
	// before placeholder/name filtering would let benign prefixes hide secrets.
	for _, token := range hookProviderToken.FindAllString(text, -1) {
		if !hookPlaceholder(token) && hookVariety(hookTokenBody(token)) >= 8 && hookEntropy(hookTokenBody(token)) >= 3.2 {
			addHookCount(counts, "provider_token")
		}
	}
	for _, assignment := range hookAssignment.FindAllStringSubmatch(text, -1) {
		key, value := strings.ToUpper(assignment[1]), assignment[2]
		if !hookCredentialName(key) || hookPlaceholder(value) || hookUUID.MatchString(value) {
			continue
		}
		if len(value) >= 16 && hookVariety(value) >= 8 && hookEntropy(value) >= 3.2 {
			addHookCount(counts, "credential_assignment")
		}
	}
}

func hookCredentialName(key string) bool {
	for _, suffix := range []string{"API_KEY", "APIKEY", "ACCESS_TOKEN", "AUTH_TOKEN", "TOKEN", "SECRET", "SECRET_KEY", "SECRET_ACCESS_KEY", "PASSWORD", "PASSWD", "CLIENT_SECRET"} {
		if key == suffix || strings.HasSuffix(key, "_"+suffix) {
			return true
		}
	}
	return false
}

func hookPlaceholder(value string) bool {
	value = strings.ToLower(value)
	for _, marker := range []string{"redacted", "changeme", "change_me", "your_", "your-", "replace", "placeholder", "example", "dummy", "sample", "<", ">", "${", "***"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func hookVariety(value string) int {
	seen := map[byte]bool{}
	for i := range value {
		seen[value[i]] = true
	}
	return len(seen)
}

func hookEntropy(value string) float64 {
	var frequencies [256]int
	for i := range value {
		frequencies[value[i]]++
	}
	var entropy float64
	for _, count := range frequencies {
		if count != 0 {
			p := float64(count) / float64(len(value))
			entropy -= p * math.Log2(p)
		}
	}
	return entropy
}

func hookTokenBody(token string) string {
	for _, prefix := range []string{"sk-ant-api03-", "github_pat_", "sk-proj-", "ghp_", "gho_", "ghu_", "ghs_", "ghr_", "xoxb-", "xoxa-", "xoxp-", "xoxr-", "xoxs-", "AIza", "sk-"} {
		if strings.HasPrefix(token, prefix) {
			return strings.TrimPrefix(token, prefix)
		}
	}
	return token
}
