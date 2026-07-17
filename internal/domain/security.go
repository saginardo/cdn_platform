package domain

import (
	"errors"
	"regexp"
	"regexp/syntax"
	"strings"
	"time"
)

const (
	EdgeCapabilitySecurity = "edge_security_v1"

	DefaultSecurityPolicyID      = "00000000-0000-4000-8000-000000000001"
	DefaultSecurityPolicyPattern = `(?i)^/+(?:[^/]+/)*(?:\.env(?:[._~-][A-Za-z0-9][A-Za-z0-9._~-]*)?|\.git(?:config|-credentials)?(?:[._~-](?:old|bak|backup|save|txt|new|swp|orig|copy|disabled|zip|gz|tgz|tar|7z|rar|[0-9]+))?|\.(?:aws|azure|docker|svn|hg|ssh|kube|gnupg|terraform)|\.ht(?:access|passwd)(?:[._~-](?:old|bak|backup|save|txt|new|swp|orig|copy|disabled|zip|gz|tgz|tar|7z|rar|[0-9]+))?|\.DS_Store|\.(?:npmrc|pypirc|netrc)|\.(?:bash|zsh|mysql|psql|rediscli|python)_history|id_(?:rsa|dsa|ecdsa|ed25519)(?:[._~-](?:old|bak|backup|save|txt|new|swp|orig|copy|disabled|zip|gz|tgz|tar|7z|rar|[0-9]+))?|terraform\.tfstate(?:\.backup)?|wp-config\.php(?:[._~-](?:old|bak|backup|save|txt|new|swp|orig|copy|disabled|zip|gz|tgz|tar|7z|rar|[0-9]+))?)(?:/|$)`

	DefaultPHPSecurityPolicyID      = "00000000-0000-4000-8000-000000000002"
	DefaultPHPSecurityPolicyPattern = `(?i)^/+(?:[^/]+/)*(?:php[-_]?info|phpversion|phptest|pinfo|webshell|shell|cmd|c99|r57|wso|b374k|alfa|xleet|backdoor|leftdao|queryversion)\.(?:php(?:[0-9]+)?|phtml|phar)(?:[._~-](?:old|bak|backup|save|txt|new|swp|jpg|jpeg|png|gif|zip|gz|tgz|tar|7z|rar))?(?:/|$)`
)

type SecurityPolicyAction string

const (
	SecurityActionBlock SecurityPolicyAction = "block"
	SecurityActionBan   SecurityPolicyAction = "ban"
)

var SecurityBanDurations = []int{3600, 21600, 43200, 86400}

type SecurityPolicy struct {
	ID                 string               `json:"id"`
	Builtin            bool                 `json:"builtin"`
	Name               string               `json:"name"`
	Enabled            bool                 `json:"enabled"`
	Pattern            string               `json:"pattern"`
	Action             SecurityPolicyAction `json:"action"`
	BanDurationSeconds int                  `json:"ban_duration_seconds,omitempty"`
	Priority           int                  `json:"priority"`
	CreatedAt          time.Time            `json:"created_at"`
	UpdatedAt          time.Time            `json:"updated_at"`
}

type SecurityEvent struct {
	ID                 string               `json:"id,omitempty"`
	NodeID             string               `json:"node_id,omitempty"`
	PolicyID           string               `json:"policy_id"`
	PolicyName         string               `json:"policy_name,omitempty"`
	ClientIP           string               `json:"client_ip"`
	Host               string               `json:"host,omitempty"`
	Path               string               `json:"path"`
	Method             string               `json:"method,omitempty"`
	Action             SecurityPolicyAction `json:"action"`
	BanDurationSeconds int                  `json:"ban_duration_seconds,omitempty"`
	ObservedAt         time.Time            `json:"observed_at"`
	BanExpiresAt       *time.Time           `json:"ban_expires_at,omitempty"`
	CreatedAt          time.Time            `json:"created_at,omitempty"`
}

