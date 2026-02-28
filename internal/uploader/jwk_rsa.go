package uploader

import (
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"math/big"
)

// parseRSAPublicKeyFromJWK reconstructs an *rsa.PublicKey from the base64url-encoded
// modulus (n) and exponent (e) fields of a JWK.
func parseRSAPublicKeyFromJWK(nB64, eB64 string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nB64)
	if err != nil {
		return nil, fmt.Errorf("decode modulus: %w", err)
	}

	eBytes, err := base64.RawURLEncoding.DecodeString(eB64)
	if err != nil {
		return nil, fmt.Errorf("decode exponent: %w", err)
	}

	n := new(big.Int).SetBytes(nBytes)

	// Exponent is big-endian bytes; convert to int
	var e int
	for _, b := range eBytes {
		e = e<<8 | int(b)
	}
	if e == 0 {
		return nil, fmt.Errorf("invalid exponent: 0")
	}

	return &rsa.PublicKey{N: n, E: e}, nil
}
