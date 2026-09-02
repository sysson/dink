// Package certs issues the self-signed CA and mTLS leaf certificates dink uses
// between the docker client and the control plane. It performs no I/O beyond
// reading the system entropy source: everything is returned as PEM blocks so
// callers can persist them, upload them to a Secret, or use them in tests.
package certs

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"
)

// KeyType selects the public key algorithm used for every certificate in a
// bundle. The CA and its leaves always share the same algorithm.
type KeyType string

const (
	KeyTypeECDSA   KeyType = "ecdsa"
	KeyTypeEd25519 KeyType = "ed25519"
	KeyTypeRSA     KeyType = "rsa"
)

const (
	DefaultOrganization = "dink"
	DefaultCACommonName = "dink-ca"
	DefaultClientCN     = "dink-client"
	DefaultServiceName  = "dink"
	DefaultNamespace    = "dink-system"
	DefaultKeyType      = KeyTypeECDSA
	DefaultRSABits      = 3072
	DefaultCADuration   = 3650 * 24 * time.Hour
	DefaultDuration     = 365 * 24 * time.Hour

	blockCertificate = "CERTIFICATE"
	blockPrivateKey  = "PRIVATE KEY"

	// Backdate to absorb clock skew between the issuing machine and the cluster.
	backdate = time.Hour
)

// KeyPair is a PEM-encoded certificate and its private key.
type KeyPair struct {
	Cert []byte
	Key  []byte
}

// Bundle is everything needed to run mTLS between a docker client and dink.
type Bundle struct {
	CA     KeyPair
	Server KeyPair
	Client KeyPair
}

// Options controls the contents of a generated Bundle. The zero value is
// usable and yields ECDSA P-256 keys with the default validity periods.
type Options struct {
	KeyType KeyType
	// RSABits is only consulted when KeyType is KeyTypeRSA.
	RSABits int

	// CADuration and Duration are the validity periods of the CA and of the
	// leaf certificates respectively.
	CADuration time.Duration
	Duration   time.Duration

	Organization string
	CACommonName string
	ClientCN     string

	// ServiceName, Namespace and ClusterDomain build the in-cluster DNS SANs
	// of the server certificate.
	ServiceName   string
	Namespace     string
	ClusterDomain string

	// ExtraDNSNames and ExtraIPs are appended to the server certificate SANs,
	// for reaching dink through an Ingress or a node IP.
	ExtraDNSNames []string
	ExtraIPs      []net.IP
}

func (o *Options) applyDefaults() error {
	if o.KeyType == "" {
		o.KeyType = DefaultKeyType
	}
	switch o.KeyType {
	case KeyTypeECDSA, KeyTypeEd25519, KeyTypeRSA:
	default:
		return fmt.Errorf("unsupported key type %q: must be one of %s, %s, %s",
			o.KeyType, KeyTypeECDSA, KeyTypeEd25519, KeyTypeRSA)
	}
	if o.RSABits == 0 {
		o.RSABits = DefaultRSABits
	}
	if o.KeyType == KeyTypeRSA && o.RSABits < 2048 {
		return fmt.Errorf("rsa key size %d is too small: minimum is 2048", o.RSABits)
	}
	if o.CADuration == 0 {
		o.CADuration = DefaultCADuration
	}
	if o.Duration == 0 {
		o.Duration = DefaultDuration
	}
	if o.CADuration <= 0 || o.Duration <= 0 {
		return fmt.Errorf("certificate durations must be positive")
	}
	if o.Organization == "" {
		o.Organization = DefaultOrganization
	}
	if o.CACommonName == "" {
		o.CACommonName = DefaultCACommonName
	}
	if o.ClientCN == "" {
		o.ClientCN = DefaultClientCN
	}
	if o.ServiceName == "" {
		o.ServiceName = DefaultServiceName
	}
	if o.Namespace == "" {
		o.Namespace = DefaultNamespace
	}
	if o.ClusterDomain == "" {
		o.ClusterDomain = "cluster.local"
	}
	return nil
}

