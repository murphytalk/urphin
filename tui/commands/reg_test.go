package commands

import "testing"

func TestIsRegistered(t *testing.T) {
	if !IsRegistered("TX") || !IsRegistered("WATCH") {
		t.Fatal("public commands should be registered")
	}
	if IsRegistered("MKT") {
		t.Fatal("grammar-only command should not appear as registered")
	}
}
