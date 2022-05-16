/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package sig

import (
	"github.com/pkg/errors"
	"go.uber.org/zap/zapcore"
	"sync"
	"sync/atomic"

	"github.com/hyperledger-labs/fabric-smart-client/platform/view/driver"
)

type Deserializer interface {
	DeserializeVerifier(raw []byte) (driver.Verifier, error)
	DeserializeSigner(raw []byte) (driver.Signer, error)
	Info(raw []byte, auditInfo []byte) (string, error)
}

type deserializer struct {
	lock sync.Mutex

	sp            driver.ServiceProvider
	deserializers atomic.Value
}

func NewMultiplexDeserializer(sp driver.ServiceProvider) (*deserializer, error) {
	return &deserializer{
		sp:            sp,
	}, nil
}

func (d *deserializer) AddDeserializer(newD Deserializer) {
	d.lock.Lock()
	defer d.lock.Unlock()

	var deserializers []Deserializer
	prev := d.deserializers.Load()
	if prev != nil {
		deserializers = prev.([]Deserializer)
	}

	res := make([]Deserializer, len(deserializers) + 1)
	copy(res, deserializers)
	res[len(res) - 1] = newD

	d.deserializers.Store(res)
}

func (d *deserializer) getDeserializers() ([]Deserializer, error) {
	deserializers := d.deserializers.Load()
	if deserializers == nil {
		return nil, errors.Errorf("no deserializers registered")
	}

	return deserializers.([]Deserializer), nil
}

func (d *deserializer) DeserializeVerifier(raw []byte) (driver.Verifier, error) {
	deserializers, err := d.getDeserializers()
	if err != nil {
		return nil, err
	}

	var errs []error
	for _, des := range deserializers {
		if logger.IsEnabledFor(zapcore.DebugLevel) {
			logger.Debugf("trying deserialization with [%v]", des)
		}
		v, err := des.DeserializeVerifier(raw)
		if err == nil {
			if logger.IsEnabledFor(zapcore.DebugLevel) {
				logger.Debugf("trying deserialization with [%v] succeeded", des)
			}
			return v, nil
		}

		if logger.IsEnabledFor(zapcore.DebugLevel) {
			logger.Debugf("trying deserialization with [%v] failed", des)
		}
		errs = append(errs, err)
	}

	return nil, errors.Errorf("failed deserialization [%v]", errs)
}

func (d *deserializer) DeserializeSigner(raw []byte) (driver.Signer, error) {
	deserializers, err := d.getDeserializers()
	if err != nil {
		return nil, err
	}

	var errs []error
	for _, des := range deserializers {
		if logger.IsEnabledFor(zapcore.DebugLevel) {
			logger.Debugf("trying signer deserialization with [%s]", des)
		}
		v, err := des.DeserializeSigner(raw)
		if err == nil {
			if logger.IsEnabledFor(zapcore.DebugLevel) {
				logger.Debugf("trying signer deserialization with [%s] succeeded", des)
			}
			return v, nil
		}

		if logger.IsEnabledFor(zapcore.DebugLevel) {
			logger.Debugf("trying signer deserialization with [%s] failed [%s]", des, err)
		}
		errs = append(errs, err)
	}

	return nil, errors.Errorf("failed signer deserialization [%v]", errs)
}

func (d *deserializer) Info(raw []byte, auditInfo []byte) (string, error) {
	deserializers, err := d.getDeserializers()
	if err != nil {
		return "", err
	}

	var errs []error
	for _, des := range deserializers {
		if logger.IsEnabledFor(zapcore.DebugLevel) {
			logger.Debugf("trying info deserialization with [%v]", des)
		}
		v, err := des.Info(raw, auditInfo)
		if err == nil {
			if logger.IsEnabledFor(zapcore.DebugLevel) {
				logger.Debugf("trying info deserialization with [%v] succeeded", des)
			}
			return v, nil
		}

		if logger.IsEnabledFor(zapcore.DebugLevel) {
			logger.Debugf("trying info deserialization with [%v] failed", des)
		}
		errs = append(errs, err)
	}

	return "", errors.Errorf("failed info deserialization [%v]", errs)
}
