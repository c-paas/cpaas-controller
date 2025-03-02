package certs

import "time"

// Default key size that should be used during generation of new keys
const DefaultKeySize = 2048

type CertOptions struct {
	CommonName   string
	Organization []string
	NotBefore    time.Time
	NotAfter     time.Time
}
