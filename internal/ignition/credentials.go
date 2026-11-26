// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package ignition

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"math/big"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh"
)

const passwordChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// Credentials holds the generated SSH keypair and/or password for a discovery boot.
type Credentials struct {
	User          string
	SSHPublicKey  []byte
	SSHPrivateKey []byte
	Password      []byte
	PasswordHash  []byte
}

// GenerateSSHKeypair generates an ephemeral ECDSA P-256 SSH keypair.
// SSHPublicKey is in authorized_keys format; SSHPrivateKey is OpenSSH PEM format.
func GenerateSSHKeypair() (publicKey, privateKey []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate ecdsa key: %w", err)
	}

	sshPub, err := ssh.NewPublicKey(&key.PublicKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create ssh public key: %w", err)
	}

	privBlock, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		return nil, nil, fmt.Errorf("marshal private key: %w", err)
	}

	return ssh.MarshalAuthorizedKey(sshPub), pem.EncodeToMemory(privBlock), nil
}

// GeneratePassword generates a random alphanumeric password of the given length
// and returns both the plaintext and its bcrypt hash.
func GeneratePassword(length int) (plain, bcryptHash []byte, err error) {
	buf := make([]byte, length)
	max := big.NewInt(int64(len(passwordChars)))
	for i := range buf {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return nil, nil, fmt.Errorf("generate password: %w", err)
		}
		buf[i] = passwordChars[n.Int64()]
	}
	hash, err := bcrypt.GenerateFromPassword(buf, bcrypt.DefaultCost)
	if err != nil {
		return nil, nil, fmt.Errorf("bcrypt password: %w", err)
	}
	return buf, hash, nil
}
