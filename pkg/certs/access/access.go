/*
 * Copyright 2019 SAP SE or an SAP affiliate company. All rights reserved.
 * This file is licensed under the Apache Software License, v. 2 except as noted
 * otherwise in the LICENSE file
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

package access

import (
	"context"
	"crypto/tls"
	"github.com/gardener/controller-manager-library/pkg/certmgmt"
	"github.com/gardener/controller-manager-library/pkg/logger"
	"sync"
	"time"
)

type AccessSource struct {
	lock        sync.Mutex
	currentCert *tls.Certificate

	config *certmgmt.Config
	access certmgmt.CertificateAccess
	logger logger.LogContext
}

func New(ctx context.Context, logger logger.LogContext, access certmgmt.CertificateAccess, cfg *certmgmt.Config) (*AccessSource, error) {
	this := &AccessSource{
		config: cfg,
		access: access,
		logger: logger,
	}
	// Initial read of certificate and key.
	if err := this.ReadCertificate(); err != nil {
		return nil, err
	}

	this.start(ctx.Done())
	return this, nil
}

func (this *AccessSource) ReadCertificate() error {
	info, err := this.access.Get(this.logger)
	if err != nil {
		return err
	}
	new, err := certmgmt.UpdateCertificate(info, this.config)
	if err != nil {
		return err
	}
	if info != new || this.currentCert == nil {
		err = this.access.Set(this.logger, new)
		if err != nil {
			return err
		}
		info = new
	} else {
		return nil
	}

	cert, err := tls.X509KeyPair(info.Cert(), info.Key())
	if err != nil {
		return err
	}
	this.lock.Lock()
	defer this.lock.Unlock()
	this.currentCert = &cert
	return nil
}

// GetCertificate fetches the currently loaded certificate, which may be nil.
func (this *AccessSource) GetCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	this.lock.Lock()
	defer this.lock.Unlock()
	return this.currentCert, nil
}

func (this *AccessSource) start(stop <-chan struct{}) {
	go this.watch(stop)
}

func (this *AccessSource) watch(stop <-chan struct{}) {
	d := this.config.Rest
	if d > 10*time.Minute {
		d = 10 * time.Minute
	}
	backoff := 1 * time.Second

	timer := time.NewTimer(d)
	for {
		select {
		case <-stop:
			timer.Stop()
			return
		case _, ok := <-timer.C:
			if !ok {
				return
			}
			this.logger.Errorf("reconciling certificate %s", this.access)
			next := d

			err := this.ReadCertificate()
			if err != nil {
				this.logger.Errorf("cannot reconcile certificate %s: %s (backoff=%s)", this.access, err, backoff)
				next = backoff
				backoff = backoff * 3 / 2
			} else {
				backoff = 1 * time.Second
			}
			timer.Reset(next)
		}
	}
}
