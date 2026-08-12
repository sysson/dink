package k8s

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	defaultCASecret        = "dink-ca"
	defaultTLSServerSecret = "dink-server-tls"

	secretKeyCert  = corev1.TLSCertKey
	secretKeyKey   = corev1.TLSPrivateKeyKey
	secretKeyCAKey = "ca.key"

	caCertValidity     = 10 * 365 * 24 * time.Hour
	serverCertValidity = 397 * 24 * time.Hour
	clientCertValidity = 90 * 24 * time.Hour

	caCommonName = "dink CA"
)

type caBundle struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
}

func (kc *KubeClient) LoadOrCreateServerTLS(ctx context.Context) (*tls.Certificate, error) {
	ca, err := kc.loadOrCreateCA(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading CA: %w", err)
	}

	secret, err := kc.client.CoreV1().Secrets(kc.SystemNamespace()).Get(ctx, defaultTLSServerSecret, metav1.GetOptions{})
	if err != nil {
		if !k8serrors.IsNotFound(err) {
			return nil, fmt.Errorf("getting secret %q in namespace %q: %w", defaultTLSServerSecret, kc.SystemNamespace(), err)
		}
		return kc.createServerTLS(ctx, ca)
	}

	certPEM := secret.Data[secretKeyCert]
	keyPEM := secret.Data[secretKeyKey]

	serverCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parsing server certificate/key in secret %q: %w", defaultTLSServerSecret, err)
	}

	leaf, err := x509.ParseCertificate(serverCert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parsing server certificate in secret %q: %w", defaultTLSServerSecret, err)
	}

	if err := validateCertificate(leaf, ca.cert); err != nil {
		return kc.createServerTLS(ctx, ca)
	}

	serverCert.Leaf = leaf
	return &serverCert, nil
}

func (kc *KubeClient) createServerTLS(ctx context.Context, ca *caBundle) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating server private key: %w", err)
	}

	serial, err := newSerialNumber()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: "dink",
		},
		NotBefore:   now.Add(-5 * time.Minute),
		NotAfter:    now.Add(serverCertValidity),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    serverDNSNames(kc.serviceAccountName, kc.SystemNamespace()),
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, fmt.Errorf("creating server certificate: %w", err)
	}

	certPEM := encodePEM("CERTIFICATE", certDER)
	keyPEM, err := encodeECPrivateKeyPEM(key)
	if err != nil {
		return nil, fmt.Errorf("encoding server private key: %w", err)
	}

	if err := kc.putSecret(ctx, defaultTLSServerSecret, corev1.SecretTypeTLS, map[string][]byte{
		secretKeyCert: certPEM,
		secretKeyKey:  keyPEM,
	}); err != nil {
		return nil, fmt.Errorf("storing secret %q: %w", defaultTLSServerSecret, err)
	}

	serverCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parsing generated server certificate: %w", err)
	}
	return &serverCert, nil
}

func (kc *KubeClient) GenerateClientCert(ctx context.Context, namespace string) (certPEM, keyPEM []byte, err error) {
	if namespace == "" {
		return nil, nil, fmt.Errorf("namespace must not be empty")
	}

	ca, err := kc.loadCA(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("loading CA: %w", err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generating client private key: %w", err)
	}

	serial, err := newSerialNumber()
	if err != nil {
		return nil, nil, err
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: namespace,
		},
		NotBefore:   now.Add(-5 * time.Minute),
		NotAfter:    now.Add(clientCertValidity),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, nil, fmt.Errorf("creating client certificate: %w", err)
	}

	keyPEM, err = encodeECPrivateKeyPEM(key)
	if err != nil {
		return nil, nil, fmt.Errorf("encoding client private key: %w", err)
	}

	return encodePEM("CERTIFICATE", certDER), keyPEM, nil
}

func (kc *KubeClient) loadOrCreateCA(ctx context.Context) (*caBundle, error) {
	ca, err := kc.loadCA(ctx)
	if err == nil {
		return ca, nil
	}
	if !k8serrors.IsNotFound(err) {
		return nil, err
	}
	return kc.createCA(ctx)
}

