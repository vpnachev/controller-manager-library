/*
 * Copyright 2019 SAP SE or an SAP affiliate company. All rights reserved. This file is licensed under the Apache Software License, v. 2 except as noted otherwise in the LICENSE file
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 *
 */

package certmgmt

import (
	"crypto"
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/pkg/errors"

	"k8s.io/client-go/util/cert"
	"k8s.io/client-go/util/keyutil"
	"k8s.io/kubernetes/cmd/kubeadm/app/util/pkiutil"
)

type info struct {
	cert   []byte
	key    []byte
	cacert []byte
	cakey  []byte
}

func (this *info) Cert() []byte {
	return this.cert
}

func (this *info) CACert() []byte {
	return this.cacert
}

func (this *info) Key() []byte {
	return this.key
}

func (this *info) CAKey() []byte {
	return this.cakey
}

func NewCertInfo(cert []byte, key []byte, cacert []byte, cakey []byte) CertificateInfo {
	return &info{
		cert:   cert,
		key:    key,
		cacert: cacert,
		cakey:  cakey,
	}
}

func newPrivateKey() (*rsa.PrivateKey, error) {
	signer, err := pkiutil.NewPrivateKey()
	if err != nil {
		return nil, err
	}
	key, ok := signer.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("not a private key: %t", key)
	}
	return key, nil
}

// EncodePrivateKeyPEM returns PEM-encoded private key data
func encodePrivateKeyPEM(key *rsa.PrivateKey) []byte {
	block := pem.Block{
		Type:  pkiutil.RSAPrivateKeyBlockType,
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}
	return pem.EncodeToMemory(&block)
}

func UpdateCertificate(old CertificateInfo, cfg *Config) (CertificateInfo, error) {
	new := &info{}
	if old != nil {
		new.cert = old.Cert()
		new.key = old.Key()
		new.cacert = old.CACert()
		new.cakey = old.CAKey()
	}

	var caKey *rsa.PrivateKey
	var caCert *x509.Certificate
	var newKey *rsa.PrivateKey
	var newCert *x509.Certificate
	var err error
	var ok bool

	if !IsValid(new, cfg.DnsNames[0], cfg.Rest) {
		fmt.Printf("not valid\n")
		if new.cacert != nil {
			fmt.Printf("cacert found\n")
			ok = Valid(new.cakey, new.cacert, new.cacert, "", 5*time.Hour*24)
			if ok {
				fmt.Printf("cacert valid\n")
				k, err := keyutil.ParsePrivateKeyPEM(new.cakey)
				if err != nil {
					ok = false
				} else {
					caKey, ok = k.(*rsa.PrivateKey)
				}
				certs, err := cert.ParseCertsPEM(new.cacert)
				if err != nil {
					ok = false
				} else {
					caCert = certs[0]
				}
			}
		}
		if new.cacert == nil || !ok {
			fmt.Printf("generate cacert\n")

			caKey, err = newPrivateKey()
			if err != nil {
				return nil, fmt.Errorf("failed to create the CA key pair: %s", err)
			}
			new.cakey = encodePrivateKeyPEM(caKey)
			caCert, err = cert.NewSelfSignedCACert(cert.Config{CommonName: "webhook-certmgmt-ca:" + cfg.CommonName}, caKey)
			if err != nil {
				return nil, fmt.Errorf("failed to create the CA certmgmt: %s", err)
			}
			new.cacert = pkiutil.EncodeCertPEM(caCert)
		}

		fmt.Printf("generate key\n")
		newKey, err = newPrivateKey()
		if err != nil {
			return nil, fmt.Errorf("failed to create the server key pair: %s", err)
		}
		new.key = encodePrivateKeyPEM(newKey)
		fmt.Printf("generate certmgmt\n")
		newCert, err = NewSignedCert(
			&cert.Config{
				CommonName: cfg.CommonName,
				AltNames: cert.AltNames{
					DNSNames: cfg.DnsNames,
				},
				Usages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			},
			newKey, caCert, caKey, cfg.Validity)
		if err != nil {
			return nil, fmt.Errorf("failed to create the server certmgmt: %s", err)
		}
		new.cert = pkiutil.EncodeCertPEM(newCert)
		return new, nil
	}
	return old, nil
}

func IsValid(info CertificateInfo, dnsname string, duration time.Duration) bool {
	if info.Cert() == nil || info.Key() == nil {
		fmt.Printf("certmgmt or key not set\n")
		return false
	}
	if info.CACert() == nil {
		fmt.Printf("cacert not set\n")
		return false
	}
	return Valid(info.Key(), info.Cert(), info.CACert(), dnsname, duration)
}

func Valid(key []byte, cert []byte, cacert []byte, dnsname string, duration time.Duration) bool {

	if len(cert) == 0 || len(key) == 0 || len(cacert) == 0 {
		fmt.Printf("something empty\n")
		return false
	}

	_, err := tls.X509KeyPair(cert, key)
	if err != nil {
		fmt.Printf("key does not match certmgmt\n")
		return false
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(cacert) {
		fmt.Printf("cannot create pool\n")
		return false
	}
	block, _ := pem.Decode([]byte(cert))
	if block == nil {
		fmt.Printf("cannot decode certmgmt\n")
		return false
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		fmt.Printf("cannot parse certmgmt\n")
		return false
	}
	ops := x509.VerifyOptions{
		DNSName:     dnsname,
		Roots:       pool,
		CurrentTime: time.Now().Add(duration),
	}
	_, err = c.Verify(ops)
	fmt.Printf("val: %s\n", err)
	return err == nil
}

// NewSignedCert creates a signed certificate using the given CA certificate and key with the given validity duration
func NewSignedCert(cfg *cert.Config, key crypto.Signer, caCert *x509.Certificate, caKey crypto.Signer, duration time.Duration) (*x509.Certificate, error) {
	serial, err := cryptorand.Int(cryptorand.Reader, new(big.Int).SetInt64(math.MaxInt64))
	if err != nil {
		return nil, err
	}
	if len(cfg.CommonName) == 0 {
		return nil, errors.New("must specify a CommonName")
	}
	if len(cfg.Usages) == 0 {
		return nil, errors.New("must specify at least one ExtKeyUsage")
	}

	certTmpl := x509.Certificate{
		Subject: pkix.Name{
			CommonName:   cfg.CommonName,
			Organization: cfg.Organization,
		},
		DNSNames:     cfg.AltNames.DNSNames,
		IPAddresses:  cfg.AltNames.IPs,
		SerialNumber: serial,
		NotBefore:    caCert.NotBefore,
		NotAfter:     time.Now().Add(duration).UTC(),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  cfg.Usages,
	}
	certDERBytes, err := x509.CreateCertificate(cryptorand.Reader, &certTmpl, caCert, key.Public(), caKey)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(certDERBytes)
}