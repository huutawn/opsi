package connection

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

type templateToken struct {
	literal     string
	placeholder string
	encoder     string
}

func validateConnectionTemplate(template string) (bool, error) {
	if containsRawCredentialLiteral(template) {
		return false, errors.New("connection template must not contain literal credential values")
	}
	tokens, err := parseConnectionTemplate(template)
	if err != nil {
		return false, err
	}
	sensitive := false
	for _, token := range tokens {
		if token.placeholder != "username" && token.placeholder != "password" {
			continue
		}
		sensitive = true
		if token.encoder != "url_userinfo" && token.encoder != "url_query" && token.encoder != "kv_quote" {
			return false, fmt.Errorf("credential placeholder %s requires url_userinfo, url_query, or kv_quote", token.placeholder)
		}
	}
	return sensitive, nil
}

func containsRawCredentialLiteral(template string) bool {
	lower := strings.ToLower(template)
	for _, marker := range []string{"username=", "username:", "user=", "uid=", "password=", "password:", "passwd=", "pwd=", "secret=", "token="} {
		index := strings.Index(lower, marker)
		for index >= 0 {
			remainder := strings.TrimSpace(template[index+len(marker):])
			if !strings.HasPrefix(remainder, "{{") {
				return true
			}
			next := strings.Index(lower[index+len(marker):], marker)
			if next < 0 {
				break
			}
			index += len(marker) + next
		}
	}
	return containsRawURLUserinfo(template)
}

func containsRawURLUserinfo(template string) bool {
	for offset := 0; offset < len(template); {
		scheme := strings.Index(template[offset:], "://")
		if scheme < 0 {
			return false
		}
		start := offset + scheme + 3
		at := strings.Index(template[start:], "@")
		if at < 0 {
			return false
		}
		userinfo := template[start : start+at]
		var literal strings.Builder
		for len(userinfo) > 0 {
			placeholder := strings.Index(userinfo, "{{")
			if placeholder < 0 {
				literal.WriteString(userinfo)
				break
			}
			literal.WriteString(userinfo[:placeholder])
			end := strings.Index(userinfo[placeholder+2:], "}}")
			if end < 0 {
				break
			}
			userinfo = userinfo[placeholder+2+end+2:]
		}
		if strings.Trim(strings.TrimSpace(literal.String()), ":") != "" {
			return true
		}
		offset = start + at + 1
	}
	return false
}

func parseConnectionTemplate(template string) ([]templateToken, error) {
	if template == "" {
		return nil, errors.New("connection template is required")
	}
	if len(template) > MaxConnectionTemplateBytes {
		return nil, errors.New("connection template exceeds 1 KiB")
	}
	if strings.Contains(template, "${") || strings.Contains(template, "$(") || strings.Contains(template, "`") {
		return nil, errors.New("connection template contains forbidden substitution syntax")
	}
	allowed := map[string]bool{"host": true, "port": true, "database": true, "username": true, "password": true}
	encoders := map[string]bool{"": true, "url_userinfo": true, "url_query": true, "kv_quote": true}
	var tokens []templateToken
	for len(template) > 0 {
		start := strings.Index(template, "{{")
		if start < 0 {
			if strings.Contains(template, "}}") {
				return nil, errors.New("connection template contains unmatched braces")
			}
			tokens = append(tokens, templateToken{literal: template})
			break
		}
		if strings.Contains(template[:start], "}}") {
			return nil, errors.New("connection template contains unmatched braces")
		}
		if start > 0 {
			tokens = append(tokens, templateToken{literal: template[:start]})
		}
		end := strings.Index(template[start+2:], "}}")
		if end < 0 {
			return nil, errors.New("connection template contains unmatched braces")
		}
		body := strings.TrimSpace(template[start+2 : start+2+end])
		parts := strings.Split(body, "|")
		if len(parts) > 2 {
			return nil, errors.New("connection template placeholder is invalid")
		}
		name, encoder := strings.TrimSpace(parts[0]), ""
		if len(parts) == 2 {
			encoder = strings.TrimSpace(parts[1])
		}
		if !allowed[name] || !encoders[encoder] || (encoder != "" && name != "username" && name != "password") {
			return nil, fmt.Errorf("connection template placeholder %q is invalid", body)
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
			return "", fmt.Errorf("template fact %s is required", token.placeholder)
		}
		if (token.placeholder == "username" || token.placeholder == "password") && !facts.CredentialAvailable {
			return "", errors.New("template credential is unavailable")
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