type SecurityBan struct {
	IP            string    `json:"ip"`
	PolicyID      string    `json:"policy_id,omitempty"`
	PolicyName    string    `json:"policy_name,omitempty"`
	TriggerNodeID string    `json:"trigger_node_id,omitempty"`
	Host          string    `json:"host,omitempty"`
	Path          string    `json:"path,omitempty"`
	Method        string    `json:"method,omitempty"`
	ExpiresAt     time.Time `json:"expires_at"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type EdgeSecurityEventBatch struct {
	Events []SecurityEvent `json:"events"`
}

type EdgeSecurityBanState struct {
	Bans        []EdgeSecurityBan `json:"bans"`
	GeneratedAt time.Time         `json:"generated_at"`
}

type EdgeSecurityBan struct {
	IP        string    `json:"ip"`
	ExpiresAt time.Time `json:"expires_at"`
}

func ValidSecurityBanDuration(seconds int) bool {
	for _, allowed := range SecurityBanDurations {
		if seconds == allowed {
			return true
		}
	}
	return false
}

func IsBuiltinSecurityPolicyID(id string) bool {
	switch id {
	case DefaultSecurityPolicyID, DefaultPHPSecurityPolicyID:
		return true
	default:
		return false
	}
}

func isBuiltinSecurityPolicyPattern(pattern string) bool {
	return pattern == DefaultSecurityPolicyPattern || pattern == DefaultPHPSecurityPolicyPattern
}

func NormalizeSecurityPolicy(policy SecurityPolicy) (SecurityPolicy, error) {
	policy.Name = strings.TrimSpace(policy.Name)
	policy.Pattern = strings.TrimSpace(policy.Pattern)
	if policy.Name == "" || len(policy.Name) > 80 {
		return SecurityPolicy{}, errors.New("security policy name must be 1-80 characters")
	}
	if policy.Pattern == "" || len(policy.Pattern) > 2048 || strings.ContainsAny(policy.Pattern, "\x00\r\n") {
		return SecurityPolicy{}, errors.New("security policy pattern must be a single line of at most 2048 characters")
	}
	if !validSecurityPatternDollars(policy.Pattern) {
		return SecurityPolicy{}, errors.New("security policy dollar signs may only be unescaped end anchors")
	}
	if _, err := CompileSecurityPattern(policy.Pattern); err != nil {
		return SecurityPolicy{}, errors.New("security policy pattern is not in the supported regular expression subset")
	}
	if !isBuiltinSecurityPolicyPattern(policy.Pattern) {
		parsed, err := syntax.Parse(strings.ReplaceAll(policy.Pattern, "(?:", "("), syntax.Perl)
		if err != nil || hasUnsafeSecurityRepetition(parsed) || securityBacktrackingChoices(parsed) > 16 {
			return SecurityPolicy{}, errors.New("security policy pattern exceeds the safe backtracking subset")
		}
	}
	if policy.Priority < 1 || policy.Priority > 10000 {
		return SecurityPolicy{}, errors.New("security policy priority must be between 1 and 10000")
	}
	switch policy.Action {
	case SecurityActionBlock:
		policy.BanDurationSeconds = 0
	case SecurityActionBan:
		if !ValidSecurityBanDuration(policy.BanDurationSeconds) {
			return SecurityPolicy{}, errors.New("security policy ban duration is not supported")
		}
	default:
		return SecurityPolicy{}, errors.New("security policy action is not supported")
	}
	return policy, nil
}

func validSecurityPatternDollars(pattern string) bool {
	for index := 0; index < len(pattern); index++ {
		if pattern[index] != '$' {
			continue
		}
		backslashes := 0
		for previous := index - 1; previous >= 0 && pattern[previous] == '\\'; previous-- {
			backslashes++
		}
		if backslashes%2 != 0 {
			return false
		}
		if index+1 < len(pattern) && pattern[index+1] != '|' && pattern[index+1] != ')' {
			return false
		}
	}
	return true
}

func securityBacktrackingChoices(expression *syntax.Regexp) int {
	if expression == nil {
		return 0
	}
	choices := 0
	switch expression.Op {
	case syntax.OpStar, syntax.OpPlus, syntax.OpQuest, syntax.OpRepeat:
		choices++
	case syntax.OpAlternate:
		choices += len(expression.Sub) - 1
	}
	for _, child := range expression.Sub {
		choices += securityBacktrackingChoices(child)
	}
	return choices
}

func CompileSecurityPattern(pattern string) (*regexp.Regexp, error) {
	// Nginx uses PCRE. Restrict user input to the RE2-compatible subset plus
	// non-capturing groups, which have identical matching semantics here.
	return regexp.Compile(strings.ReplaceAll(pattern, "(?:", "("))
}

func hasUnsafeSecurityRepetition(expression *syntax.Regexp) bool {
	if expression == nil {
		return false
	}
	if (expression.Op == syntax.OpStar || expression.Op == syntax.OpPlus || expression.Op == syntax.OpRepeat) &&
		!safeSecurityRepeatOperand(expression.Sub[0]) {
		return true
	}
	for _, child := range expression.Sub {
		if hasUnsafeSecurityRepetition(child) {
			return true
		}
	}
	return false
}

func safeSecurityRepeatOperand(expression *syntax.Regexp) bool {
	for expression.Op == syntax.OpCapture && len(expression.Sub) == 1 {
		expression = expression.Sub[0]
	}
	switch expression.Op {
	case syntax.OpLiteral, syntax.OpCharClass, syntax.OpAnyCharNotNL, syntax.OpAnyChar:
		return true
	default:
		return false
	}
}
