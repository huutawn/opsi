package cloudflare

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

const flexibleRuleRef = "opsi_public_hostname_flexible"
const redirectRuleRef = "opsi_public_hostname_https_redirect"

type ruleset struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Phase string `json:"phase"`
	Rules []rule `json:"rules"`
}
type rule struct {
	ID  string `json:"id"`
	Ref string `json:"ref"`
}

func (c *Client) ReconcileZoneRules(ctx context.Context) error {
	c.rulesMu.Lock()
	defer c.rulesMu.Unlock()
	if !c.flexibleOrigin {
		if err := c.deleteRule(ctx, "http_config_settings", flexibleRuleRef); err != nil {
			return err
		}
		return c.deleteRule(ctx, "http_request_dynamic_redirect", redirectRuleRef)
	}
	if err := c.reconcileRule(ctx, "http_config_settings", flexibleRuleRef, map[string]any{
		"action": "set_config", "action_parameters": map[string]any{"ssl": "flexible"}, "expression": fmt.Sprintf(`ends_with(http.host, %q)`, "."+c.domain), "description": "Opsi managed deployment hostnames use HTTP origins",
	}); err != nil {
		return err
	}
	return c.reconcileRule(ctx, "http_request_dynamic_redirect", redirectRuleRef, map[string]any{
		"action": "redirect", "action_parameters": map[string]any{"from_value": map[string]any{"target_url": map[string]any{"expression": `concat("https://", http.host, http.request.uri.path)`}, "status_code": 308, "preserve_query_string": true}}, "expression": fmt.Sprintf(`not ssl and ends_with(http.host, %q)`, "."+c.domain), "description": "Opsi managed deployment hostnames redirect HTTP to HTTPS",
	})
}

func (c *Client) deleteRule(ctx context.Context, phase, ref string) error {
	var sets []ruleset
	if err := c.do(ctx, http.MethodGet, "/zones/"+url.PathEscape(c.zoneID)+"/rulesets", nil, &sets); err != nil {
		return err
	}
	for _, selected := range sets {
		if selected.Kind != "zone" || selected.Phase != phase {
			continue
		}
		if err := c.do(ctx, http.MethodGet, "/zones/"+url.PathEscape(c.zoneID)+"/rulesets/"+url.PathEscape(selected.ID), nil, &selected); err != nil {
			return err
		}
		for _, existing := range selected.Rules {
			if existing.Ref == ref {
				return c.do(ctx, http.MethodDelete, "/zones/"+url.PathEscape(c.zoneID)+"/rulesets/"+url.PathEscape(selected.ID)+"/rules/"+url.PathEscape(existing.ID), nil, nil)
			}
		}
	}
	return nil
}

func (c *Client) reconcileRule(ctx context.Context, phase, ref string, desired map[string]any) error {
	desired["ref"] = ref
	var sets []ruleset
	if err := c.do(ctx, http.MethodGet, "/zones/"+url.PathEscape(c.zoneID)+"/rulesets", nil, &sets); err != nil {
		return err
	}
	var selected ruleset
	for _, candidate := range sets {
		if candidate.Kind == "zone" && candidate.Phase == phase {
			selected = candidate
			break
		}
	}
	if selected.ID == "" {
		body := map[string]any{"name": "Opsi " + phase, "kind": "zone", "phase": phase, "rules": []any{desired}}
		return c.do(ctx, http.MethodPost, "/zones/"+url.PathEscape(c.zoneID)+"/rulesets", body, &selected)
	}
	if err := c.do(ctx, http.MethodGet, "/zones/"+url.PathEscape(c.zoneID)+"/rulesets/"+url.PathEscape(selected.ID), nil, &selected); err != nil {
		return err
	}
	for _, existing := range selected.Rules {
		if existing.Ref != ref {
			continue
		}
		return c.do(ctx, http.MethodPatch, "/zones/"+url.PathEscape(c.zoneID)+"/rulesets/"+url.PathEscape(selected.ID)+"/rules/"+url.PathEscape(existing.ID), desired, nil)
	}
	return c.do(ctx, http.MethodPost, "/zones/"+url.PathEscape(c.zoneID)+"/rulesets/"+url.PathEscape(selected.ID)+"/rules", desired, nil)
}