func (kc *KubeClient) loadCA(ctx context.Context) (*caBundle, error) {
	secret, err := kc.client.CoreV1().Secrets(kc.SystemNamespace()).Get(ctx, defaultCASecret, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting secret %q in namespace %q: %w", defaultCASecret, kc.SystemNamespace(), err)
	}

	certPEM := secret.Data[secretKeyCert]
	keyPEM := secret.Data[secretKeyCAKey]

	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, fmt.Errorf("secret %q: no PEM certificate found", defaultCASecret)
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("secret %q: parsing CA certificate: %w", defaultCASecret, err)
	}
	if !cert.IsCA {
		return nil, fmt.Errorf("secret %q: certificate is not a CA", defaultCASecret)
	}
	if time.Now().After(cert.NotAfter) {
		return nil, fmt.Errorf("secret %q: CA certificate has expired", defaultCASecret)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("secret %q: no PEM private key found", defaultCASecret)
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("secret %q: parsing CA private key: %w", defaultCASecret, err)
	}

	return &caBundle{cert: cert, key: key, certPEM: certPEM}, nil
}

func (kc *KubeClient) createCA(ctx context.Context) (*caBundle, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating CA private key: %w", err)
	}

	serial, err := newSerialNumber()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: caCommonName,
		},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(caCertValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("creating CA certificate: %w", err)
	}

	certPEM := encodePEM("CERTIFICATE", certDER)
	keyPEM, err := encodeECPrivateKeyPEM(key)
	if err != nil {
		return nil, fmt.Errorf("encoding CA private key: %w", err)
	}

	if err := kc.putSecret(ctx, defaultCASecret, corev1.SecretTypeOpaque, map[string][]byte{
		secretKeyCert:  certPEM,
		secretKeyCAKey: keyPEM,
	}); err != nil {
		return nil, fmt.Errorf("storing secret %q: %w", defaultCASecret, err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("parsing generated CA certificate: %w", err)
	}

	return &caBundle{cert: cert, key: key, certPEM: certPEM}, nil
}

func (kc *KubeClient) putSecret(ctx context.Context, name string, secretType corev1.SecretType, data map[string][]byte) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: kc.SystemNamespace(),
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": kc.serviceAccountName,
			},
		},
		Type: secretType,
		Data: data,
	}

	_, err := kc.client.CoreV1().Secrets(kc.SystemNamespace()).Create(ctx, secret, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !k8serrors.IsAlreadyExists(err) {
		return err
	}

	_, err = kc.client.CoreV1().Secrets(kc.SystemNamespace()).Update(ctx, secret, metav1.UpdateOptions{})
	return err
}

func (kc *KubeClient) GetRootCA(ctx context.Context) (*x509.CertPool, error) {
	ca, err := kc.loadOrCreateCA(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading CA: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	if !pool.AppendCertsFromPEM(ca.certPEM) {
		return nil, fmt.Errorf("failed to append CA certificate to pool")
	}
	return pool, nil
}

func validateCertificate(cert *x509.Certificate, ca *x509.Certificate) error {
	now := time.Now()
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return fmt.Errorf("certificate is not currently valid (notBefore=%s, notAfter=%s)", cert.NotBefore, cert.NotAfter)
	}

	pool := x509.NewCertPool()
	pool.AddCert(ca)
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		return fmt.Errorf("certificate does not chain to CA: %w", err)
	}
	return nil
}

func serverDNSNames(serviceAccountName, systemNamespace string) []string {
	return []string{
		"localhost",
		serviceAccountName,
		fmt.Sprintf("%s.%s.svc", serviceAccountName, systemNamespace),
		fmt.Sprintf("%s.%s.svc.cluster.local", serviceAccountName, systemNamespace),
	}
}

func newSerialNumber() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("generating certificate serial number: %w", err)
	}
	return serial, nil
}

func encodePEM(blockType string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
}

func encodeECPrivateKeyPEM(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return encodePEM("EC PRIVATE KEY", der), nil
}
