package transport

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"time"
)

type Identity struct {
	Certificate string `json:"certificate"`
	PrivateKey  string `json:"private_key"`
}

var (
	ErrControllerCertificateMissing  = errors.New("controller certificate missing")
	ErrControllerCertificateMismatch = errors.New("controller certificate does not match pairing identity")
)

func Token() string {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(data[:])
}

func NewIdentity() (Identity, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Identity{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return Identity{}, err
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "DevFreight Controller"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(10, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return Identity{}, err
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return Identity{}, err
	}
	return Identity{string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))}, nil
}

func (i Identity) TLS() (*tls.Config, error) {
	cert, err := tls.X509KeyPair([]byte(i.Certificate), []byte(i.PrivateKey))
	if err != nil {
		return nil, err
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}, nil
}

func (i Identity) Fingerprint() string {
	block, _ := pem.Decode([]byte(i.Certificate))
	if block == nil {
		return ""
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:])
}

func PinnedTLS(fingerprint string) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return ErrControllerCertificateMissing
		}
		sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
		if hex.EncodeToString(sum[:]) != fingerprint {
			return ErrControllerCertificateMismatch
		}
		return nil
	}}
}
