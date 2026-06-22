package secret

import (
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
)

const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func GenerateCredentials() (SecretValue, error) {
	return GenerateCredentialsFrom(rand.Reader)
}

func GenerateCredentialsFrom(reader io.Reader) (SecretValue, error) {
	username, err := randomString(reader, 12)
	if err != nil {
		return SecretValue{}, fmt.Errorf("generate username: %w", err)
	}
	password, err := randomString(reader, 32)
	if err != nil {
		return SecretValue{}, fmt.Errorf("generate password: %w", err)
	}
	return SecretValue{Username: username, Password: password}, nil
}

func randomString(reader io.Reader, length int) (string, error) {
	out := make([]byte, length)
	max := big.NewInt(int64(len(alphabet)))
	for i := range out {
		n, err := rand.Int(reader, max)
		if err != nil {
			return "", err
		}
		out[i] = alphabet[n.Int64()]
	}
	return string(out), nil
}
