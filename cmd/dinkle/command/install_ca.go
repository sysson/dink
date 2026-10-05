package command

import (
	"errors"
	"time"

	"github.com/sysson/syskit/pki"
)

func validateInstallationCA(pair pki.KeyPair) error {
	ca, err := pki.LoadAuthority(pair)
	if err != nil {
		return err
	}
	cert := ca.Certificate()
	now := time.Now()
	if !cert.IsCA || now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) {
		return errors.New("CA certificate is not a currently valid authority")
	}
	return nil
}