// Authority is a certificate authority capable of issuing leaf certificates.
type Authority struct {
	cert *x509.Certificate
	key  crypto.Signer
	pair KeyPair
	opts Options
}

// KeyPair returns the PEM-encoded CA certificate and key. The key must never
// be uploaded to the cluster; only the certificate is needed to verify peers.
func (a *Authority) KeyPair() KeyPair { return a.pair }

// Certificate returns the parsed CA certificate.
func (a *Authority) Certificate() *x509.Certificate { return a.cert }

// NewAuthority creates a new self-signed certificate authority.
func NewAuthority(opts Options) (*Authority, error) {
	if err := opts.applyDefaults(); err != nil {
		return nil, err
	}

	key, err := generateKey(opts.KeyType, opts.RSABits)
	if err != nil {
		return nil, fmt.Errorf("generating CA key: %w", err)
	}

	serial, err := newSerial()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{opts.Organization},
			CommonName:   opts.CACommonName,
		},
		NotBefore:             now.Add(-backdate),
		NotAfter:              now.Add(opts.CADuration),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, fmt.Errorf("signing CA certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parsing generated CA certificate: %w", err)
	}

	keyPEM, err := encodeKey(key)
	if err != nil {
		return nil, err
	}

	return &Authority{
		cert: cert,
		key:  key,
		pair: KeyPair{Cert: encodePEM(blockCertificate, der), Key: keyPEM},
		opts: opts,
	}, nil
}

// LoadAuthority restores an Authority from previously generated PEM blocks so
// that new leaves can be issued without rotating the CA.
func LoadAuthority(pair KeyPair, opts Options) (*Authority, error) {
	if err := opts.applyDefaults(); err != nil {
		return nil, err
	}

	block, _ := pem.Decode(pair.Cert)
	if block == nil || block.Type != blockCertificate {
		return nil, fmt.Errorf("CA certificate is not a valid %s PEM block", blockCertificate)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing CA certificate: %w", err)
	}
	if !cert.IsCA {
		return nil, fmt.Errorf("certificate %q is not a CA", cert.Subject.CommonName)
	}

	block, _ = pem.Decode(pair.Key)
	if block == nil || block.Type != blockPrivateKey {
		return nil, fmt.Errorf("CA key is not a valid %s PEM block", blockPrivateKey)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing CA key: %w", err)
	}
	key, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("CA key of type %T cannot sign", parsed)
	}

	return &Authority{cert: cert, key: key, pair: pair, opts: opts}, nil
}

// LeafRequest describes a certificate to be issued by an Authority.
type LeafRequest struct {
	CommonName string
	// Organization overrides the Authority's default organization, used to
	// scope tenant client certificates to their namespace.
	Organization string
	ExtKeyUsage  []x509.ExtKeyUsage
	DNSNames     []string
	IPAddresses  []net.IP
}

// Issue signs a new leaf certificate with a freshly generated key.
func (a *Authority) Issue(req LeafRequest) (KeyPair, error) {
	if req.CommonName == "" {
		return KeyPair{}, fmt.Errorf("leaf certificate common name is required")
	}
	if len(req.ExtKeyUsage) == 0 {
		return KeyPair{}, fmt.Errorf("leaf certificate %q needs at least one extended key usage", req.CommonName)
	}

	key, err := generateKey(a.opts.KeyType, a.opts.RSABits)
	if err != nil {
		return KeyPair{}, fmt.Errorf("generating key for %q: %w", req.CommonName, err)
	}

	serial, err := newSerial()
	if err != nil {
		return KeyPair{}, err
	}

	// keyEncipherment is meaningless for ECDSA/Ed25519 and is rejected by some
	// verifiers, so it is only set for RSA where RSA key transport is possible.
	keyUsage := x509.KeyUsageDigitalSignature
	if a.opts.KeyType == KeyTypeRSA {
		keyUsage |= x509.KeyUsageKeyEncipherment
	}

	now := time.Now()
	notAfter := now.Add(a.opts.Duration)
	if notAfter.After(a.cert.NotAfter) {
		notAfter = a.cert.NotAfter
	}

	organization := a.opts.Organization
	if req.Organization != "" {
		organization = req.Organization
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{organization},
			CommonName:   req.CommonName,
		},
		NotBefore:             now.Add(-backdate),
		NotAfter:              notAfter,
		KeyUsage:              keyUsage,
		ExtKeyUsage:           req.ExtKeyUsage,
		BasicConstraintsValid: true,
		IsCA:                  false,
		DNSNames:              req.DNSNames,
		IPAddresses:           req.IPAddresses,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.cert, key.Public(), a.key)
	if err != nil {
		return KeyPair{}, fmt.Errorf("signing certificate for %q: %w", req.CommonName, err)
	}

	keyPEM, err := encodeKey(key)
	if err != nil {
		return KeyPair{}, err
	}

	return KeyPair{Cert: encodePEM(blockCertificate, der), Key: keyPEM}, nil
}

