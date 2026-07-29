package adminbootstrap

import "testing"

func TestNormalizeAndValidateBootstrapOwner(t *testing.T) {
	valid := Request{Email: " Owner@Example.com ", OrgName: " Example Organization ", ProjectName: " Example Project ", OAuthProvider: " GitHub ", OAuthSubject: " 12345678 "}
	got, err := NormalizeAndValidate(valid, "github")
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != "owner@example.com" || got.OrgSlug != "example-organization" || got.ProjectSlug != "example-project" || got.OAuthProvider != "github" || got.OAuthSubject != "12345678" {
		t.Fatalf("unexpected normalization: %+v", got)
	}
}

func TestNormalizeAndValidateExistingOwnerOAuthLink(t *testing.T) {
	got, err := NormalizeAndValidate(Request{LinkExistingOwner: true, OAuthProvider: " GitHub ", OAuthSubject: " 143307746 "}, "github")
	if err != nil {
		t.Fatal(err)
	}
	if got.OAuthProvider != "github" || got.OAuthSubject != "143307746" {
		t.Fatalf("unexpected link normalization: %+v", got)
	}
	for _, req := range []Request{
		{LinkExistingOwner: true},
		{LinkExistingOwner: true, OAuthProvider: "github", OAuthSubject: "123", Email: "owner@example.test"},
		{LinkExistingOwner: true, OAuthProvider: "github", OAuthSubject: "123", IssuePAT: true},
	} {
		if _, err := NormalizeAndValidate(req, "github"); err == nil {
			t.Fatalf("invalid existing-owner link accepted: %+v", req)
		}
	}
}

func TestNormalizeAndValidateBootstrapOwnerRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		req  Request
	}{
		{name: "missing email", req: Request{OrgName: "Org", ProjectName: "Project", IssuePAT: true}},
		{name: "invalid email", req: Request{Email: "not-an-email", OrgName: "Org", ProjectName: "Project", IssuePAT: true}},
		{name: "display email", req: Request{Email: "Owner <owner@example.com>", OrgName: "Org", ProjectName: "Project", IssuePAT: true}},
		{name: "missing org", req: Request{Email: "owner@example.com", ProjectName: "Project", IssuePAT: true}},
		{name: "missing project", req: Request{Email: "owner@example.com", OrgName: "Org", IssuePAT: true}},
		{name: "provider only", req: Request{Email: "owner@example.com", OrgName: "Org", ProjectName: "Project", OAuthProvider: "github"}},
		{name: "subject only", req: Request{Email: "owner@example.com", OrgName: "Org", ProjectName: "Project", OAuthSubject: "123"}},
		{name: "no linkage", req: Request{Email: "owner@example.com", OrgName: "Org", ProjectName: "Project"}},
		{name: "invalid org slug", req: Request{Email: "owner@example.com", OrgName: "Org", OrgSlug: "Bad_Slug", ProjectName: "Project", IssuePAT: true}},
		{name: "invalid project slug", req: Request{Email: "owner@example.com", OrgName: "Org", ProjectName: "Project", ProjectSlug: "-bad", IssuePAT: true}},
		{name: "control name", req: Request{Email: "owner@example.com", OrgName: "Org\nInjected", ProjectName: "Project", IssuePAT: true}},
		{name: "trailing newline name", req: Request{Email: "owner@example.com", OrgName: "Org\n", ProjectName: "Project", IssuePAT: true}},
		{name: "control subject", req: Request{Email: "owner@example.com", OrgName: "Org", ProjectName: "Project", OAuthProvider: "github", OAuthSubject: "subject\nother"}},
		{name: "unsupported provider", req: Request{Email: "owner@example.com", OrgName: "Org", ProjectName: "Project", OAuthProvider: "gitlab", OAuthSubject: "123"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NormalizeAndValidate(tt.req, "github"); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestNormalizeAndValidateRequiresPostgresAtServiceBoundary(t *testing.T) {
	req, err := NormalizeAndValidate(Request{Email: "owner@example.com", OrgName: "Org", ProjectName: "Project", IssuePAT: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (Service{}).ProvisionBootstrapOwner(t.Context(), req); ErrorCode(err) != CodeRequiresPostgres {
		t.Fatalf("expected %s, got %v", CodeRequiresPostgres, err)
	}
}
