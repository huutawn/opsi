package connection

import (
	"net/url"
	"regexp"
	"strings"
)

type templateToken struct {
	literal     string
	placeholder string
	encoder     string
}

func validateConnectionTemplate(template string) (bool, error) {
	tokens, err := parseConnectionTemplate(template)
	if err != nil {
		return false, err
	}
	if err := validateCredentialPlacement(tokens); err != nil {
		return false, err
	}
	sensitive := false
	for _, token := range tokens {
		if token.placeholder != "username" && token.placeholder != "password" {
			continue
		}
		sensitive = true
		if token.encoder != "url_userinfo" && token.encoder != "url_query" && token.encoder != "kv_quote" {
			return false, compileError(ErrorInvalidTemplate, "credential placeholders require an approved encoder")
		}
	}
	return sensitive, nil
}

var (
	credentialAssignmentPattern = regexp.MustCompile(`(?i)(username|user|uid|password|passwd|pwd|secret|token)\s*[:=]\s*$`)
	credentialAssignmentAny     = regexp.MustCompile(`(?i)(username|user|uid|password|passwd|pwd|secret|token)\s*[:=]`)
)

func validateCredentialPlacement(tokens []templateToken) error {
	for index, token := range tokens {
		if token.placeholder != "" {
			continue
		}
		match := credentialAssignmentPattern.FindStringSubmatchIndex(token.literal)
		if match == nil {
			if containsCredentialAssignment(token.literal) {
				return compileError(ErrorInvalidTemplate, "connection template must not contain literal credential assignments")
			}
			continue
		}
		if containsCredentialAssignment(token.literal[:match[0]]) {
			return compileError(ErrorInvalidTemplate, "connection template must not contain literal credential assignments")
		}
		if index+1 >= len(tokens) || tokens[index+1].placeholder == "" {
			return compileError(ErrorInvalidTemplate, "credential assignments must use an exact credential placeholder")
		}
		expected := "username"
		assignmentName := strings.ToLower(token.literal[match[2]:match[3]])
		if assignmentName != "username" && assignmentName != "user" && assignmentName != "uid" {
			expected = "password"
		}
		next := tokens[index+1]
		if next.placeholder != expected || !credentialEncoder(next.encoder) || !credentialValueTerminates(tokens, index+2) {
			return compileError(ErrorInvalidTemplate, "credential assignments must use the matching encoded credential placeholder")
		}
	}
	return validateURLUserinfo(tokens)
}

func containsCredentialAssignment(literal string) bool {
	return credentialAssignmentAny.FindStringIndex(literal) != nil
}

func credentialValueTerminates(tokens []templateToken, next int) bool {
	if next >= len(tokens) {
		return true
	}
	if tokens[next].placeholder != "" {
		return false
	}
	literal := tokens[next].literal
	if literal == "" {
		return true
	}
	return strings.ContainsRune(";,&@\r\n", rune(literal[0]))
}

func validateURLUserinfo(tokens []templateToken) error {
	var rendered strings.Builder
	for _, token := range tokens {
		if token.placeholder == "" {
			rendered.WriteString(token.literal)
			continue
		}
		rendered.WriteString("\x00" + token.placeholder + "|" + token.encoder + "\x00")
	}
	text := rendered.String()
	for offset := 0; offset < len(text); {
		scheme := strings.Index(text[offset:], "://")
		if scheme < 0 {
			break
		}
		start := offset + scheme + 3
		at := strings.Index(text[start:], "@")
		if at < 0 {
			break
		}
		userinfo := text[start : start+at]
		username := "\x00username|url_userinfo\x00"
		password := "\x00password|url_userinfo\x00"
		if userinfo != username && userinfo != username+":"+password && userinfo != ":"+password {
			return compileError(ErrorInvalidTemplate, "URL userinfo must use exact encoded credential placeholders")
		}
		offset = start + at + 1
	}
	return nil
}

func credentialEncoder(value string) bool {
	return value == "url_userinfo" || value == "url_query" || value == "kv_quote"
}

func parseConnectionTemplate(template string) ([]templateToken, error) {
	if template == "" {
		return nil, compileError(ErrorInvalidTemplate, "connection template is required")
	}
	if len(template) > MaxConnectionTemplateBytes {
		return nil, compileError(ErrorInvalidTemplate, "connection template exceeds 1 KiB")
	}
	if strings.Contains(template, "${") || strings.Contains(template, "$(") || strings.Contains(template, "`") {
		return nil, compileError(ErrorInvalidTemplate, "connection template contains forbidden substitution syntax")
	}
	allowed := map[string]bool{"host": true, "port": true, "database": true, "username": true, "password": true}
	encoders := map[string]bool{"": true, "url_userinfo": true, "url_query": true, "kv_quote": true}
	var tokens []templateToken
	for len(template) > 0 {
		start := strings.Index(template, "{{")
		if start < 0 {
			if strings.Contains(template, "}}") {
				return nil, compileError(ErrorInvalidTemplate, "connection template contains unmatched braces")
			}
			tokens = append(tokens, templateToken{literal: template})
			break
		}
		if strings.Contains(template[:start], "}}") {
			return nil, compileError(ErrorInvalidTemplate, "connection template contains unmatched braces")
		}
		if start > 0 {
			tokens = append(tokens, templateToken{literal: template[:start]})
		}
		end := strings.Index(template[start+2:], "}}")
		if end < 0 {
			return nil, compileError(ErrorInvalidTemplate, "connection template contains unmatched braces")
		}
		body := strings.TrimSpace(template[start+2 : start+2+end])
		parts := strings.Split(body, "|")
		if len(parts) > 2 {
			return nil, compileError(ErrorInvalidTemplate, "connection template placeholder is invalid")
		}
		name, encoder := strings.TrimSpace(parts[0]), ""
		if len(parts) == 2 {
			encoder = strings.TrimSpace(parts[1])
		}
		if !allowed[name] || !encoders[encoder] || (encoder != "" && name != "username" && name != "password") {
			return nil, compileError(ErrorInvalidTemplate, "connection template placeholder is invalid")
		}
		tokens = append(tokens, templateToken{placeholder: name, encoder: encoder})
		template = template[start+2+end+2:]
	}
	return tokens, nil
}

func executeConnectionTemplate(template string, facts ConnectionFacts) (string, error) {
	tokens, err := parseConnectionTemplate(template)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, token := range tokens {
		if token.placeholder == "" {
			out.WriteString(token.literal)
			continue
		}
		value := map[string]string{"host": facts.Host, "port": facts.Port, "database": facts.Database, "username": facts.Username, "password": facts.Password}[token.placeholder]
		if value == "" && token.placeholder != "password" {
			return "", compileError(ErrorMissingFact, "required template fact is unavailable")
		}
		if (token.placeholder == "username" || token.placeholder == "password") && !facts.CredentialAvailable {
			return "", compileError(ErrorMissingCredential, "template credential is unavailable")
		}
		switch token.encoder {
		case "url_userinfo":
			value = url.User(value).String()
		case "url_query":
			value = url.QueryEscape(value)
		case "kv_quote":
			value = kvQuote(value)
		}
		out.WriteString(value)
	}
	return out.String(), nil
}
