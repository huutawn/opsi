package actiondevice

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

func TestMemoryStoreListsPublicMetadataWithoutMutatingStoredKey(t *testing.T) {
	store := NewMemoryStore()
	service := Service{Store: store}
	publicKey, _, _ := ed25519.GenerateKey(rand.Reader)
	device, _, err := service.Register(context.Background(), Principal{ProjectID: "p1", UserID: "u1", Role: "owner"}, RegisterRequest{DisplayName: "laptop", PublicKey: publicKey, IdempotencyKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := service.List(context.Background(), Principal{ProjectID: "p1", UserID: "u1", Role: "viewer"})
	if err != nil || len(listed) != 1 {
		t.Fatalf("list = %#v %v", listed, err)
	}
	if len(listed[0].PublicKey) != 0 {
		t.Fatal("public list exposed the raw device key")
	}
	stored, err := store.Get(context.Background(), "p1", device.ID)
	if err != nil || string(stored.PublicKey) != string(publicKey) {
		t.Fatal("caller mutated stored public key")
	}
}
