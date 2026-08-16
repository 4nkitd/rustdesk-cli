package hbb

import (
	"bytes"
	"testing"
)

func TestGenerateKeyPair(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}
	if len(kp.Pub) != 32 || len(kp.Priv) != 32 {
		t.Fatalf("expected 32-byte keys, got pub=%d priv=%d", len(kp.Pub), len(kp.Priv))
	}
}

func TestDeriveSharedSecret(t *testing.T) {
	alice, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("alice keypair failed: %v", err)
	}
	bob, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("bob keypair failed: %v", err)
	}

	secretAlice, err := DeriveSharedSecret(alice.Priv, bob.Pub)
	if err != nil {
		t.Fatalf("alice derive failed: %v", err)
	}
	secretBob, err := DeriveSharedSecret(bob.Priv, alice.Pub)
	if err != nil {
		t.Fatalf("bob derive failed: %v", err)
	}

	if secretAlice != secretBob {
		t.Fatalf("shared secrets do not match")
	}

	secretAlice2, _ := DeriveSharedSecret(alice.Priv, bob.Pub)
	if secretAlice != secretAlice2 {
		t.Fatalf("derive shared secret is not deterministic")
	}

	_, err = DeriveSharedSecret(alice.Priv[:31], bob.Pub)
	if err == nil {
		t.Errorf("expected error for short private key")
	}
}

func TestEncryptDecryptRoundtrip(t *testing.T) {
	aliceKP, _ := GenerateKeyPair()
	bobKP, _ := GenerateKeyPair()
	sharedKey, _ := DeriveSharedSecret(aliceKP.Priv, bobKP.Pub)

	sender := NewEncryptor(sharedKey)
	receiver := NewEncryptor(sharedKey)

	msg1 := []byte("hello rustdesk")
	msg2 := []byte("another secret message")

	ct1 := sender.Encrypt(msg1)
	ct2 := sender.Encrypt(msg2)

	pt1, err := receiver.Decrypt(ct1)
	if err != nil {
		t.Fatalf("decrypt msg1 failed: %v", err)
	}
	if !bytes.Equal(pt1, msg1) {
		t.Fatalf("got %s, want %s", pt1, msg1)
	}

	pt2, err := receiver.Decrypt(ct2)
	if err != nil {
		t.Fatalf("decrypt msg2 failed: %v", err)
	}
	if !bytes.Equal(pt2, msg2) {
		t.Fatalf("got %s, want %s", pt2, msg2)
	}
}

func TestDecryptWrongCiphertext(t *testing.T) {
	kp, _ := GenerateKeyPair()
	secret, _ := DeriveSharedSecret(kp.Priv, kp.Pub)
	enc := NewEncryptor(secret)

	ct := enc.Encrypt([]byte("secret"))
	ct[len(ct)-1] ^= 0xff

	dec := NewEncryptor(secret)
	_, err := dec.Decrypt(ct)
	if err == nil {
		t.Errorf("expected error on tampered ciphertext")
	}
}

func TestMakeNonce(t *testing.T) {
	nonce := MakeNonce(0x0102030405060708)
	expectedPrefix := []byte{0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01}
	if !bytes.Equal(nonce[:8], expectedPrefix) {
		t.Errorf("got %v, want prefix %v", nonce[:8], expectedPrefix)
	}
	for i := 8; i < 24; i++ {
		if nonce[i] != 0 {
			t.Errorf("byte %d of nonce should be 0, got %d", i, nonce[i])
		}
	}
}
