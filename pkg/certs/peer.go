package certs

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
)

type PeerCertificate x509.Certificate

func (pc *PeerCertificate) MarshalJSON() ([]byte, error) {
	b := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pc.Raw})
	return json.Marshal(b)
}

func (pc *PeerCertificate) UnmarshalJSON(b []byte) error {
	var buf []byte
	if err := json.Unmarshal(b, &buf); err != nil {
		return err
	}
	derBytes, _ := pem.Decode(buf)
	c, err := x509.ParseCertificate(derBytes.Bytes)
	if err != nil {
		return err
	}
	*pc = PeerCertificate(*c)
	return nil
}