// IssueServer signs the dink control plane certificate, covering every in-cluster
// DNS name of the Service plus localhost for port-forwarded access.
func (a *Authority) IssueServer() (KeyPair, error) {
	o := a.opts
	dnsNames := []string{
		o.ServiceName,
		fmt.Sprintf("%s.%s", o.ServiceName, o.Namespace),
		fmt.Sprintf("%s.%s.svc", o.ServiceName, o.Namespace),
		fmt.Sprintf("%s.%s.svc.%s", o.ServiceName, o.Namespace, o.ClusterDomain),
		"localhost",
	}
	dnsNames = append(dnsNames, o.ExtraDNSNames...)

	ips := []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	ips = append(ips, o.ExtraIPs...)

	return a.Issue(LeafRequest{
		CommonName:  fmt.Sprintf("%s.%s.svc", o.ServiceName, o.Namespace),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    dnsNames,
		IPAddresses: ips,
	})
}

// IssueClient signs a docker client certificate.
func (a *Authority) IssueClient() (KeyPair, error) {
	return a.Issue(LeafRequest{
		CommonName:  a.opts.ClientCN,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		DNSNames:    []string{a.opts.ClientCN},
	})
}

// IssueTenantClient signs a client certificate scoped to a tenant: the
// certificate organization is the tenant namespace, identifying the caller to
// dink's identity middleware, and the common name is the client's own name.
func (a *Authority) IssueTenantClient(namespace, clientName string) (KeyPair, error) {
	if namespace == "" {
		return KeyPair{}, fmt.Errorf("tenant namespace is required")
	}
	if clientName == "" {
		return KeyPair{}, fmt.Errorf("client name is required")
	}
	return a.Issue(LeafRequest{
		CommonName:   clientName,
		Organization: namespace,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
}

// Generate produces a complete CA, server and client bundle in one call.
func Generate(opts Options) (*Bundle, error) {
	ca, err := NewAuthority(opts)
	if err != nil {
		return nil, err
	}
	return ca.Bundle()
}

// Bundle issues a fresh server and client pair from the Authority.
func (a *Authority) Bundle() (*Bundle, error) {
	server, err := a.IssueServer()
	if err != nil {
		return nil, err
	}
	client, err := a.IssueClient()
	if err != nil {
		return nil, err
	}
	return &Bundle{CA: a.pair, Server: server, Client: client}, nil
}

func generateKey(kt KeyType, rsaBits int) (crypto.Signer, error) {
	switch kt {
	case KeyTypeECDSA:
		return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case KeyTypeEd25519:
		_, key, err := ed25519.GenerateKey(rand.Reader)
		return key, err
	case KeyTypeRSA:
		return rsa.GenerateKey(rand.Reader, rsaBits)
	default:
		return nil, fmt.Errorf("unsupported key type %q", kt)
	}
}

func newSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generating certificate serial number: %w", err)
	}
	return serial, nil
}

func encodeKey(key crypto.Signer) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshalling private key: %w", err)
	}
	return encodePEM(blockPrivateKey, der), nil
}

func encodePEM(blockType string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
}
