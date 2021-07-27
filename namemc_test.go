package mcgo

import (
	"os"
	"testing"
)

func TestClaimNamemc(t *testing.T) {
	email := os.Getenv("MJ_EMAIL")
	pass := os.Getenv("MJ_PASS")
	acc := MCaccount{
		Email:    email,
		Password: pass,
	}
	_, err := acc.ClaimNamemc()
	if err != nil {
		t.Fatal(err)
	}
}