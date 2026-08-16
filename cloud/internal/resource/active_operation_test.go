package resource

import (
	"context"
	"testing"

	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

type activeOperation bool

func (a activeOperation) HasActive(context.Context, string, string) (bool, error) {
	return bool(a), nil
}

func TestPostgresMutationBlockedDuringActiveBackup(t *testing.T) {
	service := testService()
	created, _, err := service.Create(context.Background(), "project-1", "user-1", "resource-key", managedRequest(resourcev1.TypePostgres))
	if err != nil {
		t.Fatal(err)
	}
	service.Operations = []ActiveOperationAuthority{activeOperation(true)}
	if _, err := service.Update(context.Background(), "project-1", created.ID, resourcev1.UpdateRequest{Managed: managedRequest(resourcev1.TypePostgres).Managed}); resourceErrorCode(err) != "RESOURCE_ACTIVE_OPERATION_CONFLICT" {
		t.Fatalf("update err=%v", err)
	}
	if _, err := service.DeleteIntent(context.Background(), "project-1", created.ID, "user-1"); resourceErrorCode(err) != "RESOURCE_ACTIVE_OPERATION_CONFLICT" {
		t.Fatalf("delete err=%v", err)
	}
}

func resourceErrorCode(err error) string {
	if typed, ok := err.(Error); ok {
		return typed.Code
	}
	return ""
}
